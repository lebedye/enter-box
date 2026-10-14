package parser

import (
	"context"
	"fmt"
	"strings"

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
	key := fmt.Sprintf("%s|%s|%d|%s", e.Kind, e.Server, e.Port, e.UUID)
	if _, dup := p.seen[key]; dup {
		return
	}
	p.seen[key] = struct{}{}
	p.eps = append(p.eps, e)
}

func (p *Pool) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	for _, raw := range cfg.Endpoints {
		ep, err := ParseLink(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("endpoint %q: %w", raw, err)
		}
		p.Add(ep)
	}
	for _, sub := range cfg.Subscriptions {
		eps, err := LoadSubscription(ctx, sub)
		if err != nil {
			return fmt.Errorf("subscription %s: %w", sub.URL, err)
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
