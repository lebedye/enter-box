package singbox

import (
	"context"
	"sync"
	"time"

	"enter-box/internal/proxyapi"
)

const (
	// failoverInitialDelay — пауза перед первой проверкой, чтобы sing-box
	// успел поднять Clash API и загрузить сохранённый выбор.
	failoverInitialDelay = 5 * time.Second
	// failoverCheckInterval — период между проверками (отсчитывается после
	// завершения предыдущей, чтобы не накапливать тики).
	failoverCheckInterval = 2 * time.Second
	// failoverFailureWindow — сколько непрерывных ошибок нужно, чтобы
	// считать VPN действительно упавшим (а не коротким сбоем).
	failoverFailureWindow = 10 * time.Second
	// failoverParallel — сколько узлов проверяем одновременно.
	failoverParallel = 4
	// failoverSuccessesBeforeVPN — сколько подряд успехов одного кандидата
	// нужно, чтобы вернуть трафик в VPN после аварийного direct.
	failoverSuccessesBeforeVPN = 2
)

// failureWindow отслеживает непрерывную серию неудачных проверок.
// Успешная проверка полностью сбрасывает окно: отдельные зависания
// не суммируются в один «отказ».
type failureWindow struct {
	since time.Time
}

// Observe учитывает результат проверки в момент now.
// healthy=true сбрасывает окно; первый fail запоминает начало окна.
// Возвращает true, когда непрерывный отказ длится не меньше failoverFailureWindow.
func (w *failureWindow) Observe(now time.Time, healthy bool) bool {
	if healthy {
		w.since = time.Time{}
		return false
	}
	if w.since.IsZero() {
		w.since = now
		return false
	}
	return now.Sub(w.since) >= failoverFailureWindow
}

