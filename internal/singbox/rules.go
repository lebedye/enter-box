package singbox

import (
	"enter-box/internal/config"
)

func buildRules(cfg *config.Config) []map[string]any {
	var rules []map[string]any
	// Имя домена берётся из DNS и TLS/HTTP, иначе правило .ru получает
	// только IP клиента. DNS клиентов обслуживает встроенный resolver sing-box.
	rules = append(rules,
		map[string]any{"type": "default", "action": "sniff"},
		map[string]any{"type": "default", "protocol": "dns", "action": "hijack-dns"},
	)

	rules = append(rules, map[string]any{
		"type": "default",
		"ip_cidr": []string{
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"169.254.0.0/16", "224.0.0.0/4", "127.0.0.0/8",
			"fc00::/7", "fe80::/10",
		},
		"outbound": "direct",
	})

	if cfg.Routing.UseRUDirect {
		// Это локальное правило не зависит от загрузки внешних geosite-файлов.
		// xn--p1ai — DNS-представление кириллической зоны .рф.
		rules = append(rules, map[string]any{
			"type":          "default",
			"domain_suffix": []string{"ru", "рф", "xn--p1ai"},
			"outbound":      "direct",
		})
	}

	if len(cfg.Routing.ForceProxy) > 0 {
		rules = append(rules, map[string]any{
			"type":     "default",
			"domain":   cfg.Routing.ForceProxy,
			"outbound": "proxy",
		})
	}

	manual := map[string]any{"type": "default", "outbound": "direct"}
	if len(cfg.Routing.DomesticDomains) > 0 {
		manual["domain"] = cfg.Routing.DomesticDomains
	}
	if len(cfg.Routing.DomesticSuffix) > 0 {
		manual["domain_suffix"] = cfg.Routing.DomesticSuffix
	}
	if len(cfg.Routing.DomesticIPCIDR) > 0 {
		manual["ip_cidr"] = cfg.Routing.DomesticIPCIDR
	}
	// type и outbound не являются условиями: пустое правило недопустимо.
	if len(manual) > 2 {
		rules = append(rules, manual)
	}

	return rules
}
