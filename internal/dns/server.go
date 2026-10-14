// Package dns — встроенный DNS-форвардер для LAN-клиентов.
//
// Слушает на LAN-адресе (или 0.0.0.0) порт 53.
// Решает по qname, куда форвардить:
//   - RU-домены (по suffix или из routing.domestic_domains) → dns.domestic;
//   - всё остальное                                          → dns.remote.
//
// Внешняя маршрутизация запросов — на уровне ядра и sing-box:
// domestic-IP (77.88.8.8) попадёт под geoip-ru → direct,
// foreign-IP (1.1.1.1) — под route.final=proxy → VPN.
package dns

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"enter-box/internal/config"
)

type Server struct {
	logger *slog.Logger
	cfg    *config.Config

	udp *dns.Server
	tcp *dns.Server

	domesticUpstreams []string
	remoteUpstreams   []string
	domesticSuffixes  []string
	domesticDomains   map[string]struct{}
}

// New собирает сервер.
func New(logger *slog.Logger, cfg *config.Config) (*Server, error) {
	if len(cfg.DNS.Remote) == 0 {
		return nil, fmt.Errorf("dns.remote пуст")
	}

	s := &Server{
		logger:            logger,
		cfg:               cfg,
		domesticUpstreams: toHostPort(cfg.DNS.Domestic, 53),
		remoteUpstreams:   toHostPort(cfg.DNS.Remote, 53),
		domesticSuffixes:  normalizeSuffixes(cfg.Routing.DomesticSuffix),
		domesticDomains:   make(map[string]struct{}, len(cfg.Routing.DomesticDomains)),
	}
	for _, d := range cfg.Routing.DomesticDomains {
		s.domesticDomains[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
	}

	// Слушаем на 0.0.0.0:53, чтобы отвечать на всех LAN-интерфейсах.
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handle)

	s.udp = &dns.Server{Addr: "0.0.0.0:53", Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: "0.0.0.0:53", Net: "tcp", Handler: mux}
	return s, nil
}

// Start поднимает UDP+TCP слушателей.
func (s *Server) Start() error {
	errCh := make(chan error, 2)
	go func() {
		if err := s.udp.ListenAndServe(); err != nil {
			errCh <- fmt.Errorf("dns udp: %w", err)
		}
	}()
	go func() {
		if err := s.tcp.ListenAndServe(); err != nil {
			errCh <- fmt.Errorf("dns tcp: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(200 * time.Millisecond):
		s.logger.Info("dns server started", "listen", "0.0.0.0:53")
		return nil
	}
}

func (s *Server) Stop() {
	if s.udp != nil {
		_ = s.udp.Shutdown()
	}
	if s.tcp != nil {
		_ = s.tcp.Shutdown()
	}
}

func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
		return
	}
	q := req.Question[0]
	qname := strings.ToLower(strings.TrimSuffix(q.Name, "."))

	upstreams := s.pickUpstreams(qname)
	if len(upstreams) == 0 {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
		return
	}

	var lastErr error
	for _, up := range upstreams {
		resp, err := exchange(req, up)
		if err == nil && resp != nil {
			resp.Id = req.Id
			_ = w.WriteMsg(resp)
			return
		}
		lastErr = err
	}
	s.logger.Debug("dns: all upstreams failed", "q", qname, "err", lastErr)
	_ = w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
}

func (s *Server) pickUpstreams(qname string) []string {
	if _, ok := s.domesticDomains[qname]; ok {
		return s.domesticUpstreams
	}
	for _, suf := range s.domesticSuffixes {
		if strings.HasSuffix(qname, suf) {
			return s.domesticUpstreams
		}
	}
	return s.remoteUpstreams
}

func exchange(req *dns.Msg, upstream string) (*dns.Msg, error) {
	c := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
	m := req.Copy()
	resp, _, err := c.Exchange(m, upstream)
	return resp, err
}

func toHostPort(addrs []string, port uint16) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(a); err == nil {
			out = append(out, a)
			continue
		}
		out = append(out, net.JoinHostPort(a, fmt.Sprintf("%d", port)))
	}
	return out
}

func normalizeSuffixes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
