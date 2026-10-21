package amneziaapi

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeReadyVPN собирает валидный готовый vpn:// конфиг с контейнерами.
// Используется только в тестах, реальные ключи не нужны.
func makeReadyVPN(t *testing.T, marker string) string {
	t.Helper()
	doc := map[string]any{
		"containers": []map[string]any{
			{"container": "amnezia-awg", "marker": marker},
		},
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal ready config: %v", err)
	}
	var compressed bytes.Buffer
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(plain)))
	compressed.Write(size[:])
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(plain); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return "vpn://" + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
}

// withCacheDir подменяет каталог кэша через переменную окружения.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AMNEZIA_CACHE_DIR", dir)
	return dir
}

func TestResolveCachedHitNoCall(t *testing.T) {
	dir := withCacheDir(t)
	raw := "vpn://premium-key-hit"
	ready := makeReadyVPN(t, "hit")

	// Предварительно кладём свежий кэш.
	path := filepath.Join(dir, cacheFileName(raw))
	if err := writeCacheEntry(dir, path, cacheEntry{
		ResolvedAt: time.Now(),
		Config:     ready,
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	called := false
	resolve := func(context.Context, string) (string, error) {
		called = true
		return "", errors.New("не должен вызываться")
	}
	got, err := resolveCached(context.Background(), raw, resolve, time.Now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("резолвер не должен вызываться при свежем кэше")
	}
	if got != ready {
		t.Fatalf("получен не тот конфиг: %q", got)
	}
}

func TestResolveCachedExpiredRefresh(t *testing.T) {
	dir := withCacheDir(t)
	raw := "vpn://premium-key-expired"
	old := makeReadyVPN(t, "old")
	fresh := makeReadyVPN(t, "fresh")

	path := filepath.Join(dir, cacheFileName(raw))
	if err := writeCacheEntry(dir, path, cacheEntry{
		ResolvedAt: time.Now().Add(-48 * time.Hour),
		Config:     old,
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	called := 0
	resolve := func(context.Context, string) (string, error) {
		called++
		return fresh, nil
	}
	got, err := resolveCached(context.Background(), raw, resolve, time.Now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("ожидался один вызов резолвера, получено %d", called)
	}
	if got != fresh {
		t.Fatalf("ожидался обновлённый конфиг, получен %q", got)
	}

	// Обновлённый кэш должен быть свежим.
	entry, err := readCacheEntry(path)
	if err != nil || entry == nil {
		t.Fatalf("кэш не обновился: %v", err)
	}
	if entry.Config != fresh {
		t.Fatalf("в кэше не новый конфиг")
	}
}

func TestResolveCachedStaleFallback(t *testing.T) {
	dir := withCacheDir(t)
	raw := "vpn://premium-key-stale"
	stale := makeReadyVPN(t, "stale")

	path := filepath.Join(dir, cacheFileName(raw))
	if err := writeCacheEntry(dir, path, cacheEntry{
		ResolvedAt: time.Now().Add(-72 * time.Hour),
		Config:     stale,
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	resolve := func(context.Context, string) (string, error) {
		return "", errors.New("шлюз недоступен")
	}
	got, err := resolveCached(context.Background(), raw, resolve, time.Now)
	if err != nil {
		t.Fatalf("ожидался fallback на устаревший кэш, получена ошибка: %v", err)
	}
	if got != stale {
		t.Fatalf("ожидался устаревший конфиг, получен %q", got)
	}
}

func TestResolveCachedCorruptIgnored(t *testing.T) {
	dir := withCacheDir(t)
	raw := "vpn://premium-key-corrupt"
	fresh := makeReadyVPN(t, "fresh")

	path := filepath.Join(dir, cacheFileName(raw))
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}

	called := 0
	resolve := func(context.Context, string) (string, error) {
		called++
		return fresh, nil
	}
	got, err := resolveCached(context.Background(), raw, resolve, time.Now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("повреждённый кэш должен игнорироваться, вызовов: %d", called)
	}
	if got != fresh {
		t.Fatalf("ожидался свежий конфиг, получен %q", got)
	}
}

func TestResolveCachedFileMode(t *testing.T) {
	dir := withCacheDir(t)
	raw := "vpn://premium-key-mode"
	ready := makeReadyVPN(t, "mode")

	resolve := func(context.Context, string) (string, error) {
		return ready, nil
	}
	if _, err := resolveCached(context.Background(), raw, resolve, time.Now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(dir, cacheFileName(raw))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ожидались права 0600, получено %o", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("ожидались права каталога 0700, получено %o", perm)
	}
}
