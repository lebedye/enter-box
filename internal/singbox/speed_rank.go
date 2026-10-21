package singbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"enter-box/internal/proxyapi"
)

// nodeSpeed — результат измерения одного узла.
// mbps — скорость скачивания в мегабитах в секунду, delay — задержка probe в мс.
type nodeSpeed struct {
	tag   string
	mbps  float64
	delay int
}

// chooseSpeedNode выбирает лучший узел по скорости.
//
// Правила:
//   - узлы с mbps <= 0 (ошибки/не измерено) игнорируются;
//   - сортировка: mbps по убыванию, при равенстве — delay по возрастанию,
//     при равенстве — tag по возрастанию (детерминированный tie-break);
//   - гистерезис 25%: если текущий узел присутствует и его mbps >= best*0.8,
//     оставляем текущий, чтобы не дёргать selector из-за шума измерений.
//
// Возвращает (узел, true) если есть хотя бы один валидный результат.
func chooseSpeedNode(results []nodeSpeed, current string) (nodeSpeed, bool) {
	// Отбрасываем неудачные измерения.
	valid := make([]nodeSpeed, 0, len(results))
	for _, r := range results {
		if r.mbps > 0 {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		return nodeSpeed{}, false
	}

	sort.Slice(valid, func(i, j int) bool {
		if valid[i].mbps != valid[j].mbps {
			return valid[i].mbps > valid[j].mbps
		}
		if valid[i].delay != valid[j].delay {
			return valid[i].delay < valid[j].delay
		}
		return valid[i].tag < valid[j].tag
	})

	best := valid[0]

	// Гистерезис: сохраняем текущий узел, если он не сильно отстаёт.
	if current != "" {
		for _, r := range valid {
			if r.tag == current {
				if r.mbps >= best.mbps*0.8 {
					return r, true
				}
				break
			}
		}
	}
	return best, true
}

// speedTestURL — эндпоинт Cloudflare для замера скорости.
const speedTestURL = "https://speed.cloudflare.com/__down?bytes=524288"

// speedTestBytes — сколько байт ожидаем скачать.
const speedTestBytes int64 = 524288

// measureNodeSpeed измеряет скорость скачивания через конкретный outbound,
// не переключая selector. Сетевые операции выполняются без удержания r.mu.
func (r *Runner) measureNodeSpeed(ctx context.Context, tag string) (float64, error) {
	// Под mutex только берём актуальный instance и outbound.
	r.mu.Lock()
	instance := r.instance

	if instance == nil {
		r.mu.Unlock()
		return 0, fmt.Errorf("sing-box не запущен")
	}
	ob, ok := instance.Outbound().Outbound(tag)
	r.mu.Unlock()
	if !ok || ob == nil {
		return 0, fmt.Errorf("outbound %q не найден", tag)
	}

	// Транспорт с DialContext через нужный outbound. Proxy: nil — не используем
	// переменные окружения; keep-alive выключен, чтобы измерять «холодный» канал.
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		// Запрещаем редиректы: измеряем именно этот эндпоинт.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, speedTestURL, nil)
	if err != nil {
		return 0, err
	}
	// Требуем несжатое тело, чтобы байты соответствовали реальному трафику.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("speed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("speed http %d", resp.StatusCode)
	}
	// Проверяем, что сервер не сжал тело.
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && ce != "identity" {
		return 0, fmt.Errorf("speed: неожиданный Content-Encoding %q", ce)
	}

	if resp.ContentLength >= 0 && resp.ContentLength != speedTestBytes {
		return 0, fmt.Errorf("speed: неверный размер ответа %d", resp.ContentLength)
	}
	// Меряем время чтения тела ПОСЛЕ получения заголовков. При таймауте
	// учитываем уже скачанные байты: медленный исправный канал тоже получает оценку.
	start := time.Now()
	n, err := io.CopyN(io.Discard, resp.Body, speedTestBytes)
	elapsed := time.Since(start)
	var timeout net.Error
	partialTimeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
	if err != nil && !partialTimeout {
		return 0, fmt.Errorf("speed read: %w", err)
	}
	if n < 4096 || (n != speedTestBytes && !partialTimeout) {
		return 0, fmt.Errorf("speed: короткое тело %d/%d", n, speedTestBytes)
	}
	if elapsed <= 0 {
		return 0, fmt.Errorf("speed: нулевое время измерения")
	}

	mbps := float64(n) * 8 / elapsed.Seconds() / 1e6
	return mbps, nil
}

