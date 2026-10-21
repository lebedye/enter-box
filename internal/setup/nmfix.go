package setup

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"enter-box/internal/config"
)

const (
	// nmLegacyConfFile — файл старой схемы. Она делала LAN
	// unmanaged, из-за чего NetworkManager и enter-box спорили за IP.
	nmLegacyConfFile = "/etc/NetworkManager/conf.d/99-enter-box-unmanaged.conf"
	nmLANPrefix      = "enter-box-lan-"
)

// configureNetworkManagerLAN оставляет NetworkManager единственным
// владельцем постоянного LAN-адреса. enter-box после этого
// настраивает только DHCP, VPN, маршрутизацию и firewall.
//
// Если setup запущен по SSH через тот же LAN, уже назначенные
// IPv4-адреса сохраняются как дополнительные. Это не обрывает
// текущую сессию; старый адрес можно удалить после проверки.
func configureNetworkManagerLAN(cfg *config.Config) error {
	if serviceState("NetworkManager") != "active" {
		return nil
	}
	if _, err := exec.LookPath("nmcli"); err != nil {
		return fmt.Errorf("NetworkManager active, but nmcli not found")
	}
	// Удаляем только старый файл enter-box, чужие настройки NM
	// не трогаем.
	if err := os.Remove(nmLegacyConfFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove obsolete NetworkManager config: %w", err)
	}

	ifaces := append([]string{cfg.LAN.Interface}, cfg.LAN.ExtraInterfaces...)
	for i, iface := range ifaces {
		address := ""
		if i == 0 {
			address = cfg.LAN.Address
		}
		if err := configureNMInterface(iface, address); err != nil {
			return err
		}
	}
	return nil
}

func configureNMInterface(iface, wantedAddress string) error {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return nil
	}
	connection := activeNMConnection(iface)
	if connection == "" || connection == "--" {
		connection = nmLANPrefix + iface
		args := []string{"connection", "add", "type", "ethernet", "ifname", iface,
			"con-name", connection, "connection.autoconnect", "yes"}
		if out, err := exec.Command("nmcli", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("nmcli add LAN profile %s: %w (%s)", iface, err, strings.TrimSpace(string(out)))
		}
	}

	addresses := currentIPv4Addresses(iface)
	if wantedAddress != "" && !containsAddress(addresses, wantedAddress) {
		addresses = append([]string{wantedAddress}, addresses...)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("LAN interface %s has no IPv4 address", iface)
	}
	args := []string{"connection", "modify", connection,
		"connection.interface-name", iface,
		"connection.autoconnect", "yes",
		"ipv4.method", "manual",
		"ipv4.addresses", strings.Join(addresses, ","),
		"ipv4.gateway", "",
		"ipv4.never-default", "yes",
		"ipv4.ignore-auto-dns", "yes",
		"ipv6.method", "disabled"}
	if out, err := exec.Command("nmcli", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("nmcli modify LAN profile %s: %w (%s)", iface, err, strings.TrimSpace(string(out)))
	}

	// reapply меняет адреса без down/up и поэтому безопаснее
	// при удалённой настройке по SSH.
	if out, err := exec.Command("nmcli", "device", "reapply", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("nmcli reapply %s: %w (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func activeNMConnection(iface string) string {
	out, err := exec.Command("nmcli", "-g", "GENERAL.CONNECTION", "device", "show", iface).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func currentIPv4Addresses(iface string) []string {
	device, err := net.InterfaceByName(iface)
	if err != nil {
		return nil
	}
	addrs, err := device.Addrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		ip, network, err := net.ParseCIDR(addr.String())
		if err != nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		ones, _ := network.Mask.Size()
		out = append(out, fmt.Sprintf("%s/%d", ip.String(), ones))
	}
	return out
}

func containsAddress(addresses []string, wanted string) bool {
	wantedIP, _, err := net.ParseCIDR(wanted)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address)
		if err == nil && ip.Equal(wantedIP) {
			return true
		}
	}
	return false
}
