package netcfg

import (
	"context"
	"fmt"
	"os/exec"
)

// applySysctl включает IP forwarding и отключает rp_filter.
func applySysctl(ctx context.Context) error {
	settings := map[string]string{
		"net.ipv4.ip_forward":                  "1",
		"net.ipv6.conf.all.forwarding":         "1",
		"net.ipv4.conf.all.rp_filter":          "0",
		"net.ipv4.conf.default.rp_filter":      "0",
		"net.ipv4.conf.all.send_redirects":     "0",
		"net.ipv4.conf.default.send_redirects": "0",
	}
	for k, v := range settings {
		if err := runCmd(ctx, "sysctl", "-w", k+"="+v); err != nil {
			return fmt.Errorf("sysctl %s: %w", k, err)
		}
	}
	return nil
}

func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, string(out))
	}
	return nil
}
