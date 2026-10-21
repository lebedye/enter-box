package singbox

import (
	"testing"
	"time"
)

// Короткая потеря связи не должна менять сервер: считаем время, а не число тиков.
func TestFailureWindowWaitsTenSeconds(t *testing.T) {
	var window failureWindow
	start := time.Unix(100, 0)
	for _, offset := range []time.Duration{0, time.Second, 9 * time.Second} {
		if window.Observe(start.Add(offset), false) {
			t.Fatal("переключение до десяти секунд непрерывных ошибок")
		}
	}
	if !window.Observe(start.Add(10*time.Second), false) {
		t.Fatal("длительный отказ должен запускать проверку резервных узлов")
	}
}

// Успешная проверка полностью сбрасывает окно: отдельные зависания не суммируются.
func TestFailureWindowResetsAfterRecovery(t *testing.T) {
	var window failureWindow
	start := time.Unix(100, 0)
	window.Observe(start, false)
	window.Observe(start.Add(9*time.Second), true)
	if window.Observe(start.Add(11*time.Second), false) {
		t.Fatal("ошибки, разделённые успехом, не образуют непрерывный отказ")
	}
	if window.Observe(start.Add(20*time.Second), false) {
		t.Fatal("после восстановления нужно новое полное десятисекундное окно")
	}
	if !window.Observe(start.Add(21*time.Second), false) {
		t.Fatal("новое окно отказа должно завершиться спустя десять секунд")
	}
}
