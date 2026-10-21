package parser

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParseHysteria2 разбирает ссылки hysteria2:// и hy2://.
// Формат: hysteria2://password@host:port?sni=...&obfs=...#tag
// Пароль берётся из userinfo и сохраняется в Params["password"] уже
// раскодированным (url.Userinfo делает это автоматически).
func ParseHysteria2(raw string) (*Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: invalid uri")
	}

	// Хост и порт обязательны: без них outbound собрать нельзя.
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("hysteria2: missing host")
	}
	portStr := u.Port()
	if portStr == "" {
		return nil, fmt.Errorf("hysteria2: missing port")
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("hysteria2: invalid port")
	}

	// Пароль обязателен. u.User.Password() возвращает уже раскодированное
	// значение, но в hysteria2 пароль обычно лежит прямо в username.
	password := ""
	if u.User != nil {
		if p, ok := u.User.Password(); ok {
			password = p
		} else {
			password = u.User.Username()
		}
	}
	if password == "" {
		return nil, fmt.Errorf("hysteria2: missing password")
	}

	q := u.Query()

	// security: допускается отсутствие (по умолчанию tls) либо явный tls.
	// Любое другое значение — ошибка конфигурации.
	security := strings.ToLower(q.Get("security"))
	if security != "" && security != "tls" {
		return nil, fmt.Errorf("hysteria2: unsupported security")
	}

	// SNI: явный параметр, иначе берём хост.
	sni := q.Get("sni")
	if sni == "" {
		sni = host
	}

	// Тег: из фрагмента, иначе host:port.
	tag := u.Fragment
	if tag == "" {
		tag = host + ":" + portStr
	}

	// Пароль кладём в Params, чтобы buildHysteria2 не лез в Raw.
	q.Set("password", password)

	return &Endpoint{
		Tag:      tag,
		Kind:     KindHysteria2,
		Server:   host,
		Port:     uint16(port),
		Raw:      raw,
		Security: "tls",
		SNI:      sni,
		Params:   q,
	}, nil
}
