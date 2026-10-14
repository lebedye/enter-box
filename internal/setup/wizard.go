package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enter-box/internal/config"
)

const defaultUnitPath = "/etc/systemd/system/enter-box.service"

type Wizard struct {
	p          *Prompter
	configPath string
	existing   *config.Config
}

func NewWizard(configPath string) *Wizard {
	w := &Wizard{p: NewPrompter(), configPath: configPath}
	if config.Exists(configPath) {
		if c, err := config.Load(configPath); err == nil {
			w.existing = c
		}
	}
	return w
}

// Run проходит весь сценарий установки.
//
// Порядок шагов важен:
//  1. preflight     — критические проверки окружения;
//  2. ensureSSH     — иначе после смены IP потеряем доступ;
//  3. ensureNet*    — гасим менеджеры, чтобы не перезаписали IP;
//  4. интерфейсы    — WAN/LAN;
//  5. extra LAN     — WiFi/второй Ethernet (опционально);
//  6. LAN/DHCP      — адрес шлюза и диапазон;
//  7. warnLanChange — предупреждаем о смене IP на LAN;
//  8. endpoints     — VPN-узлы;
//  9. routing+DNS   — правила и DNS-сервер;
//  10. confirm+save  — проверка и запись;
//  11. systemd       — install + enable + start.
func (w *Wizard) Run() (*config.Config, error) {
	w.p.Title("enter-box — интерактивная установка")

	if fails := PrintChecks(w.p, Preflight()); fails > 0 {
		return nil, fmt.Errorf("прервано из-за критичных проблем в окружении")
	}

	if err := w.ensureSSH(); err != nil {
		return nil, err
	}
	if err := w.ensureNetworkServicesDisabled(); err != nil {
		return nil, err
	}

	cfg, err := w.askInterfaces()
	if err != nil {
		return nil, err
	}

	w.askExtraLAN(cfg)
	w.askLAN(cfg)

	if err := w.warnLanChange(cfg); err != nil {
		return nil, err
	}
	if err := w.askEndpoints(cfg); err != nil {
		return nil, err
	}
	w.askRouting(cfg)

	if !w.confirm(cfg) {
		return nil, fmt.Errorf("отменено пользователем")
	}
	if err := cfg.Save(w.configPath); err != nil {
		return nil, fmt.Errorf("save: %w", err)
	}
	w.p.OK("конфиг записан: %s", w.configPath)
	w.offerSystemd()
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Шаги 2 и 3: SSH и сетевые менеджеры
// ---------------------------------------------------------------------------

func (w *Wizard) ensureSSH() error {
	if sshActive() {
		return nil
	}
	w.p.Warn("SSH-сервер не активен. Без него вы не сможете зайти на устройство.")
	if !w.p.AskYesNo("Установить и включить openssh-server?", true) {
		return nil
	}
	if err := runSystemctlOrCmd("apt", "install", "-y", "openssh-server"); err != nil {
		return fmt.Errorf("apt install openssh-server: %w", err)
	}
	if err := runSystemctlOrCmd("systemctl", "enable", "--now", "ssh"); err != nil {
		return fmt.Errorf("enable ssh: %w", err)
	}
	w.p.OK("SSH включён")
	return nil
}

// ensureNetworkServicesDisabled гасит NetworkManager, systemd-networkd, dhcpcd.
// Без этого они перезапишут IP на LAN-интерфейсе через минуту после старта
// enter-box и сломают маршрутизацию.
//
// Применяем stop + disable + mask: mask нужен, чтобы сервис не поднялся
// повторно как зависимость или при обновлении пакета.
func (w *Wizard) ensureNetworkServicesDisabled() error {
	conflicts := []string{"NetworkManager", "systemd-networkd", "dhcpcd"}
	var active []string
	for _, svc := range conflicts {
		if serviceState(svc) == "active" {
			active = append(active, svc)
		}
	}
	if len(active) == 0 {
		return nil
	}

	w.p.Warn("Обнаружены активные сетевые менеджеры: %v", active)
	w.p.Info("Они перезапишут IP на LAN-интерфейсе и сломают enter-box.")
	w.p.Info("Рекомендуется отключить (stop + disable + mask).")

	if !w.p.AskYesNo("Отключить их сейчас?", true) {
		w.p.Warn("Отключение пропущено. Если что-то сломается — вернитесь сюда.")
		w.p.Info("  Ручное отключение:")
		for _, svc := range active {
			w.p.Info("    sudo systemctl stop %s && sudo systemctl disable %s && sudo systemctl mask %s",
				svc, svc, svc)
		}
		return nil
	}

	for _, svc := range active {
		_ = runSystemctl("stop", svc)
		_ = runSystemctl("disable", svc)
		_ = runSystemctl("mask", svc)
		w.p.OK("%s остановлен, отключён и замаскирован", svc)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Шаг 4: интерфейсы WAN/LAN
// ---------------------------------------------------------------------------

func (w *Wizard) askInterfaces() (*config.Config, error) {
	w.p.Title("Шаг 1/7. Сетевые интерфейсы")

	ifaces, err := DetectInterfaces()
	if err != nil {
		return nil, fmt.Errorf("detect interfaces: %w", err)
	}
	if len(ifaces) < 2 {
		return nil, fmt.Errorf("нужно минимум 2 интерфейса, найдено %d", len(ifaces))
	}

	w.p.Info("Найдены интерфейсы:")
	for _, ifc := range ifaces {
		w.p.Info("  %-10s %s", ifc.Name, ifc.Description)
	}
	w.p.Info("")

	// WAN.
	wanIdx := guessWAN(ifaces, w.existing)
	wanOptions := make([]string, len(ifaces))
	for idx, ifc := range ifaces {
		wanOptions[idx] = fmt.Sprintf("%s %s", ifc.Name, ifc.Description)
	}
	wanChoice := w.p.AskChoice("Куда приходит интернет (WAN)?", wanOptions, wanIdx)
	wan := ifaces[wanChoice]

	// LAN — все остальные, кроме WAN.
	remaining := make([]Iface, 0, len(ifaces)-1)
	for _, ifc := range ifaces {
		if ifc.Name != wan.Name {
			remaining = append(remaining, ifc)
		}
	}
	lanIdx := guessLAN(remaining, w.existing)
	lanOptions := make([]string, len(remaining))
	for idx, ifc := range remaining {
		lanOptions[idx] = fmt.Sprintf("%s %s", ifc.Name, ifc.Description)
	}
	lanChoice := w.p.AskChoice("Какой интерфейс смотрит в коммутатор (LAN)?", lanOptions, lanIdx)
	lan := remaining[lanChoice]

	cfg := &config.Config{}
	cfg.WAN.Name = wan.Name
	cfg.LAN.Interface = lan.Name
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Шаг 5: дополнительные LAN-интерфейсы (WiFi, второй Ethernet)
// ---------------------------------------------------------------------------

func (w *Wizard) askExtraLAN(cfg *config.Config) {
	w.p.Title("Шаг 2/7. Дополнительные LAN-интерфейсы")

	w.p.Info("Можно раздавать интернет не только через основной Ethernet,")
	w.p.Info("но и через WiFi (нужен поднятый AP) или второй USB-Ethernet.")
	w.p.Info("")

	if w.p.AskYesNo("Раздавать интернет через WiFi?", false) {
		iface := w.askWifiInterface(cfg.LAN.Interface)
		if iface != "" {
			cfg.LAN.ExtraInterfaces = append(cfg.LAN.ExtraInterfaces, iface)
			w.p.Info("WiFi-интерфейс %s добавлен в LAN", iface)
			w.p.Warn("Убедитесь, что AP поднят: hostapd запущен, %s в режиме AP.", iface)
			w.p.Warn("enter-box не запускает hostapd — он только использует готовый AP.")
		}
	}

	w.p.Info("")
	w.p.Info("Если есть второй Ethernet или USB-адаптер в коммутатор —")
	w.p.Info("перечислите интерфейсы через запятую (иначе пусто).")
	line := w.p.AskString("Дополнительные LAN (usb1, enx...)", "")
	if line != "" {
		for _, raw := range strings.Split(line, ",") {
			name := strings.TrimSpace(raw)
			if name == "" || name == cfg.LAN.Interface {
				continue
			}
			if !containsString(cfg.LAN.ExtraInterfaces, name) {
				cfg.LAN.ExtraInterfaces = append(cfg.LAN.ExtraInterfaces, name)
			}
		}
	}

	if len(cfg.LAN.ExtraInterfaces) > 0 {
		w.p.Info("Итоговый список extra LAN: %v", cfg.LAN.ExtraInterfaces)
	}
}

// askWifiInterface выбирает беспроводной интерфейс и предлагает создать
// AP-конфиг (SSID + пароль). Если пользователь соглашается — wizard
// генерирует hostapd.conf, но НЕ запускает его автоматически.
func (w *Wizard) askWifiInterface(mainLAN string) string {
	ifaces, _ := DetectInterfaces()
	var wifiIfaces []Iface
	for _, ifc := range ifaces {
		if ifc.Kind == "wireless" && ifc.Name != mainLAN {
			wifiIfaces = append(wifiIfaces, ifc)
		}
	}
	if len(wifiIfaces) == 0 {
		w.p.Warn("Беспроводных интерфейсов не найдено — пропускаем.")
		return ""
	}

	options := make([]string, len(wifiIfaces))
	for idx, ifc := range wifiIfaces {
		options[idx] = fmt.Sprintf("%s %s", ifc.Name, ifc.Description)
	}
	choice := w.p.AskChoice("Какой WiFi-интерфейс использовать?", options, 0)
	chosen := wifiIfaces[choice].Name

	w.p.Info("")
	w.p.Info("Для точки доступа понадобится SSID и пароль (WPA2).")
	w.p.Info("enter-box сохранит их в отдельный файл hostapd.conf, но")
	w.p.Info("запускать hostapd придётся вам (или через systemd-юнит).")
	if !w.p.AskYesNo("Сгенерировать hostapd.conf для этого интерфейса?", true) {
		return chosen
	}

	ssid := w.p.AskSSID("SSID сети")
	pass := w.p.AskWifiPassword("Пароль (WPA2, минимум 8 символов)")

	if err := writeHostapdConfig(chosen, ssid, pass); err != nil {
		w.p.Warn("не удалось записать hostapd.conf: %v", err)
	} else {
		w.p.OK("hostapd.conf записан: /etc/hostapd/hostapd.conf")
		w.p.Info("Запуск: sudo systemctl enable --now hostapd")
	}
	return chosen
}

// writeHostapdConfig создаёт /etc/hostapd/hostapd.conf.
// 2.4 GHz, WPA2-PSK, страна RU по умолчанию (правится вручную).
func writeHostapdConfig(iface, ssid, password string) error {
	conf := fmt.Sprintf(`# hostapd.conf для enter-box
# Интерфейс: %s
# Сгенерировано enter-box setup

interface=%s
driver=nl80211
ssid=%s

# 2.4 GHz (для 5 GHz поменяйте hw_mode на a и канал на 36..)
hw_mode=g
channel=6
ieee80211n=1
ieee80211d=1
country_code=RU

# WPA2-PSK
wpa=2
wpa_passphrase=%s
wpa_key_mgmt=WPA-PSK
wpa_pairwise=CCMP
rsn_pairwise=CCMP

# Базовая политика
auth_algs=1
ignore_broadcast_ssid=0
`, iface, iface, ssid, password)

	if err := os.MkdirAll("/etc/hostapd", 0o755); err != nil {
		return err
	}
	return os.WriteFile("/etc/hostapd/hostapd.conf", []byte(conf), 0o600)
}

// ---------------------------------------------------------------------------
// Шаг 6: LAN-адрес и DHCP
// ---------------------------------------------------------------------------

func (w *Wizard) askLAN(cfg *config.Config) {
	w.p.Title("Шаг 3/7. LAN-адрес и DHCP")

	defAddr := "192.168.88.1/24"
	if w.existing != nil && w.existing.LAN.Address != "" {
		defAddr = w.existing.LAN.Address
	}
	cfg.LAN.Address = w.p.AskString("IP-адрес шлюза для клиентов (CIDR)", defAddr)

	cfg.LAN.DHCP.Enabled = w.p.AskYesNo("Включить DHCP-сервер для клиентов?", true)
	if !cfg.LAN.DHCP.Enabled {
		return
	}

	// Префикс берём из введённого адреса: 192.168.88.1/24 → 192.168.88
	prefix := "192.168.88"
	if host, _, ok := strings.Cut(cfg.LAN.Address, "/"); ok {
		parts := strings.Split(host, ".")
		if len(parts) >= 3 {
			prefix = parts[0] + "." + parts[1] + "." + parts[2]
		}
	}

	cfg.LAN.DHCP.RangeStart = w.p.AskString("Начало диапазона DHCP", prefix+".100")
	cfg.LAN.DHCP.RangeEnd = w.p.AskString("Конец диапазона DHCP", prefix+".250")
	cfg.LAN.DHCP.LeaseTime = time.Hour

	if w.p.AskYesNo("Есть устройства со статическими адресами внутри диапазона?", false) {
		excl := w.p.AskMultiLine(
			"Введите адреса или диапазоны (a-b), по одному на строку:",
			"например: 192.168.88.50  или  192.168.88.60-192.168.88.65",
		)
		cfg.LAN.DHCP.Exclude = excl
	}
}

// warnLanChange предупреждает о смене IP на LAN-интерфейсе,
// сравнивая текущий адрес с выбранным в askLAN.
//
// Вызывается ПОСЛЕ askLAN, иначе cfg.LAN.Address ещё не задан.
func (w *Wizard) warnLanChange(cfg *config.Config) error {
	curIP := interfaceIP(cfg.LAN.Interface)
	if curIP == "" {
		return nil
	}

	newIP, _, _ := strings.Cut(cfg.LAN.Address, "/")
	if curIP == newIP {
		return nil
	}

	w.p.Warn("Внимание: IP на %s сейчас %s.", cfg.LAN.Interface, curIP)
	w.p.Info("  После setup он станет %s.", cfg.LAN.Address)
	w.p.Info("  Если зашли по SSH через %s — сессия прервётся.", cfg.LAN.Interface)
	w.p.Info("  Для переподключения используйте адрес %s или физический доступ.", newIP)
	if !w.p.AskYesNo("Продолжить?", true) {
		return fmt.Errorf("отменено")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Шаг 7: VPN-узлы
// ---------------------------------------------------------------------------

func (w *Wizard) askEndpoints(cfg *config.Config) error {
	w.p.Title("Шаг 4/7. VPN-узлы")
	w.p.Info("Можно вставить:")
	w.p.Info("  • vless://...    (VLESS + Reality/TLS/WS)")
	w.p.Info("  • vmess://...    (VMess)")
	w.p.Info("  • vpn://...      (AmneziaWG из клиента AmneziaVPN)")
	w.p.Info("")
	w.p.Info("Вставьте ссылки по одной на строку. Пустая строка — закончить.")
	cfg.Endpoints = w.p.AskMultiLine("Прямые ссылки:", "формат: vless://uuid@host:443?...")

	if w.p.AskYesNo("Добавить HTTP(S)-подписку (URL со списком узлов)?", false) {
		for {
			url := w.p.AskString("URL подписки (пусто — закончить)", "")
			if url == "" {
				break
			}
			tag := w.p.AskString("Короткое имя (tag) для этой подписки", "sub")
			ua := w.p.AskString("User-Agent (опционально)", "enter-box/1.0")
			cfg.Subscriptions = append(cfg.Subscriptions, config.SubscriptionConfig{
				URL:       url,
				Tag:       tag,
				UserAgent: ua,
			})
			if !w.p.AskYesNo("Добавить ещё одну подписку?", false) {
				break
			}
		}
	}

	if len(cfg.Endpoints) == 0 && len(cfg.Subscriptions) == 0 {
		return fmt.Errorf("не указано ни одного узла — enter-box не сможет ходить в VPN")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Шаги 8 и 9: routing и DNS
// ---------------------------------------------------------------------------

func (w *Wizard) askRouting(cfg *config.Config) {
	w.p.Title("Шаг 5/7. Маршрутизация")
	w.p.Info("enter-box умеет пускать RU-сервисы (ВК, Госуслуги, Сбер, .ru, .рф)")
	w.p.Info("напрямую через WAN, а всё остальное — через VPN.")

	cfg.Routing.UseRUDirect = w.p.AskYesNo("Обход белых списков: RU → direct, остальное → VPN?", true)
	cfg.Routing.Final = "proxy"
	cfg.Routing.DomesticSuffix = []string{".ru", ".рф"}
	cfg.Routing.URLTestInterval = 3 * time.Minute
	cfg.Routing.URLTestURL = "http://cp.cloudflare.com/generate_204"
	cfg.Routing.URLTestTolerance = 50

	if w.p.AskYesNo("Добавить свои домены, которые всегда напрямую?", false) {
		cfg.Routing.DomesticDomains = w.p.AskMultiLine(
			"Домены (по одному на строку):", "например: mycompany.ru")
	}

	w.p.Title("Шаг 6/7. DNS")
	cfg.DNS.Serve = w.p.AskYesNo("Поднять встроенный DNS-сервер для клиентов?", true)
	if cfg.DNS.Serve {
		cfg.DNS.Domestic = []string{"77.88.8.8", "77.88.8.1"}
		cfg.DNS.Remote = []string{"1.1.1.1", "8.8.8.8"}
	}
}

// ---------------------------------------------------------------------------
// Шаги 10 и 11: подтверждение, запись, systemd
// ---------------------------------------------------------------------------

func (w *Wizard) confirm(cfg *config.Config) bool {
	w.p.Title("Шаг 7/7. Проверьте настройки")
	w.p.Info("WAN (интернет):      %s", cfg.WAN.Name)
	w.p.Info("LAN (коммутатор):    %s", cfg.LAN.Interface)
	if len(cfg.LAN.ExtraInterfaces) > 0 {
		w.p.Info("Extra LAN:           %v", cfg.LAN.ExtraInterfaces)
	}
	w.p.Info("Адрес шлюза:         %s", cfg.LAN.Address)
	if cfg.LAN.DHCP.Enabled {
		w.p.Info("DHCP:                %s — %s (lease %s)",
			cfg.LAN.DHCP.RangeStart, cfg.LAN.DHCP.RangeEnd, cfg.LAN.DHCP.LeaseTime)
		if len(cfg.LAN.DHCP.Exclude) > 0 {
			w.p.Info("DHCP exclude:        %v", cfg.LAN.DHCP.Exclude)
		}
	} else {
		w.p.Info("DHCP:                выключен")
	}
	w.p.Info("DNS-сервер:          %v", cfg.DNS.Serve)
	w.p.Info("Узлов (endpoints):   %d", len(cfg.Endpoints))
	w.p.Info("Подписок:            %d", len(cfg.Subscriptions))
	w.p.Info("RU → direct:         %v", cfg.Routing.UseRUDirect)
	w.p.Info("")
	w.p.Info("Файл будет записан:  %s", w.configPath)
	w.p.Info("")

	return w.p.AskYesNo("Записать и продолжить?", true)
}

func (w *Wizard) offerSystemd() {
	if !isSystemdPresent() {
		w.p.Info("systemd не обнаружен — запустите enter-box вручную:")
		w.p.Info("   sudo enter-box run -c %s", w.configPath)
		return
	}

	if !w.p.AskYesNo("Установить systemd-сервис и включить автозапуск?", true) {
		w.p.Info("Запуск вручную: sudo enter-box run -c %s", w.configPath)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "/usr/local/bin/enter-box"
	}
	if err := writeSystemdUnit(defaultUnitPath, exe, w.configPath); err != nil {
		w.p.Warn("не удалось записать unit: %v", err)
		w.p.Info("Запуск вручную: sudo enter-box run -c %s", w.configPath)
		return
	}

	if err := runSystemctl("daemon-reload"); err != nil {
		w.p.Warn("daemon-reload: %v", err)
	}
	if err := runSystemctl("enable", "--now", "enter-box"); err != nil {
		w.p.Warn("enable --now: %v", err)
		w.p.Info("Проверьте лог: sudo journalctl -u enter-box -f")
		return
	}
	w.p.OK("служба enter-box включена и запущена")
	w.p.Info("Логи:       sudo journalctl -u enter-box -f")
	w.p.Info("Статус:     sudo systemctl status enter-box")
	w.p.Info("Остановить: sudo systemctl stop enter-box")
}

// ---------------------------------------------------------------------------
// Вспомогательные
// ---------------------------------------------------------------------------

func guessWAN(ifaces []Iface, existing *config.Config) int {
	if existing != nil && existing.WAN.Name != "" {
		for idx, ifc := range ifaces {
			if ifc.Name == existing.WAN.Name {
				return idx
			}
		}
	}
	for idx, ifc := range ifaces {
		if ifc.IsDefault {
			return idx
		}
	}
	for idx, ifc := range ifaces {
		if ifc.Kind == "usb" {
			return idx
		}
	}
	return 0
}

func guessLAN(ifaces []Iface, existing *config.Config) int {
	if existing != nil && existing.LAN.Interface != "" {
		for idx, ifc := range ifaces {
			if ifc.Name == existing.LAN.Interface {
				return idx
			}
		}
	}
	for idx, ifc := range ifaces {
		if ifc.Kind == "pci" && !ifc.IsDefault {
			return idx
		}
	}
	return 0
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isSystemdPresent() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

func writeSystemdUnit(path, exe, configPath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=enter-box — VPN gateway (VLESS/VMess/AmneziaWG)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run -c %s
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`, exe, configPath)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(unit), 0o644)
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w (%s)", args, err, string(out))
	}
	return nil
}

func runSystemctlOrCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, string(out))
	}
	return nil
}
