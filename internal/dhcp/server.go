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
	// Проверяем, что шлюз задан корректным IPv4-адресом.
	if net.ParseIP(cfg.LAN.DHCP.Gateway).To4() == nil {
		return nil, fmt.Errorf("invalid dhcp gateway %q", cfg.LAN.DHCP.Gateway)
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

	// Пакет без MAC-адреса обработать нельзя — молча игнорируем.
	if len(mac) == 0 {
		s.logger.Warn("dhcp packet without client mac")
		return
	}

	// Определяем тип ответа ДО выделения аренды, чтобы не занимать
	// адрес под неподдерживаемые сообщения.
	var msgType dhcpv4.MessageType
	switch req.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		msgType = dhcpv4.MessageTypeOffer
	case dhcpv4.MessageTypeRequest:
		msgType = dhcpv4.MessageTypeAck
	case dhcpv4.MessageTypeRelease:
		// Клиент освобождает адрес — удаляем аренду и не отвечаем.
		s.pool.Release(mac)
		return
	default:
		return
	}

	// REQUEST, адресованный другому серверу, игнорируем до выделения аренды.
	if req.MessageType() == dhcpv4.MessageTypeRequest {
		if sid := req.ServerIdentifier(); sid != nil && !sid.Equal(serverIP) {
			return
		}
	}

	lease := s.pool.Offer(mac)
	if lease == nil {
		s.logger.Warn("dhcp pool exhausted", "mac", mac.String())
		return
	}

	// Проверка запрошенного адреса применяется только к REQUEST:
	// DISCOVER может нести старый предпочитаемый IP, но всё равно
	// должен получить OFFER с адресом из пула.
	if req.MessageType() == dhcpv4.MessageTypeRequest {
		if reqIP := req.RequestedIPAddress(); reqIP != nil && !reqIP.Equal(lease.IP) {
			return
		}
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
		// WithServerIP задаёт только siaddr; option 54 нужно добавить явно.
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(serverIP)),
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
