package netcfg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	resolvedDropInDir  = "/etc/systemd/resolved.conf.d"
	resolvedDropInFile = resolvedDropInDir + "/enter-box-dns.conf"

	resolvConfPath    = "/etc/resolv.conf"
	resolvedStubPath  = "/run/systemd/resolve/stub-resolv.conf"
	resolvedRealPath  = "/run/systemd/resolve/resolv.conf"
)

// DisableDNSStubListener освобождает порт 53 для встроенного DNS-сервера
// enter-box, НЕ ломая резолвинг на самой плате.
//
// Что делает:
//  1. Создаёт drop-in /etc/systemd/resolved.conf.d/enter-box-dns.conf
//     с DNSStubListener=no. systemd-resolved перестаёт слушать 127.0.0.53:53.
//  2. Перезапускает systemd-resolved.
//  3. Если /etc/resolv.conf был симлинком на stub-resolv.conf (там только
//     127.0.0.53), перевешивает его на resolv.conf (реальные upstreams).
//     После этого хост резолвит имена напрямую через 77.88.8.8 / 1.1.1.1,
//     даже если enter-box ещё не стартовал.
//
// Если systemd-resolved не активен — ничего не делает.
func DisableDNSStubListener() error {
	if !isServiceActive("systemd-resolved.service") {
		return nil
	}

	if err := os.MkdirAll(resolvedDropInDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", resolvedDropInDir, err)
	}

	content := `# Автоматически создано enter-box.
#
# Освобождает порт 53 для встроенного DNS-форвардера enter-box.
# systemd-resolved продолжает работать, но больше не занимает 0.0.0.0:53.
# Хост резолвит имена через реальные upstreams из resolv.conf.

[Resolve]
DNSStubListener=no
`
	if err := os.WriteFile(resolvedDropInFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", resolvedDropInFile, err)
	}

	if err := exec.Command("systemctl", "restart", "systemd-resolved").Run(); err != nil {
		return fmt.Errorf("restart systemd-resolved: %w", err)
	}

	if err := fixResolvConfSymlink(); err != nil {
		// Не критично: главное — порт освобождён. Лог пойдёт в stderr.
		return fmt.Errorf("resolv.conf: %w", err)
	}
	return nil
}

// fixResolvConfSymlink меняет /etc/resolv.conf с stub на real.
// Если это не симлинк на stub — не трогает.
func fixResolvConfSymlink() error {
	target, err := os.Readlink(resolvConfPath)
	if err != nil {
		// Не симлинк (обычный файл) — не наш случай.
		return nil
	}
	if target != resolvedStubPath {
		return nil
	}
	if err := os.Remove(resolvConfPath); err != nil {
		return fmt.Errorf("remove %s: %w", resolvConfPath, err)
	}
	if err := os.Symlink(resolvedRealPath, resolvConfPath); err != nil {
		return fmt.Errorf("symlink -> %s: %w", resolvedRealPath, err)
	}
	return nil
}

// RemoveDNSStubListenerFix откатывает правки (используется при uninstall).
func RemoveDNSStubListenerFix() error {
	if err := os.Remove(resolvedDropInFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", resolvedDropInFile, err)
	}
	// Симлинк возвращаем на stub — как было по умолчанию.
	if target, err := os.Readlink(resolvConfPath); err == nil && target == resolvedRealPath {
		_ = os.Remove(resolvConfPath)
		_ = os.Symlink(resolvedStubPath, resolvConfPath)
	}
	_ = exec.Command("systemctl", "restart", "systemd-resolved").Run()
	return nil
}

func isServiceActive(unit string) bool {
	out, err := exec.Command("systemctl", "is-active", unit).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}