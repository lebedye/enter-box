package singbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"

	"enter-box/internal/config"
	"enter-box/internal/dhcp"
	enterdns "enter-box/internal/dns"
	"enter-box/internal/netcfg"
	"enter-box/internal/parser"
	"enter-box/internal/proxyapi"
)

// Runner — жизненный цикл демона: сеть → AWG → sing-box → DNS → DHCP.
type Runner struct {
	logger *slog.Logger
	cfg    *config.Config

	mu sync.Mutex
	// Сигнал ускоряет замеры после аварийного переключения, не создавая обходов параллельно.
	speedWake chan struct{}
	// Общий лимит защищает VPN от цикла перезапусков при нестабильном модеме.
	lastVPNRestart time.Time
	generation     uint64 // защищён mu; инвалидирует результаты старых сетевых проб

	// Пассивный сигнал сетевой ошибки из логов sing-box. Значение, а не
	// указатель: адрес стабилен и передаётся в box.Options.PlatformLogWriter.
	failureSignal passiveFailure

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
		speedWake: make(chan struct{}, 1),
		logger:    logger,
		cfg:       cfg,
		pool:      pool,
		opts:      opts,
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
	// В библиотечном режиме sing-box 1.14 не регистрирует протоколы сам.
	// include.Context добавляет те же реестры, что использует официальный CLI.
	boxContext := include.Context(ctx)
	instance, err := box.New(box.Options{
		Context:           boxContext,
		Options:           *r.opts,
		PlatformLogWriter: &r.failureSignal,
	})
	if err != nil {
		return fmt.Errorf("box.New: %w", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		return fmt.Errorf("box.Start: %w", err)
	}
	r.instance = instance
	r.generation++
	// Создаём общую Linux-блокировку до первой команды обычного SSH-пользователя.
	unlock, err := proxyapi.New().LockControl()
	if err != nil {
		return err
	}
	unlock()
	r.logger.Info("sing-box started")
	// Контроль доступности VPN сохраняет прямой интернет при аварии
	// и автоматически возвращает клиентов в туннель после восстановления.
	go r.monitorVPNFallback(ctx)
	// Отдельные замеры скорости не переключают пользовательский трафик:
	// быстрый failover восстанавливает связь, затем рейтинг уточняет лучший узел.
	go r.monitorVPNSpeed(ctx)

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
	unlock, err := proxyapi.New().LockControl()
	if err != nil {
		return err
	}
	defer unlock()
	r.generation++

	if err := r.bringUpAWG(ctx, pool); err != nil {
		return fmt.Errorf("bringUpAWG on reload: %w", err)
	}
	if r.instance != nil {
		_ = r.instance.Close()
		r.instance = nil
	}
	boxContext := include.Context(ctx)
	instance, err := box.New(box.Options{
		Context:           boxContext,
		Options:           *opts,
		PlatformLogWriter: &r.failureSignal,
	})
	if err != nil {
		return fmt.Errorf("box.New (reload): %w", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
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

// RestartVPN переподнимает туннели, сохраняя службу, LAN, DNS и DHCP.
// Селекторы восстанавливаются из постоянного кэша sing-box, поэтому ручной
// выбор узла не теряется. Перезагрузка подписки использует тот же mutex.
func (r *Runner) RestartVPN(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := proxyapi.New().LockControl()
	if err != nil {
		return err
	}
	defer unlock()
	return r.restartVPNLocked(ctx)
}

// Внутренний вариант позволяет атомарно проверить актуальность выбора
// и переподключиться, удерживая один mutex вместе с Reload.
func (r *Runner) restartVPNLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.lastVPNRestart.IsZero() && time.Since(r.lastVPNRestart) < time.Minute {
		return fmt.Errorf("повторное переподключение VPN разрешено через минуту")
	}
	// Не закрываем VPN из-за исчезнувшего USB-интерфейса: сначала
	// подтверждаем возвращение адреса и маршрута физического WAN.
	if err := netcfg.WaitWANReady(ctx, r.logger, r.cfg.WAN.Name, 5*time.Second); err != nil {
		return fmt.Errorf("WAN не готов к переподключению VPN: %w", err)
	}
	r.lastVPNRestart = time.Now()
	r.generation++
	r.logger.Warn("переподключаем VPN; SSH, LAN, DNS и DHCP остаются запущены")
	if r.instance != nil {
		_ = r.instance.Close()
		r.instance = nil
	}
	// Up идемпотентен: сначала закрываем AWG, чтобы обновить UDP-сокеты
	// и маршрут сервера после смены WAN-адреса или переподключения модема.
	if r.awg != nil {
		r.awg.Down()
	}
	// Down освобождает и обнуляет внутренние карты менеджера. Для нового
	// подключения нужен новый менеджер, иначе Up запишет в nil-map.
	r.awg = netcfg.NewAmneziaManager(r.logger)
	if err := r.bringUpAWG(ctx, r.pool); err != nil {
		return err
	}
	instance, err := box.New(box.Options{
		Context:           include.Context(ctx),
		Options:           *r.opts,
		PlatformLogWriter: &r.failureSignal,
	})
	if err != nil {
		return fmt.Errorf("VPN init: %w", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		return fmt.Errorf("VPN start: %w", err)
	}
	r.instance = instance
	r.logger.Info("VPN переподключён")
	return nil
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Дожидаемся завершения уже принятого ручного выбора до закрытия кэша.
	if unlock, err := proxyapi.New().LockControl(); err == nil {
		defer unlock()
	}

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
