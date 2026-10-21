package dhcp

import (
	"net"
	"testing"
	"time"
)

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	m, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("parse mac %q: %v", s, err)
	}
	return m
}

// Проверяем, что исключённые адреса не выдаются.
func TestPoolRespectsExcludes(t *testing.T) {
	p, err := NewPool("192.168.1.10", "192.168.1.12", []string{"192.168.1.10", "192.168.1.11"}, time.Hour)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	l := p.Offer(mustMAC(t, "aa:bb:cc:dd:ee:01"))
	if l == nil {
		t.Fatal("expected lease")
	}
	if !l.IP.Equal(net.ParseIP("192.168.1.12")) {
		t.Fatalf("expected 192.168.1.12, got %s", l.IP)
	}
}

// Исчерпанный пул возвращает nil.
func TestPoolExhausted(t *testing.T) {
	p, err := NewPool("192.168.1.10", "192.168.1.10", nil, time.Hour)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if l := p.Offer(mustMAC(t, "aa:bb:cc:dd:ee:01")); l == nil {
		t.Fatal("expected first lease")
	}
	if l := p.Offer(mustMAC(t, "aa:bb:cc:dd:ee:02")); l != nil {
		t.Fatalf("expected nil for exhausted pool, got %s", l.IP)
	}
}

// Release освобождает адрес для повторной выдачи.
func TestPoolRelease(t *testing.T) {
	p, err := NewPool("192.168.1.10", "192.168.1.10", nil, time.Hour)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	mac := mustMAC(t, "aa:bb:cc:dd:ee:01")
	if l := p.Offer(mac); l == nil {
		t.Fatal("expected lease")
	}
	p.Release(mac)
	if l := p.Offer(mustMAC(t, "aa:bb:cc:dd:ee:02")); l == nil {
		t.Fatal("expected lease after release")
	}
}
