package singbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enter-box/internal/proxyapi"
)

// newFakeClashClient поднимает httptest-сервер, имитирующий Clash API.
// nodes — карта тег → функция-обработчик задержки (возвращает delay в мс
// или блокируется до отмены контекста запроса).
// selectorAll — список тегов в селекторе "proxy".
func newFakeClashClient(t *testing.T, selectorAll []string, delayFn func(tag string, r *http.Request) int) *proxyapi.Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		proxies := map[string]any{
			"proxy": map[string]any{
				"name": "proxy",
				"type": "Selector",
				"now":  "proxy-auto",
				"all":  selectorAll,
			},
			"proxy-auto": map[string]any{
				"name": "proxy-auto",
				"type": "URLTest",
				"now":  "",
				"all":  selectorAll,
			},
			"direct": map[string]any{
				"name": "direct",
				"type": "Direct",
			},
		}
		for _, tag := range selectorAll {
			if tag == "proxy-auto" || tag == "direct" {
				continue
			}
			proxies[tag] = map[string]any{
				"name": tag,
				"type": "VLESS",
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
	})

	mux.HandleFunc("/proxies/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasSuffix(path, "/delay") {
			http.NotFound(w, r)
			return
		}
		// Извлекаем тег из /proxies/{tag}/delay.
		trimmed := strings.TrimPrefix(path, "/proxies/")
		tag := strings.TrimSuffix(trimmed, "/delay")

		delay := delayFn(tag, r)
		if delay <= 0 {
			// Блокируемся до отмены контекста запроса.
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"delay": delay})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := proxyapi.New()
	client.Base = srv.URL
	client.HTTP = srv.Client()
	return client
}

// TestFindAliveNode_PrefersLastGood проверяет, что при наличии preferred
// (lastGood) и другого живого узла с меньшим пингом findAliveNode возвращает
// именно preferred — lastGood не должен «переезжать» на более быстрый узел.
func TestFindAliveNode_PrefersLastGood(t *testing.T) {
	// alpha — preferred, отвечает 50ms; beta — отвечает немедленно (10ms).
	client := newFakeClashClient(t,
		[]string{"proxy-auto", "alpha", "beta", "direct"},
		func(tag string, r *http.Request) int {
			switch tag {
			case "alpha":
				time.Sleep(50 * time.Millisecond)
				return 50
			case "beta":
				return 10
			}
			return 0
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	alive, tag := findAliveNode(ctx, client, "alpha")
	if !alive {
		t.Fatalf("ожидался живой узел, получено alive=false")
	}
	if tag != "alpha" {
		t.Fatalf("ожидался preferred=alpha, получено %q", tag)
	}
}

// TestFindAliveNode_DoesNotWaitAllProbes проверяет, что findAliveNode
// возвращает первый живой узел, не дожидаясь запуска/завершения проб
// остальных узлов. 20 узлов: первый отвечает сразу, остальные висят до
// отмены контекста запроса. При корректной реализации функция вернётся
// быстро (в пределах 3s), а не зависнет на запуске всех 20 проб.
func TestFindAliveNode_DoesNotWaitAllProbes(t *testing.T) {
	tags := []string{"proxy-auto", "node01"}
	for i := 2; i <= 20; i++ {
		tags = append(tags, "node"+twoDigits(i))
	}
	tags = append(tags, "direct")

	var mu sync.Mutex
	started := 0

	client := newFakeClashClient(t, tags, func(tag string, r *http.Request) int {
		mu.Lock()
		started++
		mu.Unlock()
		if tag == "node01" {
			return 10
		}
		// Остальные висят до отмены контекста запроса.
		return 0
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	alive, tag := findAliveNode(ctx, client, "")
	elapsed := time.Since(start)

	if !alive {
		t.Fatalf("ожидался живой узел, получено alive=false")
	}
	if tag != "node01" {
		t.Fatalf("ожидался node01, получено %q", tag)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("findAliveNode не вернулся быстро: %v (запущено проб: %d)", elapsed, started)
	}
}

// twoDigits форматирует число 1..99 в двузначную строку с ведущим нулём.
func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
