package parser

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"enter-box/internal/config"
)

// maxBodySize — максимальный размер тела подписки (8 MiB).
const maxBodySize = 8 << 20

// maxEndpoints — максимальное число узлов из одной подписки.
const maxEndpoints = 500

// errSubGeneric — общая ошибка загрузки подписки без URL/userinfo/URI.
var errSubGeneric = errors.New("subscription load failed")

// LoadSubscription скачивает подписку, парсит ссылки и поддерживает
// дисковый last-good кэш сырых данных. При транспортной ошибке или
// не-200 ответе возвращает последний валидный кэш (если он есть).
// При отмене контекста кэш не читается.
func LoadSubscription(ctx context.Context, sub config.SubscriptionConfig) ([]*Endpoint, error) {
	// Проверяем схему URL: только http/https (для httptest и обычных подписок).
	u, err := url.Parse(sub.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errSubGeneric
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return nil, errSubGeneric
	}
	ua := sub.UserAgent
	if ua == "" {
		ua = "enter-box/1.0"
	}
	req.Header.Set("User-Agent", ua)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Транспортная ошибка: если контекст отменён — не читаем кэш.
		if ctx.Err() != nil {
			return nil, errSubGeneric
		}
		return fallbackToCache(sub)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fallbackToCache(sub)
	}

	// Ограничиваем чтение: maxBodySize+1, чтобы отличить «ровно лимит» от «больше».
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, errSubGeneric
		}
		return fallbackToCache(sub)
	}
	if len(body) > maxBodySize {
		// Слишком большое тело — ошибка, кэш не обновляем.
		return nil, errSubGeneric
	}

	eps, err := parseSubscriptionBody(body, sub.Tag)
	if err != nil {
		// Страница обслуживания с HTTP 200 тоже не должна выключать роутер.
		// Ответ отвергаем, не перезаписывая кэш; используем прежний список.
		return fallbackToCache(sub)
	}

	// Сохраняем сырые данные в кэш. Ошибка сохранения — ошибка загрузки.
	if err := writeCache(sub.URL, body); err != nil {
		return nil, cacheErrorf()
	}
	return eps, nil
}

// fallbackToCache пытается вернуть узлы из last-good кэша.
func fallbackToCache(sub config.SubscriptionConfig) ([]*Endpoint, error) {
	raw, err := readCache(sub.URL)
	if err != nil {
		return nil, errSubGeneric
	}
	if raw == nil {
		return nil, errSubGeneric
	}
	eps, err := parseSubscriptionBody(raw, sub.Tag)
	if err != nil {
		return nil, errSubGeneric
	}
	return eps, nil
}

// parseSubscriptionBody декодирует (при необходимости) base64 и парсит
// построчно ссылки. Возвращает ошибку, если не найдено ни одного узла.
func parseSubscriptionBody(body []byte, tag string) ([]*Endpoint, error) {
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
		if len(eps) >= maxEndpoints {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		ep, err := ParseLink(line)
		if err != nil {
			// Некорректные строки пропускаем.
			continue
		}
		if tag != "" {
			// Префикс тега без повторного наклеивания.
			if !strings.HasPrefix(ep.Tag, tag+"/") {
				ep.Tag = tag + "/" + ep.Tag
			}
		}
		eps = append(eps, ep)
	}
	if err := sc.Err(); err != nil {
		return nil, errSubGeneric
	}
	if len(eps) == 0 {
		return nil, errSubGeneric
	}
	return eps, nil
}

