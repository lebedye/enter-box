package setup

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Check struct {
	Name     string
	OK       bool
	Critical bool
	Message  string
	Fix      string
}

func Preflight() []Check {
	var checks []Check

	checks = append(checks, Check{
		Name: "root", OK: os.Geteuid() == 0, Critical: true,
		Message: "запуск от root", Fix: "запустите через sudo",
	})
	checks = append(checks, Check{
		Name: "os", OK: runtime.GOOS == "linux", Critical: true,
		Message: "ОС Linux", Fix: "enter-box работает только на Linux (Orange Pi / ARM)",
	})

	if runtime.GOOS != "linux" {
		return checks
	}

	tunOK := false
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		tunOK = true
	}
	checks = append(checks, Check{
		Name: "tun", OK: tunOK, Critical: true,
		Message: "/dev/net/tun присутствует",
		Fix:     "sudo modprobe tun && echo tun | sudo tee /etc/modules-load.d/tun.conf",
	})

	_, err := exec.LookPath("nft")
	checks = append(checks, Check{
		Name: "nft", OK: err == nil, Critical: true,
		Message: "nftables установлен", Fix: "sudo apt install nftables",
	})

	_, err = exec.LookPath("ip")
	checks = append(checks, Check{
		Name: "iproute2", OK: err == nil, Critical: true,
		Message: "iproute2 установлен", Fix: "sudo apt install iproute2",
	})

	// SSH-сервер (не критично, wizard предложит поставить).
	sshOK := sshActive()
	checks = append(checks, Check{
		Name: "ssh", OK: sshOK, Critical: false,
		Message: "SSH-сервер активен",
		Fix:     "sudo apt install -y openssh-server && sudo systemctl enable --now ssh",
	})

	// Конфликты.
	//
	// NetworkManager хранит постоянный LAN-профиль и продолжает обслуживать
	// WAN/Wi-Fi; enter-box отвечает за DHCP/VPN, без гонки за LAN-адрес.
	// Поэтому помечаем его как информацию, а не как проблему.
	//
	// systemd-networkd и dhcpcd — реальные конфликты: wizard их отключит.
	if serviceState("NetworkManager") == "active" {
		checks = append(checks, Check{
			Name: "info:NetworkManager", OK: true, Critical: false,
			Message: "NetworkManager активен: LAN будет настроен постоянным manual-профилем, WAN и Wi-Fi остаются под NM",
		})
	}

	for _, svc := range []string{"systemd-networkd", "dhcpcd"} {
		state := serviceState(svc)
		if state == "active" {
			checks = append(checks, Check{
				Name: "conflict:" + svc, OK: false, Critical: false,
				Message: svc + " активен и будет отключён wizard'ом",
				Fix:     fmt.Sprintf("sudo systemctl stop %s && sudo systemctl disable %s && sudo systemctl mask %s", svc, svc, svc),
			})
		}
	}

	return checks
}

func serviceState(name string) string {
	out, _ := exec.Command("systemctl", "is-active", name+".service").Output()
	return strings.TrimSpace(string(out))
}

func sshActive() bool {
	for _, name := range []string{"ssh", "sshd"} {
		if serviceState(name) == "active" {
			return true
		}
	}
	return false
}

func PrintChecks(w *Prompter, checks []Check) int {
	w.Title("Проверка окружения")
	criticalFails := 0
	for _, c := range checks {
		if c.OK {
			w.OK("%s", c.Message)
			continue
		}
		if c.Critical {
			criticalFails++
		}
		w.Warn("%s", c.Message)
		if c.Fix != "" {
			w.Info("   → %s", c.Fix)
		}
	}
	if criticalFails > 0 {
		w.Warn("Есть %d критичных проблем — исправьте и запустите снова", criticalFails)
	}
	return criticalFails
}
