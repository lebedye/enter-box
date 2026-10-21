package singbox

import (
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
)

// passiveFailureDelay — сколько должна непрерывно длиться пассивная серия
// сетевых ошибок sing-box, прежде чем разрешить активную HTTPS-проверку.
// До этого момента VPN считается исправным и сеть не трогаем.
const passiveFailureDelay = 10 * time.Second

// passiveFailure — пассивный сигнал сетевой ошибки из логов sing-box.
//
// Это только подозрение: он не переключает узлы и не перезапускает VPN.
// Реальную недоступность подтверждает активный Probe после passiveFailureDelay.
// Отдельный mutex не пересекается с Runner.mu и не блокирует Reload.
type passiveFailure struct {
	mu         sync.Mutex
	firstError time.Time
}

// ObserveError учитывает сообщение лога в момент now.
// Сетевые ошибки открывают окно подозрения; повторные ошибки не сдвигают
// его начало, чтобы серия не продлевалась бесконечно. Остальные сообщения
// игнорируются. Само сообщение не сохраняется — только время.
func (p *passiveFailure) ObserveError(now time.Time, message string) {
	if !isNetworkFailureMessage(message) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.firstError.IsZero() {
		p.firstError = now
	}
}

// Pending сообщает, что серия сетевых ошибок длится не меньше passiveFailureDelay.
func (p *passiveFailure) Pending(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.firstError.IsZero() {
		return false
	}
	return now.Sub(p.firstError) >= passiveFailureDelay
}

// Clear сбрасывает окно подозрения после успешной проверки или смены выбора.
func (p *passiveFailure) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.firstError = time.Time{}
}

// WriteMessage реализует log.PlatformWriter. sing-box передаёт копию лога,
// stderr при этом не заменяется. Сообщение повторно не логируем.
func (p *passiveFailure) WriteMessage(level log.Level, message string) {
	// В sing-box меньший номер означает более серьёзную ошибку (Panic=0).
	if level > log.LevelError {
		return
	}
	p.ObserveError(time.Now(), message)
}

// isNetworkFailureMessage отбирает сетевые ошибки outbound-соединений.
// Сначала требуется признак outbound/соединения, затем конкретная причина,
// чтобы не реагировать на прочие ошибки уровня error.
func isNetworkFailureMessage(message string) bool {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "outbound") &&
		!strings.Contains(lower, "open connection") &&
		!strings.Contains(lower, "connection:") {
		return false
	}
	reasons := []string{
		"timeout",
		"connection refused",
		"network is unreachable",
		"no route to host",
		"connection reset",
		"handshake",
		"eof",
	}
	for _, reason := range reasons {
		if strings.Contains(lower, reason) {
			return true
		}
	}
	return false
}