// monitorVPNSpeed запускает замеры только по сигналу подтверждённого отказа.
// У исправного соединения нет ни стартового, ни периодического теста скорости.
func (r *Runner) monitorVPNSpeed(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.speedWake:
			r.runSpeedPass(ctx)
		}
	}
}

// После восстановления сначала уже есть рабочий интернет; теперь уточняем скорость.
func (r *Runner) requestSpeedPass() {
	select {
	case r.speedWake <- struct{}{}:
	default:
	}
}

// runSpeedPass выполняет один обход после отказа с ограничением общего времени.
func (r *Runner) runSpeedPass(ctx context.Context) {
	client := proxyapi.New()

	generation := r.generationNow()

	// Работаем только в автоматическом режиме.
	outer, err := client.ActiveNode()
	if err != nil || outer != "proxy-auto" {
		return
	}
	current, err := client.AutoNode()
	if err != nil || current == "" || current == "direct" {
		// Восстановлением прямого выхода занимается failover.
		return
	}

	snapshot, err := client.Snapshot()
	if err != nil {
		r.logger.Debug("speed: не удалось получить список узлов", "err", err)
		return
	}
	// Сначала измеряем текущий выход: бюджет обхода не должен скрыть его оценку.
	for i := range snapshot {
		if snapshot[i].Tag == current {
			snapshot[0], snapshot[i] = snapshot[i], snapshot[0]
			break
		}
	}

	// Бюджет покрывает до двадцати узлов с неудачными пробами и загрузками.
	passCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	results := make([]nodeSpeed, 0, len(snapshot))
	for _, node := range snapshot {
		if passCtx.Err() != nil {
			break
		}
		// Прерываемся, если поколение сменилось (reload/restart).
		if r.generationNow() != generation {
			return
		}
		// Перед каждым узлом проверяем, что режим всё ещё автоматический
		// и внутренний выбор не изменился — ручной выбор должен остановить обход.
		if outerNow, err := client.ActiveNode(); err != nil || outerNow != "proxy-auto" {
			return
		}
		if curNow, err := client.AutoNode(); err != nil || curNow != current {
			return
		}

		// Сначала probe: отсекаем заведомо мёртвые узлы.
		delay, err := client.Probe(passCtx, node.Tag)
		if err != nil || delay <= 0 {
			continue
		}
		// Затем реальное измерение скорости.
		mbps, err := r.measureNodeSpeed(passCtx, node.Tag)
		if err != nil || mbps <= 0 {
			continue
		}
		r.logger.Info("speed metric", "tag", node.Tag, "mbps", mbps, "delay_ms", delay)
		results = append(results, nodeSpeed{tag: node.Tag, mbps: mbps, delay: delay})
	}

	// Проверяем, что текущий узел успешно измерен: иначе не трогаем selector,
	// восстановление — задача failover.
	currentOK := false
	for _, res := range results {
		if res.tag == current {
			currentOK = true
			break
		}
	}
	if !currentOK {
		return
	}

	best, ok := chooseSpeedNode(results, current)
	if !ok {
		return
	}
	if best.tag == current {
		return
	}

	// Финальные проверки перед переключением.
	if ctx.Err() != nil {
		return
	}
	if r.generationNow() != generation {
		return
	}
	if r.switchAutoIfCurrent(client, generation, current, best.tag) {
		r.logger.Info("speed: переключён автоматический выход",
			"from", current, "to", best.tag, "mbps", best.mbps, "delay_ms", best.delay)
	}
}
