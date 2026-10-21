package singbox

import (
	"testing"

	"enter-box/internal/parser"
)

// mustParse — вспомогательная функция: парсит URI и падает при ошибке.
func mustParse(t *testing.T, raw string) *parser.Endpoint {
	t.Helper()
	ep, err := parser.ParseLink(raw)
	if err != nil {
		t.Fatalf("ParseLink(%q): %v", raw, err)
	}
	return ep
}

// toStringSlice — приводит значение из map к []string (поддерживает []string и []any).
func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func TestBuildOutbound_Hysteria2(t *testing.T) {
	// В конфигурации sing-box имя SNI хранится в TLS-поле server_name.
	ep := mustParse(t, "hysteria2://p%40ss@example.com:443?sni=example.com#hy2")
	o, err := buildOutbound(ep)
	if err != nil {
		t.Fatalf("buildOutbound: %v", err)
	}
	if o["type"] != "hysteria2" {
		t.Fatalf("type: ожидали hysteria2, получили %v", o["type"])
	}
	if o["password"] != "p@ss" {
		t.Fatalf("password: ожидали p@ss, получили %v", o["password"])
	}
	tls, ok := o["tls"].(map[string]any)
	if !ok {
		t.Fatalf("ожидали tls-блок, получили %v", o["tls"])
	}
	if tls["server_name"] != "example.com" {
		t.Fatalf("tls.server_name: ожидали example.com, получили %v", tls["server_name"])
	}
}

func TestBuildOutbound_VLESSRealityDefaultUTLS(t *testing.T) {
	// Reality без явного fp должен получить utls с fingerprint=chrome по умолчанию.
	ep := mustParse(t, "vless://fakeUUID@example.com:443?security=reality&sni=example.com&pbk=pubkey&sid=abcd#r")
	o, err := buildOutbound(ep)
	if err != nil {
		t.Fatalf("buildOutbound: %v", err)
	}
	tls, ok := o["tls"].(map[string]any)
	if !ok {
		t.Fatalf("ожидали tls-блок, получили %v", o["tls"])
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok {
		t.Fatalf("reality без fp должен включать utls, получили %v", tls["utls"])
	}
	if utls["fingerprint"] != "chrome" {
		t.Fatalf("utls.fingerprint: ожидали chrome по умолчанию, получили %v", utls["fingerprint"])
	}
}

func TestBuildOutbound_VLESSALPN(t *testing.T) {
	// alpn из URI должен попасть в tls.alpn.
	ep := mustParse(t, "vless://fakeUUID@example.com:443?security=tls&sni=example.com&alpn=h2,http/1.1#a")
	o, err := buildOutbound(ep)
	if err != nil {
		t.Fatalf("buildOutbound: %v", err)
	}
	tls, ok := o["tls"].(map[string]any)
	if !ok {
		t.Fatalf("ожидали tls-блок, получили %v", o["tls"])
	}
	alpn := toStringSlice(tls["alpn"])
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("tls.alpn: ожидали [h2 http/1.1], получили %v", tls["alpn"])
	}
}
