package proxyapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSnapshotFiltersServiceProxies — регрессионный тест на реальный баг:
// фильтрация по Type "Direct" скрывает AWG-узлы (они тоже Direct),
// а служебные GLOBAL (Fallback) и block (Reject) попадают в вывод.
//
// Ожидается ровно один узел awg-test: Direct, alive=true, delay=42ms.
func TestSnapshotFiltersServiceProxies(t *testing.T) {
	proxies := map[string]any{
		"proxy": map[string]any{
			"name": "proxy",
			"type": "Selector",
			"now":  "proxy-auto",
			"all":  []string{"proxy-auto", "awg-test", "direct"},
		},
		"proxy-auto": map[string]any{
			"name": "proxy-auto",
			"type": "URLTest",
			"now":  "awg-test",
			"all":  []string{"awg-test"},
		},
		"awg-test": map[string]any{
			"name":  "awg-test",
			"type":  "Direct",
			"alive": true,
			"history": []map[string]any{
				{"time": "2026-10-20T00:00:00Z", "delay": 42},
			},
		},
		"direct": map[string]any{
			"name": "direct",
			"type": "Direct",
		},
		"GLOBAL": map[string]any{
			"name": "GLOBAL",
			"type": "Fallback",
			"all":  []string{"proxy", "direct"},
		},
		"block": map[string]any{
			"name": "block",
			"type": "Reject",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
	})
	// /proxies/proxy может понадобиться клиенту; отдаём тот же узел.
	mux.HandleFunc("/proxies/proxy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proxies["proxy"])
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.Base = srv.URL

	nodes, err := c.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot вернул ошибку: %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("ожидался ровно один узел awg-test, получено %d: %+v", len(nodes), nodes)
	}

	got := nodes[0]
	if got.Tag != "awg-test" {
		t.Errorf("Tag: ожидался awg-test, получен %q", got.Tag)
	}
	if got.Type != "Direct" {
		t.Errorf("Type: ожидался Direct, получен %q", got.Type)
	}
	if got.Alive != "ok" {
		t.Errorf("Alive: ожидался ok, получен %q", got.Alive)
	}
	if got.Delay != "42ms" {
		t.Errorf("Delay: ожидался 42ms, получен %q", got.Delay)
	}
}

// TestSnapshotMissingSelector — если селектор "proxy" отсутствует,
// Snapshot должен вернуть понятную ошибку, а не пустой список.
func TestSnapshotMissingSelector(t *testing.T) {
	proxies := map[string]any{
		"awg-test": map[string]any{
			"name": "awg-test",
			"type": "Direct",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.Base = srv.URL

	nodes, err := c.Snapshot()
	if err == nil {
		t.Fatalf("ожидалась ошибка об отсутствии селектора, получено %d узлов: %+v", len(nodes), nodes)
	}
}

// TestSnapshotMissingNode — если селектор ссылается на узел, которого нет
// в /proxies, Snapshot должен вернуть понятную ошибку, а не молча потерять узел.
func TestSnapshotMissingNode(t *testing.T) {
	proxies := map[string]any{
		"proxy": map[string]any{
			"name": "proxy",
			"type": "Selector",
			"now":  "awg-test",
			"all":  []string{"proxy-auto", "awg-test", "direct"},
		},
		"proxy-auto": map[string]any{
			"name": "proxy-auto",
			"type": "URLTest",
			"now":  "awg-test",
			"all":  []string{"awg-test"},
		},
		"direct": map[string]any{
			"name": "direct",
			"type": "Direct",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.Base = srv.URL

	nodes, err := c.Snapshot()
	if err == nil {
		t.Fatalf("ожидалась ошибка об отсутствующем узле, получено %d узлов: %+v", len(nodes), nodes)
	}
}
