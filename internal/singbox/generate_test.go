package singbox

import (
	"testing"

	"enter-box/internal/config"
	"enter-box/internal/parser"
)

// Проверяем сохранность параметров на границе нашего генератора и sing-box:
// обычный encoding/json раньше молча оставлял Options протоколов пустыми.
func TestGeneratePreservesProtocolOptions(t *testing.T) {
	cfg := &config.Config{}
	cfg.WAN.Name = "usb0"
	cfg.TUN.Interface = "tun0"
	cfg.TUN.Address = "10.10.10.1/24"
	cfg.TUN.Stack = "system"
	cfg.DNS.Remote = []string{"1.1.1.1"}
	cfg.DNS.Domestic = []string{"77.88.8.8"}
	cfg.Routing.UseRUDirect = true
	cfg.Routing.Final = "proxy"
	pool := parser.NewPool()
	pool.Add(&parser.Endpoint{Kind: parser.KindAmnezia, Tag: "test-awg"})
	opts, err := Generate(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, inbound := range opts.Inbounds {
		if inbound.Options == nil {
			t.Fatalf("inbound %s lost its protocol options", inbound.Tag)
		}
	}
	for _, outbound := range opts.Outbounds {
		if outbound.Options == nil {
			t.Fatalf("outbound %s lost its protocol options", outbound.Tag)
		}
	}
}
