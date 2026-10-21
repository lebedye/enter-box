package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errCacheGeneric — общая ошибка кэша без деталей (URL, путей, userinfo).
var errCacheGeneric = errors.New("subscription cache error")

// cacheDir возвращает каталог для дискового кэша подписок.
// Приоритет: ENTER_BOX_SUBS_CACHE_DIR (для тестов), иначе
// os.UserCacheDir()/enter-box/subscriptions.
func cacheDir() (string, error) {
	if dir := os.Getenv("ENTER_BOX_SUBS_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", errCacheGeneric
	}
	return filepath.Join(base, "enter-box", "subscriptions"), nil
}

// cacheFileName — имя файла кэша: sha256(URL) в hex.
func cacheFileName(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// readCache читает последний валидный сырой ответ подписки.
// Возвращает (nil, nil), если кэша нет.
func readCache(url string) ([]byte, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, cacheFileName(url))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errCacheGeneric
	}
	return data, nil
}

// writeCache атомарно сохраняет сырой ответ подписки:
// каталог 0700, файл 0600, запись во временный файл + rename.
func writeCache(url string, data []byte) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errCacheGeneric
	}
	// На случай, если каталог уже существовал с другими правами.
	if err := os.Chmod(dir, 0o700); err != nil {
		return errCacheGeneric
	}

	tmp, err := os.CreateTemp(dir, ".subs-*.tmp")
	if err != nil {
		return errCacheGeneric
	}
	tmpName := tmp.Name()
	// Убираем временный файл при любой ошибке ниже.
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return errCacheGeneric
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return errCacheGeneric
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return errCacheGeneric
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return errCacheGeneric
	}

	final := filepath.Join(dir, cacheFileName(url))
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return errCacheGeneric
	}
	return nil
}

// cacheErrorf — обёртка для явного сообщения об ошибке кэша без утечек.
func cacheErrorf() error {
	return fmt.Errorf("subscription cache: %w", errCacheGeneric)
}
