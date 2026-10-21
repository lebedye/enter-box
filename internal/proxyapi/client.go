// Package proxyapi — тонкий клиент Clash API, который публикует sing-box.
//
// Мы используем его для:
//   - отображения активного узла (selector "proxy");
//   - списка всех узлов с их задержками;
//   - форсированного теста задержки (urltest delay);
//   - ручного переключения узла.
//
// По умолчанию sing-box публикует Clash API на 127.0.0.1:9090
// (см. internal/singbox/generate.go → buildExperimental).
package proxyapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const defaultBase = "http://127.0.0.1:9090"

// Client — обёртка над HTTP-клиентом с базовым URL и таймаутом.
type Client struct {
	Base string
	HTTP *http.Client
}

func New() *Client {
	return &Client{
		Base: defaultBase,
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

// Proxy — один узел, как его отдаёт Clash API.
type Proxy struct {
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Alive   *bool         `json:"alive,omitempty"`
	Now     string        `json:"now,omitempty"`
	History []HistoryItem `json:"history,omitempty"`
	All     []string      `json:"all,omitempty"`
}

type HistoryItem struct {
	Time  time.Time `json:"time"`
	Delay int       `json:"delay"`
}

// GetProxy возвращает один узел по тегу.
func (c *Client) GetProxy(tag string) (*Proxy, error) {
	u := fmt.Sprintf("%s/proxies/%s", c.Base, url.PathEscape(tag))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, fmt.Errorf("clash api недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("clash api %d: %s", resp.StatusCode, string(body))
	}
	var p Proxy
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &p, nil
}

// ListProxies возвращает все узлы, отсортированные по имени.
// Отфильтровываем служебные типы (Selector, URLTest, Direct, Block).
func (c *Client) ListProxies() (map[string]Proxy, error) {
	resp, err := c.HTTP.Get(c.Base + "/proxies")
	if err != nil {
		return nil, fmt.Errorf("clash api недоступен: %w", err)
	}
	defer resp.Body.Close()
	var data struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return data.Proxies, nil
}

// ActiveNode возвращает текущий активный узел из selector "proxy".
func (c *Client) ActiveNode() (string, error) {
	p, err := c.GetProxy("proxy")
	if err != nil {
		return "", err
	}
	return p.Now, nil
}

// AutoNode возвращает текущий узел, выбранный селектором "proxy-auto".
func (c *Client) AutoNode() (string, error) {
	p, err := c.GetProxy("proxy-auto")
	if err != nil {
		return "", err
	}
	return p.Now, nil
}

// TestDelay форсированно измеряет задержку узла.
// Для selector проверяется его текущий выход, а не все члены группы.
func (c *Client) TestDelay(tag, testURL string, timeout time.Duration) (int, error) {
	return c.TestDelayContext(context.Background(), tag, testURL, timeout)
}

// TestDelayContext — TestDelay с поддержкой отмены через ctx.
func (c *Client) TestDelayContext(ctx context.Context, tag, testURL string, timeout time.Duration) (int, error) {
	if testURL == "" {
		testURL = "http://cp.cloudflare.com/generate_204"
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	u := fmt.Sprintf("%s/proxies/%s/delay?url=%s&timeout=%d",
		c.Base, url.PathEscape(tag), url.QueryEscape(testURL), timeout.Milliseconds())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("clash api недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("clash api %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Delay int    `json:"delay"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	if data.Error != "" {
		return 0, fmt.Errorf("%s", data.Error)
	}
	return data.Delay, nil
}

// Probe проверяет узел по двум независимым HTTPS-целям.
// Возвращает задержку первого успешного ответа; если обе цели недоступны —
// общую ошибку. Отмена ctx прерывает проверку.
func (c *Client) Probe(ctx context.Context, tag string) (int, error) {
	targets := []string{
		"https://1.1.1.1/cdn-cgi/trace",
		"https://8.8.8.8/resolve?name=example.com&type=A",
	}
	for _, target := range targets {
		delay, err := c.TestDelayContext(ctx, tag, target, 2*time.Second)
		if err == nil && delay > 0 {
			return delay, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, fmt.Errorf("probe %s: все цели недоступны", tag)
}

// Switch переключает selector "proxy" на конкретный узел.
// Чтобы вернуться к автоматике — передай tag = "proxy-auto".
func (c *Client) Switch(tag string) error {
	unlock, err := c.LockControl()
	if err != nil { return err }
	defer unlock()
	return c.SwitchGroup("proxy", tag)
}

// SwitchGroup переключает произвольную selector-группу на узел tag.
// Используется failover'ом для "proxy-auto" (узел или direct).
func (c *Client) SwitchGroup(group, tag string) error {
	body, _ := json.Marshal(map[string]string{"name": tag})
	u := fmt.Sprintf("%s/proxies/%s", c.Base, url.PathEscape(group))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("clash api недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("switch %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// NodeInfo — упрощённая структура для отображения в CLI.
type NodeInfo struct {
	Tag   string
	Type  string
	Alive string // "ok" / "fail" / "?"
	Delay string // "42ms" / "-"
}

// Snapshot возвращает отсортированный список реальных VPN-узлов для отображения.
//
// Узлы берём из списка all селектора "proxy", а не по типу: AWG-узлы тоже
// имеют тип Direct, поэтому фильтрация по типу их скрывала, зато пропускала
// служебные GLOBAL (Fallback) и block (Reject). Служебные теги proxy-auto
// (urltest) и direct исключаем явно.
func (c *Client) Snapshot() ([]NodeInfo, error) {
	proxies, err := c.ListProxies()
	if err != nil {
		return nil, err
	}

	selector, ok := proxies["proxy"]
	if !ok {
		return nil, fmt.Errorf("clash api: селектор \"proxy\" отсутствует")
	}

	var out []NodeInfo
	for _, name := range selector.All {
		// Служебные теги: urltest-группа и прямой выход.
		if name == "proxy-auto" || name == "direct" {
			continue
		}
		p, ok := proxies[name]
		if !ok {
			return nil, fmt.Errorf("clash api: узел %q из селектора \"proxy\" отсутствует в /proxies", name)
		}
		alive := "?"
		if p.Alive != nil {
			if *p.Alive {
				alive = "ok"
			} else {
				alive = "fail"
			}
		}
		delay := "-"
		if len(p.History) > 0 {
			d := p.History[len(p.History)-1].Delay
			if d > 0 {
				delay = fmt.Sprintf("%dms", d)
			}
		}
		out = append(out, NodeInfo{Tag: name, Type: p.Type, Alive: alive, Delay: delay})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out, nil
}
