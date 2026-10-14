package parser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type amneziaRoot struct {
	HostName   string             `json:"hostName"`
	Containers []amneziaContainer `json:"containers"`
}

type amneziaContainer struct {
	Container string      `json:"container"`
	AWG       *amneziaCfg `json:"awg,omitempty"`
	WG        *amneziaCfg `json:"wg,omitempty"`
}

type amneziaCfg struct {
	LastConfig string `json:"last_config"`
}

func ParseVPN(raw string) (*Endpoint, error) {
	payload := strings.TrimPrefix(raw, "vpn://")
	dec, err := b64Decode(payload)
	if err != nil {
		return nil, fmt.Errorf("vpn: base64: %w", err)
	}
	var root amneziaRoot
	if err := json.Unmarshal(dec, &root); err != nil {
		return nil, fmt.Errorf("vpn: json: %w", err)
	}
	var ini string
	for _, c := range root.Containers {
		if c.AWG != nil && c.AWG.LastConfig != "" {
			ini = c.AWG.LastConfig
			break
		}
		if c.WG != nil && c.WG.LastConfig != "" {
			ini = c.WG.LastConfig
			break
		}
	}
	if ini == "" {
		return nil, fmt.Errorf("vpn: no awg/wg last_config")
	}
	return parseAmneziaINI(ini, raw, root.HostName)
}

var reIPv6Endpoint = regexp.MustCompile(`^\[(.+)\]:(\d+)$`)

func parseAmneziaINI(cfg, raw, hostname string) (*Endpoint, error) {
	awg := &AmneziaWGParams{}
	var peerHost, peerPort string
	section := ""

	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch section {
		case "interface":
			switch key {
			case "privatekey":
				awg.PrivateKey = val
			case "address":
				awg.Address = append(awg.Address, splitCSV(val)...)
			case "dns":
				awg.DNS = append(awg.DNS, splitCSV(val)...)
			case "mtu":
				awg.MTU, _ = strconv.Atoi(val)
			case "jc":
				awg.Jc, _ = strconv.Atoi(val)
			case "jmin":
				awg.Jmin, _ = strconv.Atoi(val)
			case "jmax":
				awg.Jmax, _ = strconv.Atoi(val)
			case "s1":
				awg.S1, _ = strconv.Atoi(val)
			case "s2":
				awg.S2, _ = strconv.Atoi(val)
			case "h1":
				awg.H1 = parseIntFlexible(val)
			case "h2":
				awg.H2 = parseIntFlexible(val)
			case "h3":
				awg.H3 = parseIntFlexible(val)
			case "h4":
				awg.H4 = parseIntFlexible(val)
			case "i1":
				awg.I1 = unquote(val)
			case "i2":
				awg.I2 = unquote(val)
			case "i3":
				awg.I3 = unquote(val)
			case "i4":
				awg.I4 = unquote(val)
			case "i5":
				awg.I5 = unquote(val)
			}
		case "peer":
			switch key {
			case "publickey":
				awg.PeerPublic = val
			case "presharedkey":
				awg.Preshared = val
			case "endpoint":
				if m := reIPv6Endpoint.FindStringSubmatch(val); m != nil {
					peerHost, peerPort = m[1], m[2]
				} else if h, p, ok := strings.Cut(val, ":"); ok {
					peerHost, peerPort = h, p
				}
			}
		}
	}
	if awg.PrivateKey == "" || awg.PeerPublic == "" || peerHost == "" {
		return nil, fmt.Errorf("vpn: incomplete awg config (priv/pub/endpoint missing)")
	}
	port, _ := strconv.Atoi(peerPort)
	tag := hostname
	if tag == "" {
		tag = peerHost
	}
	return &Endpoint{
		Tag:    "awg-" + tag,
		Kind:   KindAmnezia,
		Server: peerHost,
		Port:   uint16(port),
		AWG:    awg,
		Raw:    raw,
	}, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseIntFlexible(s string) int {
	s = strings.TrimSpace(s)
	if v, err := strconv.ParseInt(s, 0, 64); err == nil {
		return int(v)
	}
	return 0
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
