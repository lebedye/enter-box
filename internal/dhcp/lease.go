package dhcp

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Lease — выданная аренда.
type Lease struct {
	IP      net.IP
	MAC     net.HardwareAddr
	Expires time.Time
}

// Pool — последовательный аллокатор адресов с исключениями.
type Pool struct {
	mu      sync.Mutex
	start   net.IP
	end     net.IP
	exclude []net.IP
	ttl     time.Duration
	leases  map[string]*Lease
}

// NewPool создаёт пул.
//
// excludeSpecs: список "IP" или "IP-IP", которые НЕ выдаём.
func NewPool(start, end string, excludeSpecs []string, ttl time.Duration) (*Pool, error) {
	s := net.ParseIP(strings.TrimSpace(start)).To4()
	e := net.ParseIP(strings.TrimSpace(end)).To4()
	if s == nil || e == nil {
		return nil, fmt.Errorf("invalid dhcp range %s - %s", start, end)
	}
	if ttl == 0 {
		ttl = time.Hour
	}
	exclude, err := parseExcludes(excludeSpecs)
	if err != nil {
		return nil, err
	}
	return &Pool{
		start:   s,
		end:     e,
		exclude: exclude,
		ttl:     ttl,
		leases:  make(map[string]*Lease),
	}, nil
}

func parseExcludes(specs []string) ([]net.IP, error) {
	var out []net.IP
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(spec, "-"); ok {
			a := net.ParseIP(strings.TrimSpace(lo)).To4()
			b := net.ParseIP(strings.TrimSpace(hi)).To4()
			if a == nil || b == nil {
				return nil, fmt.Errorf("bad exclude range %q", spec)
			}
			for ip := cloneIP(a); !ipGreater(ip, b); incIP(ip) {
				out = append(out, cloneIP(ip))
			}
			continue
		}
		ip := net.ParseIP(spec).To4()
		if ip == nil {
			return nil, fmt.Errorf("bad exclude ip %q", spec)
		}
		out = append(out, ip)
	}
	return out, nil
}

func (p *Pool) isExcluded(ip net.IP) bool {
	for _, ex := range p.exclude {
		if ex.Equal(ip) {
			return true
		}
	}
	return false
}

// Offer возвращает (или выделяет) аренду для MAC.
func (p *Pool) Offer(mac net.HardwareAddr) *Lease {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := mac.String()

	if l, ok := p.leases[key]; ok && time.Now().Before(l.Expires) {
		l.Expires = time.Now().Add(p.ttl)
		return l
	}

	used := map[string]bool{}
	for _, l := range p.leases {
		if time.Now().Before(l.Expires) {
			used[l.IP.String()] = true
		}
	}

	for ip := cloneIP(p.start); !ipGreater(ip, p.end); incIP(ip) {
		if p.isExcluded(ip) {
			continue
		}
		if used[ip.String()] {
			continue
		}
		l := &Lease{IP: cloneIP(ip), MAC: mac, Expires: time.Now().Add(p.ttl)}
		p.leases[key] = l
		return l
	}
	return nil
}

func cloneIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	return out
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func ipGreater(a, b net.IP) bool {
	for i := 0; i < len(a); i++ {
		if a[i] > b[i] {
			return true
		}
		if a[i] < b[i] {
			return false
		}
	}
	return false
}
