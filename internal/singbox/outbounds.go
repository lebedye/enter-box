// Package singbox генерирует JSON-совместимый конфиг sing-box.
package singbox

import (
	"fmt"
	"strings"

	"enter-box/internal/parser"
)

func buildOutbound(ep *parser.Endpoint) (map[string]any, error) {
	switch ep.Kind {
	case parser.KindVLESS:
		return buildVLESS(ep)
	case parser.KindVMess:
		return buildVMess(ep)
	case parser.KindAmnezia:
		return buildAmnezia(ep)
	case parser.KindHysteria2:
		return buildHysteria2(ep)
	}
	return nil, fmt.Errorf("unsupported kind %s", ep.Kind)
}

func buildVLESS(ep *parser.Endpoint) (map[string]any, error) {
	o := map[string]any{
		"type":        "vless",
		"tag":         ep.Tag,
		"server":      ep.Server,
		"server_port": ep.Port,
		"uuid":        ep.UUID,
	}
	if ep.Flow != "" {
		o["flow"] = ep.Flow
	}
	if tls := buildTLS(ep); tls != nil {
		o["tls"] = tls
	}
	if tr := buildTransport(ep); tr != nil {
		o["transport"] = tr
	}
	return o, nil
}

func buildVMess(ep *parser.Endpoint) (map[string]any, error) {
	o := map[string]any{
		"type":        "vmess",
		"tag":         ep.Tag,
		"server":      ep.Server,
		"server_port": ep.Port,
		"uuid":        ep.UUID,
		"alter_id":    ep.AlterID,
		"security":    ep.Cipher,
	}
	if tls := buildTLS(ep); tls != nil {
		o["tls"] = tls
	}
	if tr := buildTransport(ep); tr != nil {
		o["transport"] = tr
	}
	return o, nil
}

// buildAmnezia: sing-box не умеет AWG-параметры, но интерфейс awg-<tag>
// уже поднят userspace-демоном amneziawg-go ДО старта sing-box.
// Отдаём direct-outbound с bind_interface=awg-<tag>.
func buildAmnezia(ep *parser.Endpoint) (map[string]any, error) {
	return map[string]any{
		"type":           "direct",
		"tag":            ep.Tag,
		"bind_interface": parser.AwgIfaceName(ep),
	}, nil
}

// buildHysteria2 собирает outbound type=hysteria2.
// Пароль берётся из Params["password"] (уже раскодирован парсером).
// obfs добавляется только если в ссылке есть obfs и obfs-password.
// insecure включается только при явном insecure=1/true.
func buildHysteria2(ep *parser.Endpoint) (map[string]any, error) {
	password := ep.Params.Get("password")
	if password == "" {
		return nil, fmt.Errorf("hysteria2: missing password")
	}
	o := map[string]any{
		"type":        "hysteria2",
		"tag":         ep.Tag,
		"server":      ep.Server,
		"server_port": ep.Port,
		"password":    password,
	}
	if tls := buildTLS(ep); tls != nil {
		o["tls"] = tls
	}
	// obfs имеет смысл только в паре с паролем обфускации.
	if obfs := ep.Params.Get("obfs"); obfs != "" {
		if obfsPass := ep.Params.Get("obfs-password"); obfsPass != "" {
			o["obfs"] = map[string]any{
				"type":     obfs,
				"password": obfsPass,
			}
		}
	}
	return o, nil
}

func buildTLS(ep *parser.Endpoint) map[string]any {
	if ep.Security != "tls" && ep.Security != "reality" {
		return nil
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": ep.SNI,
	}
	// Reality без явного fingerprint должен получить utls=chrome,
	// иначе sing-box не сможет установить соединение.
	fp := ep.Fingerprint
	if fp == "" && ep.Security == "reality" {
		fp = "chrome"
	}
	if fp != "" {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": fp,
		}
	}
	// ALPN передаётся списком через запятую: alpn=h2,http/1.1.
	if alpn := ep.Params.Get("alpn"); alpn != "" {
		parts := strings.Split(alpn, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			tls["alpn"] = out
		}
	}
	// insecure — только при явном запросе, чтобы не ослаблять TLS по умолчанию.
	if v := ep.Params.Get("insecure"); v == "1" || strings.EqualFold(v, "true") {
		tls["insecure"] = true
	}
	if ep.Security == "reality" {
		tls["reality"] = map[string]any{
			"enabled":    true,
			"public_key": ep.PublicKey,
			"short_id":   ep.ShortID,
		}
	}
	return tls
}

func buildTransport(ep *parser.Endpoint) map[string]any {
	switch ep.Network {
	case "ws", "websocket":
		tr := map[string]any{
			"type": "ws",
			"path": ep.WSPath,
		}
		if ep.WSHost != "" {
			tr["headers"] = map[string]any{"Host": ep.WSHost}
		}
		return tr
	case "grpc":
		return map[string]any{
			"type":         "grpc",
			"service_name": ep.GRPCService,
		}
	case "http", "h2":
		tr := map[string]any{"type": "http"}
		if len(ep.Params["host"]) > 0 {
			tr["host"] = ep.Params["host"]
		}
		if ep.WSPath != "" {
			tr["path"] = ep.WSPath
		}
		return tr
	}
	return nil
}
