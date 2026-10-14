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
	case strings.HasPrefix(raw, "vpn://"):
		return ParseVPN(raw)
	}
	return nil, fmt.Errorf("unsupported scheme: %.24s", raw)
}
