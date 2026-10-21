package netcfg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	awgconn "github.com/amnezia-vpn/amneziawg-go/v3/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/v3/tun"

	"enter-box/internal/parser"
)

// AmneziaManager управляет набором userspace AmneziaWG-интерфейсов.
type AmneziaManager struct {
	logger  *slog.Logger
	mu      sync.Mutex
	devices map[string]*awgdevice.Device
	tuns    map[string]awgtun.Device
	ips     map[string][]string
}

func NewAmneziaManager(logger *slog.Logger) *AmneziaManager {
	return &AmneziaManager{
		logger:  logger,
		devices: make(map[string]*awgdevice.Device),
		tuns:    make(map[string]awgtun.Device),
		ips:     make(map[string][]string),
	}
}

// Up идемпотентен: если интерфейс уже поднят — возвращает его имя.
func (m *AmneziaManager) Up(ctx context.Context, ep *parser.Endpoint, wanIface string) (string, error) {
	if ep.AWG == nil {
		return "", fmt.Errorf("amnezia: no AWG params")
	}
	ifaceName := parser.AwgIfaceName(ep)

	m.mu.Lock()
	_, already := m.devices[ifaceName]
	m.mu.Unlock()
	if already {
		return ifaceName, nil
	}

	mtu := ep.AWG.MTU
	if mtu == 0 {
		mtu = 1420
	}

	tunDev, err := awgtun.CreateTUN(ifaceName, mtu)
	if err != nil {
		return "", fmt.Errorf("create tun %s: %w", ifaceName, err)
	}
	ifaceName, _ = tunDev.Name()

	devLogger := awgdevice.NewLogger(awgdevice.LogLevelError, fmt.Sprintf("(awg-%s) ", ep.Tag))
	dev := awgdevice.NewDevice(tunDev, awgconn.NewDefaultBind(), devLogger)
	// Закрепляем адрес VPN-сервера за физическим WAN до запуска sing-box.
	// Иначе новый default/TUN-маршрут может завернуть сам VPN обратно в себя.
	if err := pinEndpointRoute(ctx, ep.Server, wanIface); err != nil {
		dev.Close()
		return "", err
	}

	ipc, err := buildIPC(ep)
	if err != nil {
		dev.Close()
		return "", fmt.Errorf("prepare ipc: %w", err)
	}
	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return "", fmt.Errorf("ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", fmt.Errorf("device up: %w", err)
	}

	for _, a := range ep.AWG.Address {
		if err := runCmd(ctx, "ip", "addr", "replace", a, "dev", ifaceName); err != nil {
			dev.Close()
			return "", fmt.Errorf("AWG address: %w", err)
		}
	}
	_ = runCmd(ctx, "ip", "link", "set", "dev", ifaceName, "mtu", strconv.Itoa(mtu))
	if err := runCmd(ctx, "ip", "link", "set", ifaceName, "up"); err != nil {
		dev.Close()
		return "", fmt.Errorf("link up: %w", err)
	}
	// SO_BINDTODEVICE выбирает маршрут только через AWG. AllowedIPs в UAPI
	// не создаёт Linux-маршруты, поэтому нужен default с высоким metric:
	// обычный трафик по-прежнему выбирает физический WAN с меньшим metric.
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		dev.Close()
		return "", err
	}
	if err := runCmd(ctx, "ip", "route", "replace", "default", "dev", ifaceName, "metric", strconv.Itoa(32000+iface.Index)); err != nil {
		dev.Close()
		return "", fmt.Errorf("AWG outbound route: %w", err)
	}
	m.mu.Lock()
	m.devices[ifaceName] = dev
	m.tuns[ifaceName] = tunDev
	m.ips[ifaceName] = ep.AWG.Address
	m.mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	return ifaceName, nil
}

func (m *AmneziaManager) Down() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, dev := range m.devices {
		dev.Close()
		_ = runCmd(context.Background(), "ip", "link", "del", name)
	}
	m.devices = nil
	m.tuns = nil
	m.ips = nil
}

