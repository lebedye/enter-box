// Package singbox генерирует JSON-совместимый конфиг sing-box.
package singbox

import (
	"fmt"

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

func buildTLS(ep *parser.Endpoint) map[string]any {
	if ep.Security != "tls" && ep.Security != "reality" {
		return nil
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": ep.SNI,
	}
	if ep.Fingerprint != "" {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": ep.Fingerprint,
		}
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
