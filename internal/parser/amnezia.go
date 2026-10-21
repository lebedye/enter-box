package parser

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

func ParseVPN(raw string) (*Endpoint, error) {
	payload := strings.TrimPrefix(raw, "vpn://")
	dec, err := b64Decode(payload)
	if err != nil {
		return nil, fmt.Errorf("vpn: base64: %w", err)
	}
	dec, err = decompressAmneziaPayload(dec)
	if err != nil {
		return nil, fmt.Errorf("vpn: unpack: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(dec, &root); err != nil {
		return nil, fmt.Errorf("vpn: json: %w", err)
	}
	// Формат Amnezia менялся: старые ключи хранят last_config,
	// новые — client_config.native_config. Ищем сам INI по признакам,
	// чтобы не ломаться при очередном переименовании обёртки.
	ini := findAmneziaINI(root)
	if ini == "" {
		return nil, fmt.Errorf("vpn: no AWG/WireGuard native config")
	}
	hostname, _ := root["hostName"].(string)
	if hostname == "" {
		hostname, _ = root["name"].(string)
	}
	return parseAmneziaINI(ini, raw, hostname)
}

func decompressAmneziaPayload(data []byte) ([]byte, error) {
	// qCompress пишет размер JSON в первых четырёх байтах,
	// а затем обычный zlib-поток. Без этого новые vpn:// не читались.
	if len(data) >= 6 {
		zr, err := zlib.NewReader(bytes.NewReader(data[4:]))
		if err == nil {
			plain, readErr := io.ReadAll(zr)
			_ = zr.Close()
			// Некоторые версии Amnezia записывают неточный размер в заголовке.
			// Валидный распакованный JSON является более надёжной проверкой.
			if readErr == nil && json.Valid(plain) {
				return plain, nil
			}
		}
	}
	if json.Valid(data) {
		return data, nil
	}
	return nil, fmt.Errorf("payload is neither qCompress nor JSON")
}

func findAmneziaINI(value any) string {
	switch v := value.(type) {
	case string:
		// Gateway может завернуть native_config ещё в одну JSON-строку.
		// Сначала раскрываем такую обёртку, иначе парсер примет весь JSON за INI.
		if strings.HasPrefix(strings.TrimSpace(v), "{") {
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				if found := findAmneziaINI(nested); found != "" {
					return found
				}
			}
		}
		if strings.Contains(v, "[Interface]") && strings.Contains(v, "[Peer]") {
			return v
		}
	case []any:
		for _, item := range v {
			if found := findAmneziaINI(item); found != "" {
				return found
			}
		}
	case map[string]any:
		// Сначала проверяем точные ключи, чтобы не взять похожую
		// серверную заготовку раньше клиентской конфигурации.
		for _, key := range []string{"native_config", "last_config", "config"} {
			if s, ok := v[key].(string); ok && strings.Contains(s, "[Interface]") && strings.Contains(s, "[Peer]") {
				// last_config сам может быть JSON-строкой с полем config.
				// Пропускаем значение через ту же рекурсию, чтобы раскрыть обёртку.
				if found := findAmneziaINI(s); found != "" {
					return found
				}
			}
		}
		for _, item := range v {
			if found := findAmneziaINI(item); found != "" {
				return found
			}
		}
	}
	return ""
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
			case "s3":
				awg.S3, _ = strconv.Atoi(val)
			case "s4":
				awg.S4, _ = strconv.Atoi(val)
			case "h1":
				awg.H1 = unquote(val)
			case "h2":
				awg.H2 = unquote(val)
			case "h3":
				awg.H3 = unquote(val)
			case "h4":
				awg.H4 = unquote(val)
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
			case "headerprotectionkey":
				awg.HeaderProtectionKey = unquote(val)
			case "contentpaddingaddition":
				awg.ContentPaddingAddition = unquote(val)
			case "rekeyaftertime":
				awg.RekeyAfterTime = unquote(val)
			case "rekeytimeout":
				awg.RekeyTimeout = unquote(val)
			case "rejectaftertime":
				awg.RejectAfterTime = unquote(val)
			case "keepalivetimeout":
				awg.KeepaliveTimeout = unquote(val)
			case "maxhandshakeattempts":
				awg.MaxHandshakeAttempts = unquote(val)
			case "randomtrailers":
				awg.RandomTrailers = parseToggle(val)
			case "disablecookies":
				awg.DisableCookies = parseToggle(val)
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
			case "persistentkeepalive":
				awg.PersistentKeepalive = unquote(val)
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

func parseToggle(s string) int {
	// AWG принимает флаги как 0/1, а экспорт может записать
	// их словами true/false.
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return 1
	default:
		return 0
	}
}
