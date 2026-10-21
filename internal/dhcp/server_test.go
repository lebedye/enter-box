package dhcp

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"enter-box/internal/config"
)

// fakeConn — in-memory net.PacketConn, перехватывающий WriteTo.
type fakeConn struct {
	writes [][]byte
}

func (f *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) { return 0, nil, io.EOF }
func (f *fakeConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	f.writes = append(f.writes, cp)
	return len(p), nil
}
func (f *fakeConn) Close() error                     { return nil }
func (f *fakeConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.LAN.Interface = "lo"
	cfg.LAN.Address = "192.168.1.1/24"
	cfg.LAN.DHCP.Gateway = "192.168.1.1"
	cfg.LAN.DHCP.RangeStart = "192.168.1.100"
	cfg.LAN.DHCP.RangeEnd = "192.168.1.110"
	cfg.LAN.DHCP.LeaseTime = time.Hour
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(logger, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return s
}

func discover(t *testing.T, mac net.HardwareAddr) *dhcpv4.DHCPv4 {
	t.Helper()
	d, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithHwAddr(mac),
	)
	if err != nil {
		t.Fatalf("build discover: %v", err)
	}
	return d
}

func request(t *testing.T, mac net.HardwareAddr, reqIP, sid net.IP) *dhcpv4.DHCPv4 {
	t.Helper()
	opts := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithHwAddr(mac),
	}
	if reqIP != nil {
		opts = append(opts, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(reqIP)))
	}
	if sid != nil {
		opts = append(opts, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(sid)))
	}
	r, err := dhcpv4.New(opts...)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return r
}

func release(t *testing.T, mac net.HardwareAddr) *dhcpv4.DHCPv4 {
	t.Helper()
	r, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRelease),
		dhcpv4.WithHwAddr(mac),
	)
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	return r
}

func parseReply(t *testing.T, raw []byte) *dhcpv4.DHCPv4 {
	t.Helper()
	m, err := dhcpv4.FromBytes(raw)
	if err != nil {
		t.Fatalf("parse reply: %v", err)
	}
	return m
}

// Шесть клиентов получают уникальные OFFER, затем тот же адрес в ACK.
func TestServerOfferAckUnique(t *testing.T) {
	s := testServer(t)
	conn := &fakeConn{}
	peer := &net.UDPAddr{IP: net.IPv4zero, Port: 68}

	seen := map[string]bool{}
	for i := 1; i <= 6; i++ {
		mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, byte(i)}

		// DISCOVER → ровно один новый ответ (OFFER).
		// После i-го клиента накоплено 2*i-1 записей: (i-1) пар OFFER+ACK и текущий OFFER.
		s.handle(conn, peer, discover(t, mac))
		if want := 2*i - 1; len(conn.writes) != want {
			t.Fatalf("client %d: expected %d writes after DISCOVER, got %d", i, want, len(conn.writes))
		}
		offer := parseReply(t, conn.writes[len(conn.writes)-1])
		if offer.MessageType() != dhcpv4.MessageTypeOffer {
			t.Fatalf("client %d: expected OFFER, got %s", i, offer.MessageType())
		}
		ip := offer.YourIPAddr.String()
		if seen[ip] {
			t.Fatalf("duplicate offer ip %s", ip)
		}
		seen[ip] = true

		// Проверяем option 54, DNS и router.
		if sid := offer.ServerIdentifier(); !sid.Equal(net.ParseIP("192.168.1.1")) {
			t.Fatalf("client %d: bad server id %v", i, sid)
		}
		if dns := offer.DNS(); len(dns) == 0 || !dns[0].Equal(net.ParseIP("192.168.1.1")) {
			t.Fatalf("client %d: bad dns %v", i, dns)
		}
		if r := offer.Router(); len(r) == 0 || !r[0].Equal(net.ParseIP("192.168.1.1")) {
			t.Fatalf("client %d: bad router %v", i, r)
		}

		// REQUEST на выданный адрес должен подтвердить тот же IP.
		// Ровно один новый ответ (ACK), всего записей становится 2*i.
		before := len(conn.writes)
		s.handle(conn, peer, request(t, mac, offer.YourIPAddr, net.ParseIP("192.168.1.1")))
		if len(conn.writes) != before+1 {
			t.Fatalf("client %d: expected exactly one ACK write, got %d new", i, len(conn.writes)-before)
		}
		ack := parseReply(t, conn.writes[len(conn.writes)-1])
		if ack.MessageType() != dhcpv4.MessageTypeAck {
			t.Fatalf("client %d: expected ACK, got %s", i, ack.MessageType())
		}
		if !ack.YourIPAddr.Equal(offer.YourIPAddr) {
			t.Fatalf("client %d: ack ip %s != offer ip %s", i, ack.YourIPAddr, offer.YourIPAddr)
		}
	}
}

