// Package wifi — управление WiFi-интерфейсом: on/off, connect, list.
//
// Работает в двух режимах:
//   - если NetworkManager активен → используем nmcli (проще, хранит пароли);
//   - если нет → работаем через wpa_cli (требует запущенный wpa_supplicant).
//
// ВАЖНО: enter-box setup маскирует NetworkManager. После этого
// `enter-box wifi connect` пойдёт через wpa_cli. Если wpa_supplicant
// не запущен — будет понятная ошибка с подсказкой.
package wifi

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RadioOn включает WiFi-радио (rfkill unblock + nmcli radio wifi on,
// если NetworkManager есть).
func RadioOn(iface string) error {
	_ = runCmd("rfkill", "unblock", "wifi")

	if hasNM() {
		return runCmd("nmcli", "radio", "wifi", "on")
	}
	// Без NM — просто поднять интерфейс.
	return runCmd("ip", "link", "set", iface, "up")
}

// RadioOff выключает WiFi-радио.
func RadioOff(iface string) error {
	if hasNM() {
		return runCmd("nmcli", "radio", "wifi", "off")
	}
	// Отключаем интерфейс и блокируем радио через rfkill.
	_ = runCmd("ip", "link", "set", iface, "down")
	return runCmd("rfkill", "block", "wifi")
}

// Connect подключается к сети. Если NetworkManager активен — через nmcli.
// Иначе — через wpa_cli (требует запущенный wpa_supplicant на этом интерфейсе).
func Connect(iface, ssid, password string) error {
	if hasNM() {
		return runCmd("nmcli", "device", "wifi", "connect", ssid,
			"password", password, "ifname", iface)
	}
	return connectWPA(iface, ssid, password)
}

// Scan сканирует сети. Возвращает человекочитаемый список (SSID, сигнал, защита).
func Scan(iface string) (string, error) {
	if hasNM() {
		out, err := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY",
			"device", "wifi", "list", "ifname", iface).Output()
		return string(out), err
	}
	// wpa_cli scan_results
	if err := runCmd("wpa_cli", "-i", iface, "scan"); err != nil {
		return "", fmt.Errorf("wpa_cli scan: %w", err)
	}
	time.Sleep(2 * time.Second) // даём время собрать результаты
	out, err := exec.Command("wpa_cli", "-i", iface, "scan_results").Output()
	return string(out), err
}

// hasNM сообщает, активен ли NetworkManager.
func hasNM() bool {
	out, _ := exec.Command("systemctl", "is-active", "NetworkManager.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}

// connectWPA работает через wpa_cli:
//
//	wpa_cli -i wlan0 add_network
//	wpa_cli -i wlan0 set_network 0 ssid "\"MySSID\""
//	wpa_cli -i wlan0 set_network 0 psk  "\"MyPassword\""
//	wpa_cli -i wlan0 enable_network 0
//	wpa_cli -i wlan0 save_config
func connectWPA(iface, ssid, password string) error {
	// Проверим, что wpa_supplicant отвечает.
	if err := runCmd("wpa_cli", "-i", iface, "ping"); err != nil {
		return fmt.Errorf("wpa_supplicant не отвечает на %s. "+
			"Запустите его: sudo systemctl enable --now wpa_supplicant@%s", iface, iface)
	}

	// 1. add_network → id
	id, err := wpaCmdOut(iface, "add_network")
	if err != nil {
		return fmt.Errorf("add_network: %w", err)
	}
	id = strings.TrimSpace(id)

	// 2. ssid
	if err := runCmd("wpa_cli", "-i", iface, "set_network", id, "ssid",
		fmt.Sprintf("%q", ssid)); err != nil {
		return fmt.Errorf("set ssid: %w", err)
	}
	// 3. psk
	if err := runCmd("wpa_cli", "-i", iface, "set_network", id, "psk",
		fmt.Sprintf("%q", password)); err != nil {
		return fmt.Errorf("set psk: %w", err)
	}
	// 4. enable_network
	if err := runCmd("wpa_cli", "-i", iface, "enable_network", id); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	// 5. save
	if err := runCmd("wpa_cli", "-i", iface, "save_config"); err != nil {
		return fmt.Errorf("save_config: %w", err)
	}
	return nil
}

// wpaCmdOut выполняет wpa_cli и возвращает stdout (например, id сети).
func wpaCmdOut(iface, cmd string, args ...string) (string, error) {
	fullArgs := append([]string{"-i", iface, cmd}, args...)
	out, err := exec.Command("wpa_cli", fullArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("wpa_cli %s: %w", cmd, err)
	}
	// wpa_cli печатает заголовок и значение; берём последнюю непустую строку.
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	var last string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			last = line
		}
	}
	return last, nil
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, string(out))
	}
	return nil
}
