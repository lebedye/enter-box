package singbox

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sagernet/sing-box/option"

	"enter-box/internal/config"
	"enter-box/internal/parser"
)

func Generate(cfg *config.Config, pool *parser.Pool) (*option.Options, error) {
	outbounds, err := buildOutbounds(cfg, pool)
	if err != nil {
		return nil, err
	}

	root := map[string]any{
		"log": map[string]any{
			"level":     cfg.Log.Level,
			"timestamp": true,
		},
		"dns":          buildDNS(cfg),
		"inbounds":     buildInbounds(cfg),
		"outbounds":    outbounds,
		"route":        buildRoute(cfg),
		"experimental": buildExperimental(),
	}

	raw, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	var opts option.Options
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, fmt.Errorf("unmarshal into sing-box options: %w (json=%s)", err, raw)
	}
	return &opts, nil
}

func DumpJSON(w io.Writer, opts *option.Options) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(opts)
}

func buildDNS(cfg *config.Config) map[string]any {
	servers := make([]map[string]any, 0, len(cfg.DNS.Remote)+len(cfg.DNS.Domestic))
	firstRemote := ""
	firstDomestic := ""

	for i, s := range cfg.DNS.Remote {
		tag := fmt.Sprintf("remote%d", i)
		if firstRemote == "" {
			firstRemote = tag
		}
		servers = append(servers, map[string]any{
			"type":   "udp",
			"tag":    tag,
			"server": s,
		})
	}
	for i, s := range cfg.DNS.Domestic {
		tag := fmt.Sprintf("domestic%d", i)
		if firstDomestic == "" {
			firstDomestic = tag
		}
		servers = append(servers, map[string]any{
			"type":   "udp",
			"tag":    tag,
			"server": s,
		})
	}

	dns := map[string]any{
		"servers": servers,
		"final":   firstRemote,
	}
	if firstDomestic != "" {
		dns["rules"] = []map[string]any{
			{
				"rule_set": []string{"geosite-ru"},
				"server":   firstDomestic,
			},
		}
	}
	return dns
}

func buildInbounds(cfg *config.Config) []map[string]any {
	return []map[string]any{
		{
			"type":                     "tun",
			"tag":                      "tun-in",
			"interface_name":           cfg.TUN.Interface,
			"address":                  []string{cfg.TUN.Address},
			"mtu":                      cfg.TUN.MTU,
			"stack":                    cfg.TUN.Stack,
			"auto_route":               true,
			"strict_route":             false,
			"auto_redirect":            false,
			"endpoint_independent_nat": true,
		},
	}
}

func buildOutbounds(cfg *config.Config, pool *parser.Pool) ([]map[string]any, error) {
	outs := []map[string]any{
		{"type": "direct", "tag": "direct"},
		{"type": "block", "tag": "block"},
	}

	proxyTags := make([]string, 0, pool.Len())
	for _, ep := range pool.All() {
		o, err := buildOutbound(ep)
		if err != nil {
			return nil, fmt.Errorf("outbound %s: %w", ep.Tag, err)
		}
		outs = append(outs, o)
		proxyTags = append(proxyTags, ep.Tag)
	}
	if len(proxyTags) == 0 {
		return nil, fmt.Errorf("no proxy endpoints loaded")
	}

	interval := cfg.Routing.URLTestInterval
	if interval <= 0 {
		interval = 3 * time.Minute
	}
	tolerance := cfg.Routing.URLTestTolerance
	if tolerance <= 0 {
		tolerance = 50
	}
	testURL := cfg.Routing.URLTestURL
	if testURL == "" {
		testURL = "http://cp.cloudflare.com/generate_204"
	}

	outs = append(outs, map[string]any{
		"type":                        "urltest",
		"tag":                         "proxy-auto",
		"outbounds":                   proxyTags,
		"url":                         testURL,
		"interval":                    interval.String(),
		"tolerance":                   tolerance,
		"idle_timeout":                "30m",
		"interrupt_exist_connections": false,
	})

	selectorMembers := append([]string{"proxy-auto"}, proxyTags...)
	defaultTag := "proxy-auto"
	if cfg.Routing.Preferred != "" {
		for _, t := range proxyTags {
			if t == cfg.Routing.Preferred {
				defaultTag = cfg.Routing.Preferred
				break
			}
		}
	}

	outs = append(outs, map[string]any{
		"type":      "selector",
		"tag":       "proxy",
		"outbounds": selectorMembers,
		"default":   defaultTag,
	})

	return outs, nil
}

func buildRoute(cfg *config.Config) map[string]any {
	r := map[string]any{
		"rules":                 buildRules(cfg),
		"final":                 cfg.Routing.Final,
		"auto_detect_interface": true,
	}
	if cfg.Routing.UseRUDirect {
		r["rule_set"] = ruleSetsRU()
	}
	return r
}

func buildExperimental() map[string]any {
	return map[string]any{
		"clash_api": map[string]any{
			"external_controller": "127.0.0.1:9090",
			"default_mode":        "Rule",
		},
	}
}