func buildIPC(ep *parser.Endpoint) (string, error) {
	p := ep.AWG
	var sb strings.Builder

	add := func(k, v string) {
		if v == "" {
			return
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	addInt := func(k string, v int) {
		if v <= 0 {
			return
		}
		add(k, strconv.Itoa(v))
	}

	// UAPI amneziawg-go принимает 32-байтовые ключи в hex, а экспорт
	// Amnezia хранит их в стандартном WireGuard base64.
	privateKey, err := wireGuardKeyHex(p.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	peerKey, err := wireGuardKeyHex(p.PeerPublic)
	if err != nil {
		return "", fmt.Errorf("peer public key: %w", err)
	}
	add("private_key", privateKey)
	add("listen_port", "0")
	// 0x2024 — output-mark sing-box auto_redirect. Метка исключает внешние
	// UDP-пакеты AWG из повторного захвата и совпадает с bypass ip rule.
	add("fwmark", strconv.Itoa(0x2024))
	presharedKey := ""
	if p.Preshared != "" {
		presharedKey, err = wireGuardKeyHex(p.Preshared)
		if err != nil {
			return "", fmt.Errorf("preshared key: %w", err)
		}
	}

	// Все device-параметры должны идти до public_key: эта строка начинает
	// peer-секцию UAPI, и последующие jc/s1/... иначе считаются peer-ключами.
	addInt("jc", p.Jc)
	addInt("jmin", p.Jmin)
	addInt("jmax", p.Jmax)
	addInt("s1", p.S1)
	addInt("s2", p.S2)
	addInt("s3", p.S3)
	addInt("s4", p.S4)
	add("h1", p.H1)
	add("h2", p.H2)
	add("h3", p.H3)
	add("h4", p.H4)
	add("i1", p.I1)
	add("i2", p.I2)
	add("i3", p.I3)
	add("i4", p.I4)
	add("i5", p.I5)
	if p.HeaderProtectionKey != "" {
		headerKey, err := wireGuardKeyHex(p.HeaderProtectionKey)
		if err != nil {
			return "", fmt.Errorf("header protection key: %w", err)
		}
		add("header_protection_key", headerKey)
	}
	add("content_padding_addition", p.ContentPaddingAddition)
	add("rekey_after_time", p.RekeyAfterTime)
	add("rekey_timeout", p.RekeyTimeout)
	add("reject_after_time", p.RejectAfterTime)
	add("keepalive_timeout", p.KeepaliveTimeout)
	add("max_handshake_attempts", p.MaxHandshakeAttempts)
	addInt("random_trailers", p.RandomTrailers)
	addInt("disable_cookies", p.DisableCookies)

	add("public_key", peerKey)
	add("preshared_key", presharedKey)
	add("endpoint", net.JoinHostPort(ep.Server, strconv.Itoa(int(ep.Port))))
	keepalive := p.PersistentKeepalive
	if keepalive == "" {
		keepalive = "25"
	}
	add("persistent_keepalive_interval", keepalive)
	add("allowed_ip", "0.0.0.0/0")
	add("allowed_ip", "::/0")

	return sb.String(), nil
}

func pinEndpointRoute(ctx context.Context, server, wanIface string) error {
	if server == "" || wanIface == "" {
		return nil
	}
	ip := net.ParseIP(server)
	if ip == nil {
		// В конфигурациях обычно уже указан IP, но доменное имя тоже поддерживаем.
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip4", server)
		if err != nil {
			return fmt.Errorf("resolve AWG endpoint %s: %w", server, err)
		}
		if len(resolved) == 0 {
			return fmt.Errorf("resolve AWG endpoint %s: адрес не найден", server)
		}
		ip = resolved[0]
	}
	if ip.To4() == nil {
		return fmt.Errorf("AWG endpoint IPv6 пока не поддерживается: %s", server)
	}

	out, err := exec.CommandContext(ctx, "ip", "-4", "route", "get", ip.String()).Output()
	if err != nil {
		return fmt.Errorf("find WAN route for %s: %w", ip, err)
	}
	fields := strings.Fields(string(out))
	gateway := ""
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "via" {
			gateway = fields[i+1]
			break
		}
	}
	args := []string{"route", "replace", ip.String() + "/32"}
	if gateway != "" {
		args = append(args, "via", gateway)
	}
	args = append(args, "dev", wanIface)
	if err := runCmd(ctx, "ip", args...); err != nil {
		return fmt.Errorf("pin AWG endpoint to WAN: %w", err)
	}
	return nil
}

func wireGuardKeyHex(value string) (string, error) {
	value = strings.TrimSpace(value)
	// Повторный импорт уже нормализованного UAPI-конфига тоже поддерживаем.
	if raw, err := hex.DecodeString(value); err == nil && len(raw) == 32 {
		return strings.ToLower(value), nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("ожидалось 32 байта, получено %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
