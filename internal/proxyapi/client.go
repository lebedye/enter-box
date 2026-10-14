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

// AutoNode возвращает текущий узел, выбранный urltest.
func (c *Client) AutoNode() (string, error) {
	p, err := c.GetProxy("proxy-auto")
	if err != nil {
		return "", err
	}
	return p.Now, nil
}

// TestDelay форсированно измеряет задержку узла.
// tag может быть любым узлом, включая "proxy-auto" (тогда прогоняются все).
func (c *Client) TestDelay(tag, testURL string, timeout time.Duration) (int, error) {
	if testURL == "" {
		testURL = "http://cp.cloudflare.com/generate_204"
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	u := fmt.Sprintf("%s/proxies/%s/delay?url=%s&timeout=%d",
		c.Base, url.PathEscape(tag), url.QueryEscape(testURL), timeout.Milliseconds())

	resp, err := c.HTTP.Get(u)
	if err != nil {
		return 0, fmt.Errorf("clash api недоступен: %w", err)
	}
	defer resp.Body.Close()

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

// Switch переключает selector "proxy" на конкретный узел.
// Чтобы вернуться к автоматике — передай tag = "proxy-auto".
func (c *Client) Switch(tag string) error {
	body, _ := json.Marshal(map[string]string{"name": tag})
	req, err := http.NewRequest(http.MethodPut, c.Base+"/proxies/proxy", bytes.NewReader(body))
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

// Snapshot возвращает отсортированный список узлов для отображения.
func (c *Client) Snapshot() ([]NodeInfo, error) {
	proxies, err := c.ListProxies()
	if err != nil {
		return nil, err
	}
	var out []NodeInfo
	for name, p := range proxies {
		switch p.Type {
		case "Selector", "URLTest", "Direct", "Block", "DNS":
			continue
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