// REQUEST чужому серверу игнорируется без ответа и без выделения аренды.
func TestServerForeignRequestIgnored(t *testing.T) {
	s := testServer(t)
	conn := &fakeConn{}
	peer := &net.UDPAddr{IP: net.IPv4zero, Port: 68}
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}

	s.handle(conn, peer, request(t, mac, net.ParseIP("192.168.1.100"), net.ParseIP("10.0.0.1")))
	if len(conn.writes) != 0 {
		t.Fatalf("expected no reply, got %d", len(conn.writes))
	}
	// Аренда не должна быть создана.
	if n := len(s.pool.leases); n != 0 {
		t.Fatalf("expected no leases, got %d", n)
	}
}

// RELEASE освобождает аренду: другой MAC получает тот же адрес.
func TestServerReleaseFreesLease(t *testing.T) {
	s := testServer(t)
	conn := &fakeConn{}
	peer := &net.UDPAddr{IP: net.IPv4zero, Port: 68}
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}

	s.handle(conn, peer, discover(t, mac))
	if len(conn.writes) != 1 {
		t.Fatalf("expected offer, got %d writes", len(conn.writes))
	}
	offer := parseReply(t, conn.writes[0])
	offeredIP := offer.YourIPAddr

	s.handle(conn, peer, release(t, mac))
	if len(conn.writes) != 1 {
		t.Fatalf("release must not produce reply, got %d writes", len(conn.writes))
	}
	// Аренда удалена из пула.
	if n := len(s.pool.leases); n != 0 {
		t.Fatalf("expected lease to be deleted, got %d leases", n)
	}
	// Другой MAC получает тот же освобождённый адрес.
	other := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x02}
	s.handle(conn, peer, discover(t, other))
	if len(conn.writes) != 2 {
		t.Fatalf("expected second offer, got %d writes", len(conn.writes))
	}
	second := parseReply(t, conn.writes[1])
	if !second.YourIPAddr.Equal(offeredIP) {
		t.Fatalf("expected released ip %s to be reused, got %s", offeredIP, second.YourIPAddr)
	}
}

// Пакет без MAC не вызывает паники и не отвечает.
func TestServerNoMAC(t *testing.T) {
	s := testServer(t)
	conn := &fakeConn{}
	peer := &net.UDPAddr{IP: net.IPv4zero, Port: 68}
	d, err := dhcpv4.New(dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// dhcpv4.New подставляет случайный MAC — обнуляем, чтобы проверить
	// именно защиту от пакета без MAC.
	d.ClientHWAddr = nil
	s.handle(conn, peer, d)
	if len(conn.writes) != 0 {
		t.Fatalf("expected no reply, got %d", len(conn.writes))
	}
}

// DISCOVER с чужим запрошенным IP всё равно получает OFFER из пула.
func TestServerDiscoverWithForeignRequestedIP(t *testing.T) {
	s := testServer(t)
	conn := &fakeConn{}
	peer := &net.UDPAddr{IP: net.IPv4zero, Port: 68}
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}

	d, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithHwAddr(mac),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("10.0.0.99"))),
	)
	if err != nil {
		t.Fatalf("build discover: %v", err)
	}
	s.handle(conn, peer, d)
	if len(conn.writes) != 1 {
		t.Fatalf("expected one OFFER, got %d writes", len(conn.writes))
	}
	offer := parseReply(t, conn.writes[0])
	if offer.MessageType() != dhcpv4.MessageTypeOffer {
		t.Fatalf("expected OFFER, got %s", offer.MessageType())
	}
	if offer.YourIPAddr.Equal(net.ParseIP("10.0.0.99")) {
		t.Fatalf("offer must not use foreign requested ip %s", offer.YourIPAddr)
	}
}

// New отклоняет некорректный шлюз.
func TestNewInvalidGateway(t *testing.T) {
	cfg := &config.Config{}
	cfg.LAN.Interface = "lo"
	cfg.LAN.Address = "192.168.1.1/24"
	cfg.LAN.DHCP.Gateway = "not-an-ip"
	cfg.LAN.DHCP.RangeStart = "192.168.1.100"
	cfg.LAN.DHCP.RangeEnd = "192.168.1.110"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(logger, cfg); err == nil {
		t.Fatal("expected error for invalid gateway")
	}
}
