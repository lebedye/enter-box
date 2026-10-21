package parser

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"enter-box/internal/config"
)

// --- ParseLink: hysteria2 / hy2 ---

func TestParseLink_Hysteria2(t *testing.T) {
	// Пароль в userinfo закодирован как %40 — должен раскодироваться в @.
	ep, err := ParseLink("hysteria2://p%40ss@example.com:443?sni=example.com#hy2node")
	if err != nil {
		t.Fatalf("hysteria2 должен парситься: %v", err)
	}
	if string(ep.Kind) != "hysteria2" {
		t.Fatalf("kind: ожидали hysteria2, получили %q", ep.Kind)
	}
	if ep.Server != "example.com" {
		t.Fatalf("server: ожидали example.com, получили %q", ep.Server)
	}
	if ep.Port != 443 {
		t.Fatalf("port: ожидали 443, получили %d", ep.Port)
	}
	if ep.SNI != "example.com" {
		t.Fatalf("sni: ожидали example.com, получили %q", ep.SNI)
	}
	// Пароль должен быть доступен из URI-параметров уже раскодированным.
	if got := ep.Params.Get("password"); got != "p@ss" {
		t.Fatalf("password: ожидали p@ss (раскодированный), получили %q", got)
	}
}

func TestParseLink_Hy2(t *testing.T) {
	// hy2 — алиас hysteria2.
	ep, err := ParseLink("hy2://pass@example.com:8443?sni=example.com#n")
	if err != nil {
		t.Fatalf("hy2 должен парситься: %v", err)
	}
	if string(ep.Kind) != "hysteria2" {
		t.Fatalf("hy2 должен давать kind hysteria2, получили %q", ep.Kind)
	}
	if ep.Port != 8443 {
		t.Fatalf("port: ожидали 8443, получили %d", ep.Port)
	}
}

// Пароли Hysteria2 заменяют UUID: один адрес с разными учётными данными
// должен оставаться двумя независимыми вариантами подключения.
func TestPoolKeepsDifferentHysteriaCredentials(t *testing.T) {
	pool := NewPool()
	for _, raw := range []string{
		"hy2://first@example.com:443?sni=example.com#one",
		"hy2://second@example.com:443?sni=example.com#two",
		"hy2://first@example.com:443?sni=example.com#duplicate-label",
	} {
		ep, err := ParseLink(raw)
		if err != nil { t.Fatal(err) }
		pool.Add(ep)
	}
	if pool.Len() != 2 { t.Fatalf("ожидались два разных подключения, получено %d", pool.Len()) }
}

func TestParseLink_Hysteria2_BadPort(t *testing.T) {
	if _, err := ParseLink("hysteria2://pass@example.com:notaport?sni=example.com"); err == nil {
		t.Fatal("некорректный порт должен возвращать ошибку")
	}
}

func TestParseLink_Hysteria2_InvalidSecurity(t *testing.T) {
	if _, err := ParseLink("hysteria2://pass@example.com:443?security=invalid&sni=example.com"); err == nil {
		t.Fatal("некорректное значение security должно возвращать ошибку")
	}
}

// --- LoadSubscription: кэш и безопасность ошибок ---

func TestLoadSubscription_CacheOn503(t *testing.T) {
	// Изолированный каталог кэша на время теста.
	t.Setenv("ENTER_BOX_SUBS_CACHE_DIR", t.TempDir())

	links := "vless://fakeUUID@example.com:443?security=tls&sni=example.com#node1\n" +
		"vless://fakeUUID@example.com:8443?security=tls&sni=example.com#node2"
	goodBody := base64.StdEncoding.EncodeToString([]byte(links))

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(goodBody))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	sub := config.SubscriptionConfig{URL: srv.URL, Tag: "test"}

	first, err := LoadSubscription(context.Background(), sub)
	if err != nil {
		t.Fatalf("первая загрузка должна быть успешной: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("ожидали 2 узла, получили %d", len(first))
	}

	// Второй запрос получает 503 — должен вернуться последний валидный список из кэша.
	second, err := LoadSubscription(context.Background(), sub)
	if err != nil {
		t.Fatalf("при 503 должен вернуться кэш, а не ошибка: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("из кэша ожидали 2 узла, получили %d", len(second))
	}
}

func TestLoadSubscription_ErrorDoesNotLeakSecrets(t *testing.T) {
	// Кэша нет: свежий каталог.
	t.Setenv("ENTER_BOX_SUBS_CACHE_DIR", t.TempDir())

	// Сервер сразу закрываем: запрос упадёт на транспортном уровне,
	// и стандартная ошибка http.Client содержит полный URL с userinfo и токеном.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	secretToken := "supersecrettoken123"
	userinfo := "user:pass"
	subURL := strings.Replace(baseURL, "http://", "http://"+userinfo+"@", 1) + "/sub?token=" + secretToken

	sub := config.SubscriptionConfig{URL: subURL}
	_, err := LoadSubscription(context.Background(), sub)
	if err == nil {
		t.Fatal("ожидали ошибку при недоступном сервере")
	}
	msg := err.Error()
	if strings.Contains(msg, secretToken) {
		t.Fatalf("ошибка не должна содержать секретный токен: %q", msg)
	}
	if strings.Contains(msg, userinfo) {
		t.Fatalf("ошибка не должна содержать userinfo: %q", msg)
	}
}

func TestLoadSubscription_EmptyDoesNotPoisonCache(t *testing.T) {
	t.Setenv("ENTER_BOX_SUBS_CACHE_DIR", t.TempDir())

	links := "vless://fakeUUID@example.com:443?security=tls&sni=example.com#node1"
	goodBody := base64.StdEncoding.EncodeToString([]byte(links))

	var mode int // 0 = good, 1 = empty, 2 = 503
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case 0:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(goodBody))
		case 1:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	sub := config.SubscriptionConfig{URL: srv.URL}

	// 1. Хорошая подписка — успех и заполнение кэша.
	if _, err := LoadSubscription(context.Background(), sub); err != nil {
		t.Fatalf("хорошая подписка должна загружаться: %v", err)
	}

	// 2. HTTP 200 с пустым телом не заменяет рабочий список пустым:
	// возвращается последний валидный кэш, как при странице обслуживания.
	mode = 1
	if cached, err := LoadSubscription(context.Background(), sub); err != nil || len(cached) != 1 {
		t.Fatal("пустой ответ должен сохранить доступность прежнего списка")
	}

	// 3. Сервер недоступен — должен вернуться кэш с хорошими узлами,
	//    а не пустой список от предыдущего вызова.
	mode = 2
	eps, err := LoadSubscription(context.Background(), sub)
	if err != nil {
		t.Fatalf("при 503 должен вернуться кэш: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("кэш не должен быть отравлен пустой подпиской, ожидали 1 узел, получили %d", len(eps))
	}
}

func TestLoadSubscription_UnsupportedOnly(t *testing.T) {
	t.Setenv("ENTER_BOX_SUBS_CACHE_DIR", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("foo://bar\nbaz://qux"))
	}))
	defer srv.Close()

	sub := config.SubscriptionConfig{URL: srv.URL}
	if _, err := LoadSubscription(context.Background(), sub); err == nil {
		t.Fatal("подписка без поддерживаемых ссылок должна возвращать ошибку")
	}
}
