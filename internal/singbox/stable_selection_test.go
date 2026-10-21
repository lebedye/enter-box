package singbox

import (
	"testing"

	"enter-box/internal/config"
	"enter-box/internal/parser"
)

// buildTestPool собирает пул из двух синтетических VLESS-узлов.
// Используются только существующие конструкторы parser.NewPool/Add и
// parser.Endpoint с KindVLESS — никаких новых полей конфига.
func buildTestPool(t *testing.T) *parser.Pool {
	t.Helper()
	pool := parser.NewPool()
	pool.Add(&parser.Endpoint{
		Tag:  "node1",
		Kind: parser.KindVLESS,
		Server: "one.example.com", Port: 443, UUID: "test-one",
	})
	pool.Add(&parser.Endpoint{
		Tag:  "node2",
		Kind: parser.KindVLESS,
		Server: "two.example.com", Port: 443, UUID: "test-two",
	})
	return pool
}

// TestBuildOutboundsStableSelection проверяет, что автоматический выбор
// реализован через selector, а не через urltest.
//
// urltest недопустим: он переключает трафик на узел с меньшей задержкой,
// даже если текущий узел полностью здоров. Это приводит к «дрожанию»
// выбора новых соединений без реальной необходимости.
// Стабильная автоматика должна менять узел только при отказе, поэтому
// proxy-auto обязан быть selector'ом, включающим оба узла и direct.
func TestBuildOutboundsStableSelection(t *testing.T) {
	cfg := &config.Config{}
	pool := buildTestPool(t)

	outs, err := buildOutbounds(cfg, pool)
	if err != nil {
		t.Fatalf("buildOutbounds: %v", err)
	}

	var auto map[string]any
	var proxy map[string]any
	for _, o := range outs {
		switch o["tag"] {
		case "proxy-auto":
			auto = o
		case "proxy":
			proxy = o
		}
	}

	if auto == nil {
		t.Fatal("outbound proxy-auto не найден")
	}
	if auto["type"] != "selector" {
		t.Fatalf("proxy-auto должен быть selector, получено %v", auto["type"])
	}
	if auto["type"] == "urltest" {
		t.Fatal("proxy-auto не должен быть urltest: здоровый узел нельзя менять из-за меньшей задержки")
	}

	members, ok := auto["outbounds"].([]string)
	if !ok {
		t.Fatalf("proxy-auto.outbounds имеет неожиданный тип %T", auto["outbounds"])
	}
	has := func(tag string) bool {
		for _, m := range members {
			if m == tag {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"node1", "node2", "direct"} {
		if !has(want) {
			t.Fatalf("proxy-auto.outbounds не содержит %q: %v", want, members)
		}
	}

	if proxy == nil {
		t.Fatal("outbound proxy не найден")
	}
	if proxy["default"] != "proxy-auto" {
		t.Fatalf("proxy.default должен быть proxy-auto, получено %v", proxy["default"])
	}
}

// TestBuildExperimentalCacheFile проверяет, что experimental включает
// cache_file с непустым путём. Без кэша выбор узла теряется при рестарте
// демона, и автоматика начинает с произвольного узла.
func TestBuildExperimentalCacheFile(t *testing.T) {
	exp := buildExperimental()

	cache, ok := exp["cache_file"].(map[string]any)
	if !ok {
		t.Fatalf("experimental.cache_file отсутствует или имеет неверный тип: %T", exp["cache_file"])
	}
	if cache["enabled"] != true {
		t.Fatalf("cache_file.enabled должен быть true, получено %v", cache["enabled"])
	}
	path, _ := cache["path"].(string)
	if path == "" {
		t.Fatal("cache_file.path должен быть непустым, чтобы сохранять выбор после рестарта")
	}
}

// Домен VPN-сервера нельзя разрешать через VPN, который ещё не поднят.
// Для девяти доменных серверов подписки нужен отдельный прямой bootstrap DNS.
func TestServerDomainsUseDirectBootstrapDNS(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.Domestic = []string{"77.88.8.8"}
	route := buildRoute(cfg)
	resolver, ok := route["default_domain_resolver"].(map[string]any)
	if !ok || resolver["server"] != "domestic0" {
		t.Fatalf("адрес VPN-сервера должен разрешаться напрямую, получено %v", route["default_domain_resolver"])
	}
}
