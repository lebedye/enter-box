package parser

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ParseVLESS(raw string) (*Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}
	if u.Scheme != "vless" {
		return nil, fmt.Errorf("vless: scheme=%q", u.Scheme)
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fmt.Errorf("vless: missing uuid")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("vless: bad port %q", u.Port())
	}
	q := u.Query()
	ep := &Endpoint{
		Tag:         decodeFragment(u.Fragment),
		Kind:        KindVLESS,
		Server:      u.Hostname(),
		Port:        uint16(port),
		UUID:        u.User.Username(),
		Flow:        q.Get("flow"),
		Security:    q.Get("security"),
		SNI:         firstNonEmpty(q.Get("sni"), q.Get("peer")),
		Fingerprint: q.Get("fp"),
		PublicKey:   q.Get("pbk"),
		ShortID:     q.Get("sid"),
		Network:     firstNonEmpty(q.Get("type"), "tcp"),
		WSPath:      q.Get("path"),
		WSHost:      q.Get("host"),
		GRPCService: q.Get("serviceName"),
		Params:      q,
		Raw:         raw,
	}
	if ep.Tag == "" {
		ep.Tag = fmt.Sprintf("vless-%s-%d", ep.Server, ep.Port)
	}
	return ep, nil
}

func decodeFragment(f string) string {
	s, _ := url.QueryUnescape(f)
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
