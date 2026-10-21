package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"enter-box/internal/amneziaapi"
	"enter-box/internal/config"
)

type Pool struct {
	eps  []*Endpoint
	seen map[string]struct{}
}

func NewPool() *Pool {
	return &Pool{seen: make(map[string]struct{})}
}

func (p *Pool) Len() int         { return len(p.eps) }
func (p *Pool) All() []*Endpoint { return p.eps }

func (p *Pool) Add(e *Endpoint) {
	// Идентичность соединения определяется всеми значимыми полями Endpoint,
	// а не только адресом и UUID: на одном адресе с одним UUID могут
	// сосуществовать разные SNI, transport и Reality-параметры.
	// Tag и Raw — метаданные, а не часть соединения, поэтому обнуляем их.
	// url.Values в encoding/json сортируется по ключам, так что ключ стабилен.
	key := endpointIdentity(e)
	if _, dup := p.seen[key]; dup {
		return
	}
	p.seen[key] = struct{}{}
	p.eps = append(p.eps, e)
}

func endpointIdentity(e *Endpoint) string {
	clone := *e
	clone.Tag = ""
	clone.Raw = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		// Fallback без секретов: только адресные поля.
		return fmt.Sprintf("%s|%s|%d", e.Kind, e.Server, e.Port)
	}
	return string(b)
}

func (p *Pool) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	rawEndpoints := append([]string(nil), cfg.Endpoints...)
	for _, path := range cfg.EndpointFiles {
		// Секретный Premium-ключ удобнее держать отдельным файлом с mode 0600,
		// чтобы он не попадал в общий YAML и резервные копии документации.
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read endpoint file %s: %w", path, err)
		}
		rawEndpoints = append(rawEndpoints, strings.TrimSpace(string(content)))
	}
	for _, raw := range rawEndpoints {
		raw = strings.TrimSpace(raw)
		// Premium-ключ содержит API-токен, а не готовый AWG-туннель.
		// Перед обычным парсингом безопасно обмениваем его на
		// актуальную конфигурацию через официальный Amnezia Gateway.
		if amneziaapi.IsBootstrapVPN(raw) {
			// Кэш избавляет от повторных запросов к шлюзу и позволяет
			// пережить его временную недоступность.
			resolved, err := amneziaapi.ResolveCached(ctx, raw)
			if err != nil {
				return fmt.Errorf("resolve Amnezia Premium endpoint: %w", err)
			}
			raw = resolved
		}
		ep, err := ParseLink(raw)
		if err != nil {
			// Ссылка содержит приватные VPN-ключи: её нельзя включать в журнал.
			return fmt.Errorf("parse endpoint: %w", err)
		}
		p.Add(ep)
	}
	for _, sub := range cfg.Subscriptions {
		eps, err := LoadSubscription(ctx, sub)
		if err != nil {
			// URL подписки содержит токен доступа; в журнал передаём только её метку.
			return fmt.Errorf("subscription %q: %w", sub.Tag, err)
		}
		for _, ep := range eps {
			p.Add(ep)
		}
	}
	if p.Len() == 0 {
		return fmt.Errorf("no endpoints loaded")
	}
	return nil
}
