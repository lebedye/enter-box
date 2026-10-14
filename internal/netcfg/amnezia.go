package netcfg

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	awgconn "github.com/amnezia-vpn/amneziawg-go/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/tun"

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

	ipc := buildIPC(ep)
	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return "", fmt.Errorf("ipc set: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", fmt.Errorf("device up: %w", err)
	}

	for _, a := range ep.AWG.Address {
		if err := runCmd(ctx, "ip", "addr", "add", a, "dev", ifaceName); err != nil {
			m.logger.Warn("addr add", "iface", ifaceName, "addr", a, "err", err)
		}
	}
	_ = runCmd(ctx, "ip", "link", "set", "dev", ifaceName, "mtu", strconv.Itoa(mtu))
	if err := runCmd(ctx, "ip", "link", "set", ifaceName, "up"); err != nil {
		dev.Close()
		return "", fmt.Errorf("link up: %w", err)
	}
	if wanIface != "" && ep.Server != "" {
		_ = runCmd(ctx, "ip", "route", "add", ep.Server+"/32", "dev", wanIface)
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

func buildIPC(ep *parser.Endpoint) string {
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

	add("private_key", p.PrivateKey)
	add("listen_port", "0")
	add("public_key", p.PeerPublic)
	add("preshared_key", p.Preshared)
	add("endpoint", fmt.Sprintf("%s:%d", ep.Server, ep.Port))
	add("persistent_keepalive_interval", "25")
	add("allowed_ip", "0.0.0.0/0")
	add("allowed_ip", "::/0")

	addInt("jc", p.Jc)
	addInt("jmin", p.Jmin)
	addInt("jmax", p.Jmax)
	addInt("s1", p.S1)
	addInt("s2", p.S2)
	addInt("h1", p.H1)
	addInt("h2", p.H2)
	addInt("h3", p.H3)
	addInt("h4", p.H4)
	add("i1", p.I1)
	add("i2", p.I2)
	add("i3", p.I3)
	add("i4", p.I4)
	add("i5", p.I5)

	return sb.String()
}