// monitorVPNFallback сохраняет интернет у абонентов при отказе VPN.
//
// Логика:
//   - outer proxy = "proxy-auto" → автоматический режим;
//   - outer proxy = реальный тег → ручной выбор, автоматика не спорит;
//   - outer proxy = "direct" → оператор явно выключил VPN, не трогаем.
//
// При непрерывном отказе ≥ failoverFailureWindow сначала проверяем WAN
// (Probe("direct")). Если WAN мёртв — предупреждаем один раз и ждём его
// возвращения, не переключая серверы. Когда WAN вернулся — вызываем
// r.RestartVPN (с внутренним rate-limit) и сбрасываем окно.
//
// Если WAN жив и режим auto — параллельно проверяем реальные узлы
// (lastGood первым), переключаем proxy-auto на первый живой. Если живых
// нет — переключаем внутренний proxy-auto на direct и просим RestartVPN.
// Возврат в VPN — после двух подряд успехов одного кандидата.
func (r *Runner) monitorVPNFallback(ctx context.Context) {
	client := proxyapi.New()

	var window failureWindow
	var apiWindow failureWindow
	var observationKey string
	var observedGeneration uint64
	var lastGood string
	var wanWasDown bool
	var wanWarned bool
	var candidate string
	var candidateSuccesses int

	// sleep ждёт d или завершения ctx. Возвращает false, если ctx отменён.
	sleep := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}

	if !sleep(failoverInitialDelay) {
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}

		generation := r.generationNow()
		outer, err := client.ActiveNode()
		if err != nil {
			r.logger.Debug("проверка VPN: Clash API пока недоступен", "err", err)
			// Если предыдущее переподключение не подняло API, повторяем его
			// с общим минутным ограничением; иначе восстановление остановится.
			if apiWindow.Observe(time.Now(), false) {
				r.restartIfCurrent(ctx, client, generation, "", "")
			}
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}
		apiWindow.Observe(time.Now(), true)

		// Явный direct от оператора — не вмешиваемся.
		if outer == "direct" {
			window = failureWindow{}
			wanWasDown = false
			wanWarned = false
			candidate = ""
			candidateSuccesses = 0
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		manual := outer != "proxy-auto"

		// current — узел, который реально используется сейчас.
		current := outer
		if !manual {
			if auto, aerr := client.AutoNode(); aerr == nil {
				current = auto
			}
		}
		// Ошибки старого узла нельзя переносить на новый ручной выбор.
		key := outer + "|" + current
		if key != observationKey || generation != observedGeneration {
			// Старые ошибки не переносим на новый выбор. Начальный пустой
			// ключ не сбрасывает сигнал, чтобы стартовые ошибки не терялись.
			if observationKey != "" {
				r.failureSignal.Clear()
			}
			observationKey = key
			observedGeneration = generation
			window = failureWindow{}
			candidate, candidateSuccesses = "", 0
		}

		// Проверяем текущий узел. В ручном режиме — только его.
		// Активный Probe запускаем только после пассивного сигнала ошибки:
		// при исправном VPN сеть не тестируется постоянно.
		healthy := false
		if current != "" && current != "direct" {
			if !wanWasDown && !r.failureSignal.Pending(time.Now()) {
				if !sleep(failoverCheckInterval) {
					return
				}
				continue
			}
			delay, perr := client.Probe(ctx, current)
			if ctx.Err() != nil {
				return
			}
			healthy = perr == nil && delay > 0
			if healthy {
				r.failureSignal.Clear()
				lastGood = current
			}
		} else if current == "direct" {
			// Внутренний direct — это уже аварийный режим, «здоровья» нет.
			healthy = false
		}

		now := time.Now()

		// Пока WAN лежал — проверяем только его возвращение.
		if wanWasDown {
			wanOK := probeDirect(ctx, client)
			if ctx.Err() != nil {
				return
			}
			if !wanOK {
				if !sleep(failoverCheckInterval) {
					return
				}
				continue
			}
			// WAN вернулся — перезапускаем VPN (метод сам ограничивает частоту).
			r.logger.Info("WAN восстановлен: перезапуск VPN")
			r.restartIfCurrent(ctx, client, generation, outer, current)
			window = failureWindow{}
			wanWasDown = false
			wanWarned = false
			candidate = ""
			candidateSuccesses = 0
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		if healthy {
			window.Observe(now, true)
			candidate = ""
			candidateSuccesses = 0
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		// Пассивный сигнал уже подтверждён 10 секундами, а Probe не прошёл:
		// не ждём второе окно, сразу переходим к WAN и резервным узлам.
		if r.failureSignal.Pending(now) {
			window.since = now.Add(-failoverFailureWindow)
		}

		// Непрерывный отказ ещё не набрал окно — ждём.
		if !window.Observe(now, false) {
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		// Окно отказа набрано. Сначала проверяем WAN.
		wanOK := probeDirect(ctx, client)
		if ctx.Err() != nil {
			return
		}
		if !wanOK {
			if !wanWarned {
				r.logger.Warn("VPN и WAN недоступны: ждём восстановления WAN, серверы не переключаем")
				wanWarned = true
			}
			wanWasDown = true
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		// WAN жив.
		if manual {
			// Ручной режим: не выбираем другие VPN, только перезапуск того же конфига.
			r.logger.Warn("VPN недоступен (ручной узел): перезапуск sing-box", "node", current)
			r.restartIfCurrent(ctx, client, generation, outer, current)
			window = failureWindow{}
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		// Автоматический режим.
		if current == "direct" {
			// Внутренний direct: ищем живой узел, начиная с lastGood.
			// Возврат в VPN — после двух подряд успехов одного кандидата.
			// Вторую проверку делаем тому же кандидату, а не случайному
			// самому быстрому ответившему узлу из параллельного поиска.
			preferred := lastGood
			if candidate != "" {
				preferred = candidate
			}
			alive, tag := findAliveNode(ctx, client, preferred)
			if ctx.Err() != nil {
				return
			}
			if alive {
				if tag != candidate {
					candidate = tag
					candidateSuccesses = 0
				}
				candidateSuccesses++
				if candidateSuccesses >= failoverSuccessesBeforeVPN {
					// Перечитываем outer, чтобы не перебить ручной выбор.
					if r.switchAutoIfCurrent(client, generation, current, tag) {
						r.logger.Info("VPN ВОССТАНОВЛЕН: трафик снова идёт через VPN", "node", tag)
						r.requestSpeedPass()
						lastGood = tag
						candidate = ""
						candidateSuccesses = 0
						window = failureWindow{}
					}
				}
			} else {
				// Все узлы мертвы — перезапускаем sing-box (rate-limit внутри).
				candidate = ""
				candidateSuccesses = 0
				r.logger.Warn("все VPN-узлы недоступны: перезапуск sing-box")
				r.restartIfCurrent(ctx, client, generation, outer, current)
				window = failureWindow{}
			}
			if !sleep(failoverCheckInterval) {
				return
			}
			continue
		}

		// Текущий узел мёртв, но внутренний proxy-auto ещё не на direct.
		alive, tag := findAliveNode(ctx, client, lastGood)
		if ctx.Err() != nil {
			return
		}
		if alive {
			// Перечитываем outer перед переключением, чтобы не перебить ручной выбор.
			if r.switchAutoIfCurrent(client, generation, current, tag) {
				r.logger.Warn("VPN-узел недоступен: переключение на резервный", "node", tag)
				r.requestSpeedPass()
				lastGood = tag
			}
		} else {
			// Живых нет — внутренний proxy-auto на direct.
			if r.switchAutoIfCurrent(client, generation, current, "direct") {
				r.logger.Warn("VPN НЕДОСТУПЕН: абоненты переключены на прямой интернет через модем")
				// Новый ручной выбор между переключением и рестартом будет
				// замечен guard-проверкой, поэтому здоровый ручной узел не трогаем.
				r.restartIfCurrent(ctx, client, generation, outer, "direct")
			}
		}
		window = failureWindow{}
		if !sleep(failoverCheckInterval) {
			return
		}
	}
}

// probeDirect проверяет доступность WAN через outbound "direct".
func probeDirect(ctx context.Context, client *proxyapi.Client) bool {
	delay, err := client.Probe(ctx, "direct")
	if err != nil || delay <= 0 {
		return false
	}
	return true
}

// findAliveNode параллельно (не более failoverParallel) проверяет реальные
// узлы из Snapshot, начиная с preferred (lastGood). Возвращает первый живой
// тег и останавливает остальные пробы через отмену контекста.
func findAliveNode(ctx context.Context, client *proxyapi.Client, preferred string) (bool, string) {
	snapshot, err := client.Snapshot()
	if err != nil {
		return false, ""
	}

	// Сначала отдельно подтверждаем прежний узел. Простая перестановка
	// в параллельной очереди не гарантирует его приоритет перед быстрым соседом.
	tags := make([]string, 0, len(snapshot))
	if preferred != "" {
		for _, n := range snapshot {
			if n.Tag == preferred {
				if delay, err := client.Probe(ctx, preferred); err == nil && delay > 0 {
					return true, preferred
				}
				break
			}
		}
	}
	for _, n := range snapshot {
		if n.Tag == preferred {
			continue
		}
		tags = append(tags, n.Tag)
	}
	if len(tags) == 0 {
		return false, ""
	}

	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan string, len(tags))
	sem := make(chan struct{}, failoverParallel)

	var wg sync.WaitGroup
	for _, tag := range tags {
		tag := tag
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Ожидание слота выполняется в работнике: основной поток сразу
			// читает ответы и отменяет остальные проверки после первого успеха.
			select {
			case <-probeCtx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			delay, perr := client.Probe(probeCtx, tag)
			if perr == nil && delay > 0 {
				select {
				case results <- tag:
				case <-probeCtx.Done():
				}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()
	defer func() { cancel(); wg.Wait() }()
	for {
		select {
		case <-ctx.Done():
			return false, ""
		case tag, ok := <-results:
			if !ok {
				return false, ""
			}
			return true, tag
		}
	}
}
