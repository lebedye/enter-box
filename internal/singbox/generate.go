package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"

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
		"inbounds":     buildInbounds(cfg, pool),
		"outbounds":    outbounds,
		"route":        buildRoute(cfg),
		"experimental": buildExperimental(),
	}

	raw, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	var opts option.Options
	// sing-box использует UnmarshalJSONContext для выбора конкретного
	// протокола. encoding/json молча оставляет его Options пустым.
	if err := singjson.UnmarshalContext(include.Context(context.Background()), raw, &opts); err != nil {
		return nil, fmt.Errorf("unmarshal into sing-box options: %w", err)
	}
	return &opts, nil
}

func DumpJSON(w io.Writer, opts *option.Options) error {
	// Для дампа требуется тот же context-aware codec, иначе теряются поля.
	enc := singjson.NewEncoderContext(include.Context(context.Background()), w)
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
			// Зарубежный DNS следует за selector: VPN либо аварийный direct.
			"detour": "proxy",
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
			// В sing-box 1.14 пустой direct нельзя использовать как DNS detour.
			// Привязка к физическому WAN даёт тот же прямой маршрут.
			"bind_interface": cfg.WAN.Name,
		})
	}

	dns := map[string]any{
		"servers":         servers,
		"final":           firstRemote,
		"reverse_mapping": true,
	}
	if firstDomestic != "" {
		dns["rules"] = []map[string]any{
			{
				"type": "default",
				// Локальный suffix не требует загрузки geosite с GitHub.
				"domain_suffix": []string{"ru", "xn--p1ai"},
				"server":        firstDomestic,
			},
		}
	}
	return dns
}

func buildInbounds(cfg *config.Config, pool *parser.Pool) []map[string]any {
	exclusions := localRouteExclusions(cfg)
	for _, endpoint := range pool.All() {
		// Внешний UDP AmneziaWG не должен повторно попадать в sing-box:
		// auto_route использует отдельную таблицу, поэтому /32 в main недостаточно.
		if ip := net.ParseIP(endpoint.Server); ip != nil && ip.To4() != nil {
			exclusions = append(exclusions, ip.String()+"/32")
		}
	}
	return []map[string]any{
		{
			"type":           "tun",
			"tag":            "tun-in",
			"interface_name": cfg.TUN.Interface,
			"address":        []string{cfg.TUN.Address},
			"mtu":            cfg.TUN.MTU,
			"stack":          cfg.TUN.Stack,
			"auto_route":     true,
			"strict_route":   false,
			// auto_redirect перехватывает именно транзитный трафик клиентов LAN.
			// Без него auto_route надёжно обслуживает только сам маршрутизатор.
			"auto_redirect": true,
			// Сетевое управление и локальные подсети не направляются в TUN.
			"route_exclude_address": exclusions,
		},
	}
}

func localRouteExclusions(cfg *config.Config) []string {
	// Сохраняем доступ к LAN, модему и уже существующей подсети SSH.
	// На другой плате адреса определяются заново, адресов текущей платы здесь нет.
	prefixes := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	seen := map[string]bool{}
	for _, name := range []string{cfg.LAN.Interface, cfg.WAN.Name} {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !seen[network.String()] {
				prefixes = append(prefixes, network.String())
				seen[network.String()] = true
			}
		}
	}
	return prefixes
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

	// Автоматика — selector, а не urltest: здоровый узел нельзя менять
	// из-за меньшей задержки. Переключение делает failover через SwitchGroup.
	autoMembers := append([]string{}, proxyTags...)
	autoMembers = append(autoMembers, "direct")

	outs = append(outs, map[string]any{
		"type":      "selector",
		"tag":       "proxy-auto",
		"outbounds": autoMembers,
		"default":   proxyTags[0],
	})

	// direct используется автоматикой только как аварийный канал.
	// По умолчанию selector остаётся на VPN-группе proxy-auto.
	selectorMembers := append([]string{"proxy-auto"}, proxyTags...)
	selectorMembers = append(selectorMembers, "direct")
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
		"rules": buildRules(cfg),
		"final": cfg.Routing.Final,
		// Явный WAN не позволяет AWG/TUN стать интерфейсом для direct.
		"default_interface": cfg.WAN.Name,
	}
	// Имена VPN-серверов разрешаем через WAN, даже когда сам VPN недоступен.
	// Иначе запуск туннеля зависел бы от DNS внутри ещё не поднятого туннеля.
	if len(cfg.DNS.Domestic) > 0 {
		r["default_domain_resolver"] = map[string]any{"server": "domestic0"}
	}
	return r
}

func buildExperimental() map[string]any {
	return map[string]any{
		"clash_api": map[string]any{
			"external_controller": "127.0.0.1:9090",
			"default_mode":        "Rule",
		},
		// Кэш сохраняет выбор обоих селекторов после рестарта демона.
		// Каталог создаёт systemd через StateDirectory.
		"cache_file": map[string]any{
			"enabled": true,
			"path":    "/var/lib/enter-box/selector.db",
		},
	}
}
