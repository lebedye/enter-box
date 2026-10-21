package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"enter-box/internal/proxyapi"
)

// proxyMenu — интерактивное меню управления VPN-узлами.
//
// Поведение:
//   - при старте один раз снимаем Snapshot и прогоняем пробы;
//   - номер узла (1-based) → ручное переключение через c.Switch(exactTag);
//   - "a" → вернуться к автовыбору (proxyAuto);
//   - "r" → переснять Snapshot и заново прогнать пробы;
//   - "p" → попросить демон переподключиться (proxyReconnect);
//   - "q" или EOF → выход с кодом 0.
//
// После выбора узла меню остаётся открытым; Snapshot обновляется только
// по команде "r". Невалидный ввод не вызывает PUT и печатает понятное
// сообщение.
func proxyMenu(c *proxyapi.Client, in io.Reader, out io.Writer) int {
	ctx := context.Background()

	nodes, err := c.Snapshot()
	if err != nil {
		fmt.Fprintf(out, "не удалось получить список узлов: %v\n", err)
		return 1
	}
	// Пробы делаем по копии, чтобы не мутировать исходный срез.
	nodes = probeProxyNodes(ctx, c, nodes)

	reader := bufio.NewReader(in)

	for {
		// Ошибка необязательного статуса не мешает выбрать узел из снимка.
		if active, err := c.ActiveNode(); err == nil {
			if active == "proxy-auto" {
				selected, _ := c.AutoNode()
				fmt.Fprintf(out, "\nРежим: автоматический; текущий выход: %s\n", selected)
			} else {
				fmt.Fprintf(out, "\nРежим: ручной; текущий выход: %s\n", active)
			}
		}
		printProxyTable(out, nodes)
		fmt.Fprint(out, "\nвыбор: номер узла, a — авто, r — обновить, p — переподключить, q — выход\n> ")

		line, readErr := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if readErr != nil && line == "" {
			// EOF без ввода — корректный выход.
			fmt.Fprintln(out)
			return 0
		}

		switch line {
		case "":
			// Пустая строка — просто перерисовать меню.
			continue
		case "q", "Q":
			return 0
		case "a", "A":
			if code := proxyAuto(c); code != 0 {
				fmt.Fprintln(out, "не удалось вернуться к автовыбору")
			} else {
				fmt.Fprintln(out, "включён автовыбор узла")
			}
		case "r", "R":
			fresh, err := c.Snapshot()
			if err != nil {
				fmt.Fprintf(out, "не удалось обновить список узлов: %v\n", err)
				continue
			}
			nodes = probeProxyNodes(ctx, c, fresh)
		case "p", "P":
			proxyReconnect(in, out)
		default:
			n, convErr := strconv.Atoi(line)
			if convErr != nil || n < 1 || n > len(nodes) {
				fmt.Fprintf(out, "неверный выбор %q: введите номер 1..%d, a, r, p или q\n", line, len(nodes))
				continue
			}
			tag := nodes[n-1].Tag
			if err := c.Switch(tag); err != nil {
				fmt.Fprintf(out, "не удалось переключиться на %q: %v\n", tag, err)
				continue
			}
			fmt.Fprintf(out, "ручной режим: выбран %q; результат проверки %s\n", tag, nodes[n-1].Alive)
		}

		if readErr != nil {
			// Ввод закончился — выходим после обработки последней строки.
			return 0
		}
	}
}

// probeProxyNodes параллельно (не более 4 одновременно) прогоняет Probe
// по каждому узлу. Порядок узлов сохраняется как в исходном срезе.
// Успешная проба → Alive="ok", Delay="<N>ms"; ошибка → Alive="fail", Delay="-".
func probeProxyNodes(ctx context.Context, c *proxyapi.Client, nodes []proxyapi.NodeInfo) []proxyapi.NodeInfo {
	out := make([]proxyapi.NodeInfo, len(nodes))
	copy(out, nodes)

	const maxParallel = 4
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i := range out {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			delay, err := c.Probe(ctx, out[idx].Tag)
			if err != nil || delay <= 0 {
				out[idx].Alive = "fail"
				out[idx].Delay = "-"
				return
			}
			out[idx].Alive = "ok"
			out[idx].Delay = fmt.Sprintf("%dms", delay)
		}(i)
	}
	wg.Wait()
	return out
}

// printProxyTable печатает таблицу узлов: номер, тег, тип, статус, задержка.
func printProxyTable(out io.Writer, nodes []proxyapi.NodeInfo) {
	fmt.Fprintln(out, "№   TAG                          TYPE       ALIVE  DELAY")
	for i, n := range nodes {
		fmt.Fprintf(out, "%-3d %-28s %-10s %-6s %s\n",
			i+1, n.Tag, n.Type, n.Alive, n.Delay)
	}
}

// proxyAuto возвращает управление автовыбору.
//
// Если сейчас активен конкретный узел (не proxy-auto и не direct),
// сначала переключаем "proxy-auto" на него, чтобы автоматика продолжила
// с текущего узла, а затем переключаем "proxy" на "proxy-auto".
// При любой ошибке успех не объявляем.
func proxyAuto(c *proxyapi.Client) int {
	// Два PUT выполняются как одна операция относительно монитора/Reload.
	unlock, lockErr := c.LockControl()
	if lockErr != nil { return 1 }
	defer unlock()
	active, err := c.ActiveNode()
	if err == nil && active != "" && active != "proxy-auto" && active != "direct" {
		if err := c.SwitchGroup("proxy-auto", active); err != nil {
			return 1
		}
	}
	if err := c.SwitchGroup("proxy", "proxy-auto"); err != nil {
		return 1
	}
	return 0
}

// proxyReconnect отправляет SIGUSR1 основному процессу, сохраняя службу.
// Переподключаются только VPN-компоненты. Под root вызываем systemctl напрямую,
// иначе — через sudo. stdio связан с in/out, shell не используется.
//
// Сообщаем именно «запрос отправлен»: фактическое переподключение
// выполняет демон, и его успех здесь не наблюдаем.
func proxyReconnect(in io.Reader, out io.Writer) int {
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command("systemctl", "kill", "--kill-whom=main", "--signal=SIGUSR1", "enter-box.service")
	} else {
		cmd = exec.Command("sudo", "systemctl", "kill", "--kill-whom=main", "--signal=SIGUSR1", "enter-box.service")
	}
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(out, "не удалось отправить запрос на переподключение: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "запрос на переподключение отправлен")
	return 0
}
