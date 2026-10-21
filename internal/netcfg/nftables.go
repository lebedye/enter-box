package netcfg

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// nftTable — имя таблицы nftables.
//
// Без дефиса: старые версии nft могут не принимать "-" в идентификаторах
// цепочек, а таблицы — принимают не везде. enter_box безопаснее.
const nftTable = "enter_box"

// applyNftScriptMulti создаёт таблицу inet enter_box с:
//   - forward-правилами для каждого LAN-интерфейса (iifname ... oifname wan/tun),
//   - NAT (masquerade) для LAN-подсети в сторону WAN и tun0.
//
// NAT не зависит от того, какой LAN-интерфейс принял пакет — важен
// только source CIDR. Поэтому правило пишем один раз, а не в цикле.
func applyNftScriptMulti(ctx context.Context, table, lanCIDR, wan, tun string, lans []string) error {
	var fwd strings.Builder
	for _, lan := range lans {
		fmt.Fprintf(&fwd,
			"    iifname %q oifname %q accept\n"+
				"    iifname %q oifname %q accept\n",
			lan, wan, lan, tun)
	}

	script := fmt.Sprintf(`
add table inet %[1]s
delete table inet %[1]s

table inet %[1]s {
  chain forward {
    type filter hook forward priority filter; policy drop;
    ct state established,related accept
%[2]s  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr %[3]s oifname %[4]q masquerade
    ip saddr %[3]s oifname %[5]q masquerade
  }
}`, table, fwd.String(), lanCIDR, wan, tun)

	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft apply: %w (%s)", err, string(out))
	}
	return nil
}

func cleanupNftables(ctx context.Context) error {
	return runCmd(ctx, "nft", "delete", "table", "inet", nftTable)
}
