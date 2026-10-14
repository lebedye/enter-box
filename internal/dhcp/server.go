package dhcp

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"

	"enter-box/internal/config"
)

// Server — DHCPv4-сервер, слушающий на всех LAN-интерфейсах.
// Один пул адресов на всех клиентов.
type Server struct {
	logger  *slog.Logger
	cfg     *config.Config
	pool    *Pool
	ifaces  []string
	servers []*server4.Server
}

func New(logger *slog.Logger, cfg *config.Config) (*Server, error) {
	p, err := NewPool(
		cfg.LAN.DHCP.RangeStart,
		cfg.LAN.DHCP.RangeEnd,
		cfg.LAN.DHCP.Exclude,
		cfg.LAN.DHCP.LeaseTime,
	)
	if err != nil {
		return nil, err
	}
	ifaces := append([]string{cfg.LAN.Interface}, cfg.LAN.ExtraInterfaces...)
	return &Server{
		logger: logger,
		cfg:    cfg,
		pool:   p,
		ifaces: ifaces,
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	for _, name := range s.ifaces {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return fmt.Errorf("iface %s: %w", name, err)
		}
		laddr := &net.UDPAddr{IP: net.IPv4zero, Port: 67}
		srv, err := server4.NewServer(iface.Name, laddr, s.handle)
		if err != nil {
			return fmt.Errorf("dhcp listen %s: %w", name, err)
		}
		s.servers = append(s.servers, srv)
		go func(name string, srv *server4.Server) {
			if err := srv.Serve(); err != nil {
				s.logger.Warn("dhcp serve stopped", "iface", name, "err", err)
			}
		}(name, srv)
		s.logger.Info("dhcp server started", "iface", name)
	}
	return nil
}

func (s *Server) Stop() {
	for _, srv := range s.servers {
		_ = srv.Close()
	}
}

func (s *Server) handle(conn net.PacketConn, peer net.Addr, req *dhcpv4.DHCPv4) {
	serverIP := net.ParseIP(s.cfg.LAN.DHCP.Gateway)
	mac := req.ClientHWAddr

	lease := s.pool.Offer(mac)
	if lease == nil {
		s.logger.Warn("dhcp pool exhausted", "mac", mac.String())
		return
	}

	var msgType dhcpv4.MessageType
	switch req.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		msgType = dhcpv4.MessageTypeOffer
	case dhcpv4.MessageTypeRequest:
		msgType = dhcpv4.MessageTypeAck
	default:
		return
	}

	// Маска из lan.address.
	_, ipnet, err := net.ParseCIDR(s.cfg.LAN.Address)
	var mask net.IPMask = net.CIDRMask(24, 32)
	if err == nil {
		mask = ipnet.Mask
	}

	resp, err := dhcpv4.NewReplyFromRequest(req,
		dhcpv4.WithMessageType(msgType),
		dhcpv4.WithServerIP(serverIP),
		dhcpv4.WithYourIP(lease.IP),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(mask)),
		dhcpv4.WithOption(dhcpv4.OptRouter(serverIP)),
		dhcpv4.WithOption(dhcpv4.OptDNS(serverIP)),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(s.cfg.LAN.DHCP.LeaseTime)),
	)
	if err != nil {
		s.logger.Error("dhcp reply", "err", err)
		return
	}
	_, _ = conn.WriteTo(resp.ToBytes(), peer)
}
