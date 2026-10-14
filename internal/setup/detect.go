package setup

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Iface struct {
	Name        string
	MAC         string
	UP          bool
	Addrs       []string
	Kind        string
	IsDefault   bool
	HasIP       bool
	Description string
}

func DetectInterfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	defaultIface := defaultRouteIface()

	var out []Iface
	for _, i := range ifaces {
		if shouldSkip(i.Name) {
			continue
		}
		it := Iface{
			Name: i.Name,
			MAC:  i.HardwareAddr.String(),
			UP:   i.Flags&net.FlagUp != 0,
			Kind: detectKind(i.Name),
		}
		if it.MAC == "" {
			it.MAC = "—"
		}
		for _, a := range mustAddrs(i) {
			it.Addrs = append(it.Addrs, a)
			it.HasIP = true
		}
		it.IsDefault = i.Name == defaultIface
		it.Description = formatIface(it)
		out = append(out, it)
	}
	return out, nil
}

func mustAddrs(i net.Interface) []string {
	addrs, _ := i.Addrs()
	var s []string
	for _, a := range addrs {
		s = append(s, a.String())
	}
	return s
}

func shouldSkip(name string) bool {
	switch {
	case name == "lo":
		return true
	case strings.HasPrefix(name, "docker"),
		strings.HasPrefix(name, "veth"),
		strings.HasPrefix(name, "br-"),
		strings.HasPrefix(name, "virbr"),
		strings.HasPrefix(name, "tun"),
		strings.HasPrefix(name, "tap"),
		strings.HasPrefix(name, "awg-"),
		strings.HasPrefix(name, "wg"):
		return true
	}
	return false
}

func detectKind(name string) string {
	base := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(filepath.Join(base, "wireless")); err == nil {
		return "wireless"
	}
	link, err := os.Readlink(filepath.Join(base, "device", "subsystem"))
	if err != nil {
		return "virtual"
	}
	switch {
	case strings.Contains(link, "usb"):
		return "usb"
	case strings.Contains(link, "pci"):
		return "pci"
	}
	return "virtual"
}

func defaultRouteIface() string {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func formatIface(i Iface) string {
	var tags []string
	switch i.Kind {
	case "usb":
		tags = append(tags, "USB")
	case "pci":
		tags = append(tags, "PCI")
	case "wireless":
		tags = append(tags, "WiFi")
	case "virtual":
		tags = append(tags, "virtual")
	}
	if i.UP {
		tags = append(tags, "UP")
	} else {
		tags = append(tags, "down")
	}
	if i.IsDefault {
		tags = append(tags, "default-route")
	}
	addr := "—"
	if len(i.Addrs) > 0 {
		addr = strings.Join(i.Addrs, ",")
	}
	return "[" + strings.Join(tags, ",") + "] mac=" + i.MAC + " addr=" + addr
}

// interfaceIP возвращает первый IPv4-адрес интерфейса без маски.
func interfaceIP(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, _ := iface.Addrs()
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipnet.IP.To4(); ip != nil {
			return ip.String()
		}
	}
	return ""
}
