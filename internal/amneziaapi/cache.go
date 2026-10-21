package amneziaapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// cacheFreshLifetime — сколько готовый конфиг считается свежим.
	// В течение этого времени повторный запрос к шлюзу не выполняется.
	cacheFreshLifetime = 24 * time.Hour
	// resolveTimeout ограничивает сетевой обмен с Amnezia, чтобы
	// зависший шлюз не блокировал запуск приложения.
	resolveTimeout = 90 * time.Second
)

// cacheEntry — формат файла кэша. Хранится только готовый vpn://
// конфиг и время его получения; исходный Premium-ключ не сохраняется.
type cacheEntry struct {
	ResolvedAt time.Time `json:"resolved_at"`
	Config     string    `json:"config"`
}

// ResolveCached возвращает готовый AmneziaWG-конфиг для Premium-ключа,
// используя локальный кэш. Свежий кэш отдаётся без обращения к сети.
// При ошибке шлюза допускается использование устаревшего, но валидного
// кэша. Сырые ключи и конфиги никогда не попадают в логи.
func ResolveCached(ctx context.Context, raw string) (string, error) {
	return resolveCached(ctx, raw, Resolve, time.Now)
}

// resolveCached — внутренняя реализация с внедряемыми зависимостями,
// что позволяет тестировать кэш полностью офлайн.
func resolveCached(
	ctx context.Context,
	raw string,
	resolve func(context.Context, string) (string, error),
	now func() time.Time,
) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		// Без каталога кэша работаем как обычный Resolve.
		return resolveWithTimeout(ctx, raw, resolve)
	}
	path := filepath.Join(dir, cacheFileName(raw))

	entry, readErr := readCacheEntry(path)
	if readErr == nil && entry != nil && now().Sub(entry.ResolvedAt) < cacheFreshLifetime {
		// Свежий кэш: сеть не нужна.
		return entry.Config, nil
	}

	resolved, resolveErr := resolveWithTimeout(ctx, raw, resolve)
	if resolveErr == nil {
		// Ответ шлюза принимается только после проверки формата:
		// невалидный конфиг нельзя ни кэшировать, ни возвращать.
		if !isReadyConfig(resolved) {
			return "", fmt.Errorf("amnezia: шлюз вернул невалидный конфиг")
		}
		if err := writeCacheEntry(dir, path, cacheEntry{
			ResolvedAt: now(),
			Config:     resolved,
		}); err != nil {
			// Ошибка записи кэша не должна ломать основной сценарий.
			slog.Warn("amnezia: не удалось сохранить кэш конфига", "error", err)
		}
		return resolved, nil
	}

	// Отмена или истечение контекста — это не сбой шлюза:
	// устаревший кэш здесь не подставляется, ошибка возвращается как есть.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	// Шлюз недоступен: пробуем устаревший, но валидный кэш.
	if readErr == nil && entry != nil {
		slog.Warn("amnezia: шлюз недоступен, используется устаревший кэш",
			"age", now().Sub(entry.ResolvedAt).String())
		return entry.Config, nil
	}
	return "", resolveErr
}

// resolveWithTimeout вызывает резолвер с ограничением по времени.
func resolveWithTimeout(
	ctx context.Context,
	raw string,
	resolve func(context.Context, string) (string, error),
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	return resolve(ctx, raw)
}

// cacheDir возвращает защищённый каталог кэша.
// Приоритет: AMNEZIA_CACHE_DIR, затем /var/lib/enter-box/amnezia для root,
// затем пользовательский кэш для обычных пользователей.
func cacheDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("AMNEZIA_CACHE_DIR")); override != "" {
		return override, nil
	}
	if os.Geteuid() == 0 {
		return "/var/lib/enter-box/amnezia", nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("определить каталог кэша: %w", err)
	}
	return filepath.Join(base, "enter-box", "amnezia"), nil
}

// cacheFileName — имя файла кэша: sha256 от исходного Premium-ключа.
// Так имя не раскрывает содержимое ключа.
func cacheFileName(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:]) + ".json"
}

// readCacheEntry читает и валидирует файл кэша. Повреждённый или
// невалидный кэш игнорируется (возвращается nil без ошибки).
func readCacheEntry(path string) (*cacheEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, nil
	}
	if entry.ResolvedAt.IsZero() || !isReadyConfig(entry.Config) {
		return nil, nil
	}
	return &entry, nil
}

// writeCacheEntry атомарно записывает кэш: временный файл 0600 в
// каталоге 0700, затем rename. Так читатель никогда не увидит
// частично записанный файл.
func writeCacheEntry(dir, path string, entry cacheEntry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("создать каталог кэша: %w", err)
	}
	// На случай, если каталог уже существовал с более широкими правами.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("выставить права каталога кэша: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".amnezia-*.tmp")
	if err != nil {
		return fmt.Errorf("создать временный файл кэша: %w", err)
	}
	tmpName := tmp.Name()
	// При любой ошибке удаляем временный файл.
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("выставить права файла кэша: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("записать файл кэша: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("синхронизировать файл кэша: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрыть файл кэша: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("переименовать файл кэша: %w", err)
	}
	return nil
}

// isReadyConfig проверяет, что строка — готовый (не bootstrap)
// vpn:// конфиг, содержащий контейнеры AmneziaWG.
func isReadyConfig(raw string) bool {
	if !strings.HasPrefix(strings.TrimSpace(raw), "vpn://") {
		return false
	}
	// Bootstrap-ключ не является готовым конфигом.
	if IsBootstrapVPN(raw) {
		return false
	}
	var doc struct {
		Containers []json.RawMessage `json:"containers"`
	}
	if err := decodeVPNJSON(raw, &doc); err != nil {
		return false
	}
	return len(doc.Containers) > 0
}
