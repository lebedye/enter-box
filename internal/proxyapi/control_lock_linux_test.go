package proxyapi

import (
	"path/filepath"
	"testing"
	"time"
)

// Два процесса/дескриптора используют одну критическую секцию. Второй
// ручной выбор не должен попасть внутрь незавершённого переподключения.
func TestControlLockSerializesSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.lock")
	unlock, err := lockControlPath(path)
	if err != nil { t.Fatal(err) }
	entered := make(chan error, 1)
	go func() {
		second, err := lockControlPath(path)
		if err == nil { second() }
		entered <- err
	}()
	select {
	case err := <-entered:
		unlock()
		t.Fatalf("вторая операция обошла блокировку: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-entered:
		if err != nil { t.Fatal(err) }
	case <-time.After(3 * time.Second):
		t.Fatal("операция не продолжилась после снятия блокировки")
	}
}
