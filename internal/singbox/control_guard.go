package singbox

import (
	"context"

	"enter-box/internal/proxyapi"
)

// Номер поколения связывает сетевые пробы с экземпляром VPN. Результаты
// старых проб нельзя применять к обновлённой подписке или новому ручному выбору.
func (r *Runner) generationNow() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

// Вызывается под r.mu: Reload/Restart не могут подменить API между
// проверкой поколения и действием. Ручной режим дополнительно читается из API.
func (r *Runner) selectionMatchesLocked(c *proxyapi.Client, generation uint64, outer, current string) bool {
	if r.generation != generation {
		return false
	}
	active, err := c.ActiveNode()
	if outer == "" {
		// Восстановление API разрешено только пока API действительно недоступен.
		return err != nil
	}
	if err != nil || active != outer {
		return false
	}
	if outer == "proxy-auto" {
		selected, err := c.AutoNode()
		return err == nil && selected == current
	}
	return active == current
}

// Автоматика меняет только внутренний selector. Даже одновременный ручной
// выбор внешнего selector не будет отменён автоматическим переключением.
func (r *Runner) switchAutoIfCurrent(c *proxyapi.Client, generation uint64, current, target string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := c.LockControl()
	if err != nil { return false }
	defer unlock()
	if !r.selectionMatchesLocked(c, generation, "proxy-auto", current) {
		return false
	}
	if err := c.SwitchGroup("proxy-auto", target); err != nil {
		r.logger.Warn("не удалось изменить автоматический выход", "err", err)
		return false
	}
	return true
}

// Переподключение по результатам проб разрешено только для всё ещё
// актуального выбора. Явная команда оператора использует RestartVPN напрямую.
func (r *Runner) restartIfCurrent(ctx context.Context, c *proxyapi.Client, generation uint64, outer, current string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock, err := c.LockControl()
	if err != nil { return }
	defer unlock()
	if !r.selectionMatchesLocked(c, generation, outer, current) {
		return
	}
	if err := r.restartVPNLocked(ctx); err != nil {
		r.logger.Debug("переподключение отложено", "err", err)
	}
}
