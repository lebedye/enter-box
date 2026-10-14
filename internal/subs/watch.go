// Package subs — фоновое обновление подписок.
package subs

import (
	"context"
	"log/slog"
	"time"

	"enter-box/internal/config"
	"enter-box/internal/parser"
	"enter-box/internal/singbox"
)

type Watcher struct {
	logger *slog.Logger
	cfg    *config.Config
	pool   *parser.Pool
	runner *singbox.Runner

	tick      time.Duration
	lastFetch map[string]time.Time
}

func New(logger *slog.Logger, cfg *config.Config, pool *parser.Pool, runner *singbox.Runner) *Watcher {
	return &Watcher{
		logger:    logger,
		cfg:       cfg,
		pool:      pool,
		runner:    runner,
		tick:      time.Minute,
		lastFetch: make(map[string]time.Time),
	}
}

func (w *Watcher) Run(ctx context.Context) {
	if len(w.cfg.Subscriptions) == 0 {
		w.logger.Info("subscription watcher: no subscriptions configured")
		return
	}
	w.tickOnce(ctx)
	w.logger.Info("subscription watcher started",
		"count", len(w.cfg.Subscriptions), "tick", w.tick)

	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("subscription watcher stopped")
			return
		case <-t.C:
			w.tickOnce(ctx)
		}
	}
}

func (w *Watcher) tickOnce(ctx context.Context) {
	now := time.Now()
	var due []config.SubscriptionConfig
	for _, sub := range w.cfg.Subscriptions {
		if sub.AutoUpdate <= 0 {
			continue
		}
		if last, ok := w.lastFetch[sub.URL]; ok && now.Sub(last) < sub.AutoUpdate {
			continue
		}
		due = append(due, sub)
	}
	if len(due) == 0 {
		return
	}

	w.logger.Info("refreshing subscriptions", "due", len(due))

	newPool := parser.NewPool()
	if err := newPool.LoadFromConfig(ctx, w.cfg); err != nil {
		w.logger.Warn("subscription refresh failed", "err", err)
		return
	}
	if poolsEqual(w.pool, newPool) {
		for _, sub := range due {
			w.lastFetch[sub.URL] = now
		}
		w.logger.Debug("subscriptions unchanged")
		return
	}

	added, removed := poolDiff(w.pool, newPool)
	w.logger.Info("subscriptions changed",
		"old", w.pool.Len(), "new", newPool.Len(),
		"added", len(added), "removed", len(removed))
	for _, tag := range added {
		w.logger.Info("  + node", "tag", tag)
	}
	for _, tag := range removed {
		w.logger.Info("  - node", "tag", tag)
	}

	opts, err := singbox.Generate(w.cfg, newPool)
	if err != nil {
		w.logger.Error("generate config after refresh", "err", err)
		return
	}
	if err := w.runner.Reload(ctx, newPool, opts); err != nil {
		w.logger.Error("reload failed", "err", err)
		return
	}
	for _, sub := range due {
		w.lastFetch[sub.URL] = now
	}
	w.pool = newPool
}

func poolsEqual(a, b *parser.Pool) bool {
	if a.Len() != b.Len() {
		return false
	}
	seen := make(map[string]struct{}, a.Len())
	for _, ep := range a.All() {
		seen[ep.Raw] = struct{}{}
	}
	for _, ep := range b.All() {
		if _, ok := seen[ep.Raw]; !ok {
			return false
		}
	}
	return true
}

func poolDiff(oldPool, newPool *parser.Pool) (added, removed []string) {
	oldByRaw := make(map[string]string, oldPool.Len())
	for _, ep := range oldPool.All() {
		oldByRaw[ep.Raw] = ep.Tag
	}
	newByRaw := make(map[string]string, newPool.Len())
	for _, ep := range newPool.All() {
		newByRaw[ep.Raw] = ep.Tag
	}
	for raw, tag := range newByRaw {
		if _, ok := oldByRaw[raw]; !ok {
			added = append(added, tag)
		}
	}
	for raw, tag := range oldByRaw {
		if _, ok := newByRaw[raw]; !ok {
			removed = append(removed, tag)
		}
	}
	return
}
