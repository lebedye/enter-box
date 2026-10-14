package parser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type vmessJSON struct {
	V    string `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port string `json:"port"`
	ID   string `json:"id"`
	Aid  string `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	FP   string `json:"fp"`
	ALPN string `json:"alpn"`
}

func ParseVMess(raw string) (*Endpoint, error) {
	body := strings.TrimPrefix(raw, "vmess://")
	body = strings.TrimSpace(body)
	dec, err := b64Decode(body)
	if err != nil {
		return nil, fmt.Errorf("vmess: base64: %w", err)
	}
	var v vmessJSON
	if err := json.Unmarshal(dec, &v); err != nil {
		return nil, fmt.Errorf("vmess: json: %w", err)
	}
	if v.Add == "" || v.ID == "" {
		return nil, fmt.Errorf("vmess: missing server/uuid")
	}
	port, _ := strconv.Atoi(v.Port)
	aid, _ := strconv.Atoi(v.Aid)
	ep := &Endpoint{
		Tag:         v.PS,
		Kind:        KindVMess,
		Server:      v.Add,
		Port:        uint16(port),
		UUID:        v.ID,
		AlterID:     aid,
		Cipher:      v.Scy,
		Network:     firstNonEmpty(v.Net, "tcp"),
		WSPath:      v.Path,
		WSHost:      firstNonEmpty(v.Host, v.SNI),
		SNI:         v.SNI,
		Fingerprint: v.FP,
		Security:    normalizeVMessTLS(v.TLS),
		Raw:         raw,
	}
	if ep.Tag == "" {
		ep.Tag = fmt.Sprintf("vmess-%s-%d", ep.Server, ep.Port)
	}
	if ep.Cipher == "" {
		ep.Cipher = "auto"
	}
	return ep, nil
}

func normalizeVMessTLS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "tls", "reality":
		return s
	}
	return ""
}

func b64Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("base64 decode failed")
}
