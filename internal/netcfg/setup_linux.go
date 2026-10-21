package netcfg

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"

	"enter-box/internal/config"
)

// Setup выполняет разовую настройку хоста при старте демона.
type Setup struct {
	logger *slog.Logger
	cfg    *config.Config
}

func New(logger *slog.Logger, cfg *config.Config) (*Setup, error) {
	return &Setup{logger: logger, cfg: cfg}, nil
}

func (s *Setup) Apply(ctx context.Context) error {
	// 0a. Освобождаем порт 53 для встроенного DNS-сервера enter-box.
	//     Без этого systemd-resolved займёт 0.0.0.0:53 и DNS не поднимется.
	if err := DisableDNSStubListener(); err != nil {
		s.logger.Warn("dnsfix: не удалось освободить порт 53", "err", err)
		// Не прерываем: возможно, systemd-resolved уже не мешает.
	} else {
		s.logger.Info("dnsfix: порт 53 освобождён (DNSStubListener=no)")
	}

	// 0b. При активном NetworkManager постоянным адресом владеет его профиль,
	//     созданный setup. Второй systemd-юнит здесь вызвал бы гонку за адрес.
	if networkManagerActive() {
		disableLegacyLanAddrUnit()
		s.logger.Info("LAN address is managed by NetworkManager")
	} else if err := writeLanAddrUnit(s.cfg); err != nil {
		s.logger.Warn("lan-addr unit", "err", err)
	}

	// 1. sysctl
	if err := applySysctl(ctx); err != nil {
		return err
	}

	// 2. IP на LAN-интерфейс.
	if err := runCmd(ctx, "ip", "addr", "replace", s.cfg.LAN.Address, "dev", s.cfg.LAN.Interface); err != nil {
		return fmt.Errorf("set LAN addr: %w", err)
	}
	if err := runCmd(ctx, "ip", "link", "set", s.cfg.LAN.Interface, "up"); err != nil {
		return fmt.Errorf("LAN up: %w", err)
	}

	// 3. Extra LAN-интерфейсы: поднимаем. IP на них пользователь настраивает
	//    сам (например, тот же /24 — тогда они работают как единая шина).
	for _, name := range s.cfg.LAN.ExtraInterfaces {
		if err := runCmd(ctx, "ip", "link", "set", name, "up"); err != nil {
			s.logger.Warn("extra LAN up", "iface", name, "err", err)
		}
	}

	// 4. WAN up.
	if err := runCmd(ctx, "ip", "link", "set", s.cfg.WAN.Name, "up"); err != nil {
		return fmt.Errorf("WAN up: %w", err)
	}

	// 5. nftables для всех LAN.
	lanCIDR := networkOf(s.cfg.LAN.Address)
	lans := append([]string{s.cfg.LAN.Interface}, s.cfg.LAN.ExtraInterfaces...)
	if err := applyNftScriptMulti(ctx, nftTable, lanCIDR,
		s.cfg.WAN.Name, s.cfg.TUN.Interface, lans); err != nil {
		return err
	}
	return nil
}

func networkManagerActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "NetworkManager").Run() == nil
}

func disableLegacyLanAddrUnit() {
	// Убираем только ранее созданный enter-box unit; чужие сетевые службы
	// и профили NetworkManager не затрагиваются.
	_ = exec.Command("systemctl", "disable", "--now", "enter-box-lan-addr.service").Run()
	_ = os.Remove("/etc/systemd/system/enter-box-lan-addr.service")
	_ = os.Remove("/usr/local/bin/enter-box-lan-addr")
	_ = exec.Command("systemctl", "daemon-reload").Run()
}

func (s *Setup) Cleanup() error {
	ctx := context.Background()
	_ = cleanupNftables(ctx)
	return nil
}

func writeLanAddrUnit(cfg *config.Config) error {
	const svcPath = "/etc/systemd/system/enter-box-lan-addr.service"
	const shPath = "/usr/local/bin/enter-box-lan-addr"

	script := fmt.Sprintf(`#!/bin/bash
set -e
ip link set %s up || true
ip addr replace %s dev %s
`, cfg.LAN.Interface, cfg.LAN.Address, cfg.LAN.Interface)

	if err := os.WriteFile(shPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", shPath, err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=enter-box: static LAN IP on %s
Before=enter-box.service network-online.target
After=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s

[Install]
WantedBy=multi-user.target
`, cfg.LAN.Interface, shPath)

	if err := os.WriteFile(svcPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", svcPath, err)
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", "enter-box-lan-addr.service").Run()
	return nil
}

func networkOf(addr string) string {
	_, n, err := net.ParseCIDR(addr)
	if err != nil {
		return addr
	}
	return n.String()
}
