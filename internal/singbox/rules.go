package singbox

import (
	"enter-box/internal/config"
)

func buildRules(cfg *config.Config) []map[string]any {
	var rules []map[string]any

	rules = append(rules, map[string]any{
		"ip_cidr": []string{
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"169.254.0.0/16", "224.0.0.0/4", "127.0.0.0/8",
			"fc00::/7", "fe80::/10",
		},
		"outbound": "direct",
	})

	if len(cfg.Routing.ForceProxy) > 0 {
		rules = append(rules, map[string]any{
			"domain":   cfg.Routing.ForceProxy,
			"outbound": "proxy",
		})
	}

	if cfg.Routing.UseRUDirect {
		rules = append(rules,
			map[string]any{
				"rule_set": []string{"geosite-ru"},
				"outbound": "direct",
			},
			map[string]any{
				"rule_set": []string{"geoip-ru"},
				"outbound": "direct",
			},
		)
	}

	manual := map[string]any{"outbound": "direct"}
	if len(cfg.Routing.DomesticDomains) > 0 {
		manual["domain"] = cfg.Routing.DomesticDomains
	}
	if len(cfg.Routing.DomesticSuffix) > 0 {
		manual["domain_suffix"] = cfg.Routing.DomesticSuffix
	}
	if len(cfg.Routing.DomesticIPCIDR) > 0 {
		manual["ip_cidr"] = cfg.Routing.DomesticIPCIDR
	}
	if len(manual) > 1 {
		rules = append(rules, manual)
	}

	return rules
}
