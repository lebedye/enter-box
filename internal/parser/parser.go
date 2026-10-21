package parser

import (
	"fmt"
	"strings"
)

func ParseLink(raw string) (*Endpoint, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "vless://"):
		return ParseVLESS(raw)
	case strings.HasPrefix(raw, "vmess://"):
		return ParseVMess(raw)
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		return ParseHysteria2(raw)
	case strings.HasPrefix(raw, "vpn://"):
		return ParseVPN(raw)
	}
	// Не включаем исходную ссылку в ошибку: в ней могут находиться пароль и UUID.
	scheme, _, _ := strings.Cut(raw, ":")
	if len(scheme) > 24 || strings.ContainsAny(scheme, "@/?#") {
		scheme = "unknown"
	}
	return nil, fmt.Errorf("unsupported scheme: %s", scheme)
}
