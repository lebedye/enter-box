package singbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"enter-box/internal/proxyapi"
)

// Результат медленной проверки не должен отменять ручной выбор или
// действовать на список серверов, уже обновлённый подпиской.
func TestControlIgnoresStaleResults(t *testing.T) {
	for _, tc := range []struct{ name, outer string; observed, actual uint64 }{
		{"operator selected another server", "manual-new", 1, 1},
		{"subscription reloaded", "proxy-auto", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var puts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPut { puts.Add(1); w.WriteHeader(204); return }
				now := tc.outer
				if req.URL.Path == "/proxies/proxy-auto" { now = "old-server" }
				json.NewEncoder(w).Encode(map[string]string{"now": now})
			}))
			defer srv.Close()
			client := proxyapi.New(); client.Base = srv.URL; client.HTTP = srv.Client()
			runner := &Runner{generation: tc.actual}
			if runner.switchAutoIfCurrent(client, tc.observed, "old-server", "direct") {
				t.Fatal("устаревший отказ не должен менять текущий выход")
			}
			// Нет cfg/instance: ошибочный запуск восстановления здесь вызвал бы
			// панику. Guard обязан завершиться до любых действий с VPN.
			runner.restartIfCurrent(context.Background(), client, tc.observed, "proxy-auto", "old-server")
			if puts.Load() != 0 || !runner.lastVPNRestart.IsZero() {
				t.Fatal("устаревший результат запустил переключение или переподключение")
			}
		})
	}
}
