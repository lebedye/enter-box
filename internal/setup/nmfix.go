package setup

import (
	"fmt"
	"os"
	"strings"
)

const (
	nmConfDir  = "/etc/NetworkManager/conf.d"
	nmConfFile = nmConfDir + "/99-enter-box-unmanaged.conf"
)

// configureNetworkManagerUnmanaged записывает drop-in конфиг NetworkManager,
// переводящий указанные интерфейсы в статус unmanaged.
//
// Зачем: enter-box полностью управляет LAN-интерфейсами (вешает IP,
// маршрутизирует, поднимает DHCP-сервер). Если NetworkManager тоже будет
// ими управлять — он сбросит IP и сломает маршрутизацию.
//
// При этом сам NetworkManager остаётся активным:
//   * WAN — NM получает IP по DHCP от провайдера;
//   * Wi-Fi в режиме клиента — NM управляет подключением через nmcli.
//
// Функция идемпотентна — повторный вызов перезаписывает конфиг.
func configureNetworkManagerUnmanaged(ifaces []string) error {
	// Чистим от пустых и дубликатов.
	uniq := make([]string, 0, len(ifaces))
	seen := make(map[string]struct{}, len(ifaces))
	for _, n := range ifaces {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		uniq = append(uniq, n)
	}
	if len(uniq) == 0 {
		return nil
	}

	if err := os.MkdirAll(nmConfDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nmConfDir, err)
	}

	// Формат NM: unmanaged-devices=interface-name:eth0;interface-name:eth1
	var expr strings.Builder
	for i, n := range uniq {
		if i > 0 {
			expr.WriteByte(';')
		}
		expr.WriteString("interface-name:")
		expr.WriteString(n)
	}

	content := fmt.Sprintf(`# Автоматически создано enter-box (setup wizard).
#
# NetworkManager не управляет этими интерфейсами — их полностью
# контролирует enter-box (IP, DHCP, nftables).
#
# ВАЖНО: не добавляйте сюда WAN-интерфейс, если провайдер выдаёт
# IP по DHCP — иначе адрес не будет получен.
# Не добавляйте сюда Wi-Fi в режиме клиента — им должен управлять NM.

[keyfile]
unmanaged-devices=%s
`, expr.String())

	if err := os.WriteFile(nmConfFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", nmConfFile, err)
	}
	return nil
}

// reloadNetworkManager перезапускает NetworkManager, чтобы он перечитал
// conf.d. Если NM не активен — ничего не делает.
func reloadNetworkManager() error {
	if serviceState("NetworkManager") != "active" {
		return nil
	}
	return runSystemctl("restart", "NetworkManager")
}