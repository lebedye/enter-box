package proxyapi

import (
	"os"
	"path/filepath"
	"syscall"
)

// LockControl сериализует команды SSH-клиента с автоматикой и Reload.
// Содержимого и секретов в файле нет: доступ на чтение достаточен для
// Linux flock, поэтому обычному пользователю не нужны sudo и запись в /run.
func (c *Client) LockControl() (func(), error) {
	if c.Base != defaultBase {
		// Клиент другого экземпляра API не должен блокировать местный демон.
		return func() {}, nil
	}
	return lockControlPath("/run/enter-box-control/selection.lock")
}

func lockControlPath(path string) (func(), error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil { return nil, err }
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil { return nil, err }
	if os.Geteuid() == 0 {
		// Устраняем влияние строгого root umask; файл остаётся недоступным
		// для записи другим пользователям и не содержит данных конфигурации.
		if err := os.Chmod(dir, 0755); err != nil { f.Close(); return nil, err }
		if err := f.Chmod(0644); err != nil { f.Close(); return nil, err }
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { f.Close(); return nil, err }
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
