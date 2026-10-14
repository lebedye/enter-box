package singbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/option"

	"enter-box/internal/config"
	"enter-box/internal/dhcp"
	enterdns "enter-box/internal/dns"
	"enter-box/internal/netcfg"
	"enter-box/internal/parser"
)

// Runner — жизненный цикл демона: сеть → AWG → sing-box → DNS → DHCP.
type Runner struct {
	logger *slog.Logger
	cfg    *config.Config

	mu sync.Mutex

	pool     *parser.Pool
	opts     *option.Options
	instance *box.Box

	netSetup *netcfg.Setup
	awg      *netcfg.AmneziaManager
	dnsSrv   *enterdns.Server
	dhcpSrv  *dhcp.Server
}

func NewRunner(logger *slog.Logger, cfg *config.Config, pool *parser.Pool, opts *option.Options) (*Runner, error) {
	return &Runner{
		logger: logger,
		cfg:    cfg,
		pool:   pool,
		opts:   opts,
	}, nil
}

func (r *Runner) Start(ctx context.Context) error {
	// 1. Сеть: sysctl, LAN IP, nftables, static-IP unit, DNS stub off.
	ns, err := netcfg.New(r.logger, r.cfg)
	if err != nil {
		return fmt.Errorf("netcfg init: %w", err)
	}
	if err := ns.Apply(ctx); err != nil {
		return fmt.Errorf("netcfg apply: %w", err)
	}
	r.netSetup = ns

	// 1b. Монитор LAN-интерфейсов: периодически поднимает упавшие.
	//     Wi-Fi-чипы на Orange Pi часто сбрасывают AP при перегреве —
	//     без монитора enter-box не заметит этого.
	ifaces := append([]string{r.cfg.LAN.Interface}, r.cfg.LAN.ExtraInterfaces...)
	if len(ifaces) > 0 {
		mon := netcfg.NewIfaceMonitor(r.logger, ifaces, 30*time.Second)
		go mon.Run(ctx)
	}

	// 2. AWG userspace.
	r.awg = netcfg.NewAmneziaManager(r.logger)
	if err := r.bringUpAWG(ctx, r.pool); err != nil {
		return err
	}

	// 3. sing-box.
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: *r.opts,
	})
	if err != nil {
		return fmt.Errorf("box.New: %w", err)
	}
	if err := instance.Start(); err != nil {
		return fmt.Errorf("box.Start: %w", err)
	}
	r.instance = instance
	r.logger.Info("sing-box started")

	// 4. DNS-сервер (после sing-box, чтобы правила маршрутизации работали).
	if r.cfg.DNS.Serve {
		srv, err := enterdns.New(r.logger, r.cfg)
		if err != nil {
			return fmt.Errorf("dns init: %w", err)
		}
		if err := srv.Start(); err != nil {
			return fmt.Errorf("dns start: %w", err)
		}
		r.dnsSrv = srv
	}

	// 5. DHCP.
	if r.cfg.LAN.DHCP.Enabled {
		srv, err := dhcp.New(r.logger, r.cfg)
		if err != nil {
			return fmt.Errorf("dhcp init: %w", err)
		}
		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("dhcp start: %w", err)
		}
		r.dhcpSrv = srv
	}

	return nil
}

// Reload перезапускает только sing-box (при обновлении подписок).
func (r *Runner) Reload(ctx context.Context, pool *parser.Pool, opts *option.Options) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.bringUpAWG(ctx, pool); err != nil {
		return fmt.Errorf("bringUpAWG on reload: %w", err)
	}
	if r.instance != nil {
		_ = r.instance.Close()
		r.instance = nil
	}
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: *opts,
	})
	if err != nil {
		return fmt.Errorf("box.New (reload): %w", err)
	}
	if err := instance.Start(); err != nil {
		return fmt.Errorf("box.Start (reload): %w", err)
	}
	r.instance = instance
	r.pool = pool
	r.opts = opts
	r.logger.Info("sing-box reloaded", "endpoints", pool.Len())
	return nil
}

func (r *Runner) bringUpAWG(ctx context.Context, pool *parser.Pool) error {
	for _, ep := range pool.All() {
		if ep.Kind != parser.KindAmnezia {
			continue
		}
		iface, err := r.awg.Up(ctx, ep, r.cfg.WAN.Name)
		if err != nil {
			return fmt.Errorf("awg up %s: %w", ep.Tag, err)
		}
		r.logger.Debug("awg iface ready", "iface", iface, "tag", ep.Tag)
	}
	return nil
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dhcpSrv != nil {
		r.dhcpSrv.Stop()
	}
	if r.dnsSrv != nil {
		r.dnsSrv.Stop()
	}
	if r.instance != nil {
		_ = r.instance.Close()
		r.instance = nil
	}
	if r.awg != nil {
		r.awg.Down()
	}
	if r.netSetup != nil {
		_ = r.netSetup.Cleanup()
	}
}