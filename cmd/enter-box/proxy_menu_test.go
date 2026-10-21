package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"enter-box/internal/proxyapi"
)

// fakeClashAPI поднимает httptest-сервер, имитирующий Clash API sing-box
// с двумя узлами (alpha, beta) и селектором "proxy".
// Возвращает клиент proxyapi, указывающий на сервер, и указатель на
// счётчик PUT-запросов (переключений), чтобы проверять побочные эффекты.
func fakeClashAPI(t *testing.T) (*proxyapi.Client, func() (int, string)) {
	t.Helper()

	var mu sync.Mutex
	putCount := 0
	lastSwitch := ""

	mux := http.NewServeMux()

	// /proxies — снапшот всех узлов.
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		alive := true
		resp := map[string]any{
			"proxies": map[string]any{
				"proxy": map[string]any{
					"name": "proxy",
					"type": "Selector",
					"now":  "proxy-auto",
					"all":  []string{"proxy-auto", "alpha", "beta", "direct"},
				},
				"proxy-auto": map[string]any{
					"name": "proxy-auto",
					"type": "URLTest",
					"now":  "alpha",
					"all":  []string{"alpha", "beta"},
				},
				"alpha": map[string]any{
					"name":  "alpha",
					"type":  "VLESS",
					"alive": alive,
					"history": []map[string]any{
						{"delay": 10},
					},
				},
				"beta": map[string]any{
					"name":  "beta",
					"type":  "VLESS",
					"alive": alive,
					"history": []map[string]any{
						{"delay": 10},
					},
				},
				"direct": map[string]any{
					"name": "direct",
					"type": "Direct",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /proxies/{tag}/delay — тест задержки узла.
	mux.HandleFunc("/proxies/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/delay") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"delay": 10})
			return
		}
		// PUT /proxies/proxy — переключение селектора.
		if r.Method == http.MethodPut {
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			putCount++
			lastSwitch = body.Name
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := proxyapi.New()
	client.Base = srv.URL
	client.HTTP = srv.Client()

	// Читаем состояние под тем же mutex, что использует HTTP-обработчик.
	return client, func() (int, string) {
		mu.Lock()
		defer mu.Unlock()
		return putCount, lastSwitch
	}
}

// TestProxyMenu_SelectBeta проверяет, что ввод "2\nq\n" выбирает второй
// узел (beta) и возвращает код 0.
func TestProxyMenu_SelectBeta(t *testing.T) {
	client, state := fakeClashAPI(t)

	var out bytes.Buffer
	code := proxyMenu(client, strings.NewReader("2\nq\n"), &out)
	if code != 0 {
		t.Fatalf("proxyMenu вернул код %d, ожидался 0; вывод:\n%s", code, out.String())
	}
	count, tag := state()
	if count != 1 || tag != "beta" {
		t.Fatalf("ожидался выбор beta ровно один раз: count=%d tag=%q", count, tag)
	}
}

// TestProxyMenu_InvalidThenQuit проверяет, что невалидный номер (99)
// не вызывает PUT, а последующий "q" корректно завершает меню с кодом 0.
func TestProxyMenu_InvalidThenQuit(t *testing.T) {
	client, state := fakeClashAPI(t)

	var out bytes.Buffer
	code := proxyMenu(client, strings.NewReader("99\nq\n"), &out)
	if code != 0 {
		t.Fatalf("proxyMenu вернул код %d, ожидался 0; вывод:\n%s", code, out.String())
	}
	count, _ := state()
	if count != 0 {
		t.Fatalf("невалидный номер не должен вызывать PUT, получено %d", count)
	}
}
