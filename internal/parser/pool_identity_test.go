package parser

import (
	"net/url"
	"testing"
)

func TestPoolAddDistinctVLESSOnSameAddress(t *testing.T) {
	base := func() *Endpoint {
		return &Endpoint{
			Kind:   KindVLESS,
			Server: "example.com",
			Port:   443,
			UUID:   "11111111-1111-1111-1111-111111111111",
		}
	}

	p := NewPool()

	a := base()
	a.SNI = "a.example.com"
	a.Network = "tcp"
	a.Security = "reality"
	a.PublicKey = "pk-a"
	a.ShortID = "sid-a"
	p.Add(a)

	b := base()
	b.SNI = "b.example.com"
	b.Network = "tcp"
	b.Security = "reality"
	b.PublicKey = "pk-b"
	b.ShortID = "sid-b"
	p.Add(b)

	c := base()
	c.SNI = "a.example.com"
	c.Network = "ws"
	c.WSPath = "/ws"
	c.Security = "tls"
	p.Add(c)

	if got := p.Len(); got != 3 {
		t.Fatalf("expected 3 distinct endpoints, got %d", got)
	}
}

func TestPoolAddDedupIgnoresTagAndRaw(t *testing.T) {
	p := NewPool()

	a := &Endpoint{
		Tag:     "first",
		Kind:    KindVLESS,
		Server:  "example.com",
		Port:    443,
		UUID:    "11111111-1111-1111-1111-111111111111",
		SNI:     "example.com",
		Network: "tcp",
		Raw:     "vless://first",
		Params:  url.Values{"type": []string{"tcp"}},
	}
	b := &Endpoint{
		Tag:     "second",
		Kind:    KindVLESS,
		Server:  "example.com",
		Port:    443,
		UUID:    "11111111-1111-1111-1111-111111111111",
		SNI:     "example.com",
		Network: "tcp",
		Raw:     "vless://second",
		Params:  url.Values{"type": []string{"tcp"}},
	}

	p.Add(a)
	p.Add(b)

	if got := p.Len(); got != 1 {
		t.Fatalf("expected 1 endpoint after dedup, got %d", got)
	}
}
