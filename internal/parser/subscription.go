package parser

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"enter-box/internal/config"
)

func LoadSubscription(ctx context.Context, sub config.SubscriptionConfig) ([]*Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return nil, err
	}
	ua := sub.UserAgent
	if ua == "" {
		ua = "enter-box/1.0"
	}
	req.Header.Set("User-Agent", ua)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	text := string(body)
	if !strings.Contains(text, "://") {
		if dec, err := b64Decode(strings.TrimSpace(text)); err == nil {
			text = string(dec)
		}
	}

	var eps []*Endpoint
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		ep, err := ParseLink(line)
		if err != nil {
			continue
		}
		if sub.Tag != "" {
			ep.Tag = sub.Tag + "/" + ep.Tag
		}
		eps = append(eps, ep)
	}
	return eps, sc.Err()
}
