package netcfg

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// IfaceMonitor периодически проверяет, что LAN-интерфейсы и Wi-Fi (в режиме AP)
// находятся в состоянии UP. Если интерфейс упал — поднимает его.
//
// Зачем: Wi-Fi-чипы на Orange Pi (особенно встроенные) при перегреве или
// после долгого uptime сбрасывают режим точки доступа. Без монитора
// enter-box продолжает считать, что сеть работает, а клиенты теряют связь.
//
// Монитор не пересоздаёт интерфейс заново и не перезапускает hostapd —
// это задача systemd-юнита hostapd. Наша цель — вернуть link в UP.
type IfaceMonitor struct {
	logger   *slog.Logger
	ifaces   []string
	interval time.Duration
}

func NewIfaceMonitor(logger *slog.Logger, ifaces []string, interval time.Duration) *IfaceMonitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &IfaceMonitor{
		logger:   logger,
		ifaces:   ifaces,
		interval: interval,
	}
}

// Run блокируется до отмены ctx.
func (m *IfaceMonitor) Run(ctx context.Context) {
	m.logger.Info("ifmon: запущен монитор LAN-интерфейсов",
		"ifaces", m.ifaces, "interval", m.interval)

	t := time.NewTicker(m.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("ifmon: остановлен")
			return
		case <-t.C:
			for _, name := range m.ifaces {
				m.checkAndHeal(ctx, name)
			}
		}
	}
}

func (m *IfaceMonitor) checkAndHeal(ctx context.Context, name string) {
	out, err := exec.CommandContext(ctx, "ip", "link", "show", name).Output()
	if err != nil {
		m.logger.Warn("ifmon: интерфейс не найден", "iface", name, "err", err)
		return
	}
	header := firstLine(string(out))
	// Формат: "3: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> ..."
	if strings.Contains(header, "UP") {
		return
	}

	m.logger.Warn("ifmon: интерфейс опущен, поднимаю", "iface", name)
	if err := exec.CommandContext(ctx, "ip", "link", "set", name, "up").Run(); err != nil {
		m.logger.Error("ifmon: не удалось поднять", "iface", name, "err", err)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}