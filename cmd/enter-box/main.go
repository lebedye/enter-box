// Command enter-box — VPN-шлюз для Orange Pi.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"enter-box/internal/config"
	"enter-box/internal/parser"
	"enter-box/internal/proxyapi"
	"enter-box/internal/setup"
	"enter-box/internal/singbox"
	"enter-box/internal/subs"
	"enter-box/internal/wifi"
)

var version = "dev"

func main() {
	args := os.Args[1:]

	if len(args) > 0 && len(args[0]) > 0 && args[0][0] == '-' {
		args = append([]string{"run"}, args...)
	}
	if len(args) == 0 {
		usage()
		os.Exit(0)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "setup", "init", "install":
		os.Exit(cmdSetup(rest))
	case "check", "doctor":
		os.Exit(cmdCheck(rest))
	case "interfaces", "ifaces", "ls":
		os.Exit(cmdInterfaces(rest))
	case "run", "daemon", "start":
		os.Exit(cmdRun(rest))
	case "proxy", "p":
		os.Exit(cmdProxy(rest))
	case "wifi", "w":
		os.Exit(cmdWifi(rest))
	case "version", "-v", "--version":
		fmt.Println("enter-box", version)
		os.Exit(0)
	case "help", "-h", "--help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`enter-box — VPN-шлюз (VLESS / VMess / AmneziaWG)

Команды:
  setup [flags]               интерактивная установка
  check                       проверки окружения
  interfaces                  список сетевых интерфейсов
  run  -c <path>              запуск демона
  proxy [status|list|test|switch|auto]   управление активным VPN-узлом
  wifi  <on|off|connect|scan> управление WiFi
  version                     версия

Флаги setup:
  -c <path>   путь к конфигу (по умолчанию /etc/enter-box/enter-box.yaml)

Флаги run:
  -c <path>   путь к конфигу (по умолчанию enter-box.yaml)
  -v          подробный лог
  -dump       напечатать конфиг sing-box и выйти

Примеры:
  sudo enter-box setup
  enter-box proxy                         показать активный узел
  enter-box proxy list                    все узлы с задержками
  enter-box proxy test proxy-auto         прогнать все узлы сейчас
  enter-box proxy switch provider/node3   прибить узел
  enter-box proxy auto                    вернуть автовыбор
  sudo enter-box wifi scan                скан сетей
  sudo enter-box wifi connect MyNet secret123
`)
}

// --- setup / check / interfaces ----------------------------------------

func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	cfgPath := fs.String("c", "/etc/enter-box/enter-box.yaml", "куда записать конфиг")
	_ = fs.Parse(args)

	wiz := setup.NewWizard(*cfgPath)
	if _, err := wiz.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	return 0
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	_ = fs.Parse(args)

	p := setup.NewPrompter()
	fails := setup.PrintChecks(p, setup.Preflight())
	if fails > 0 {
		return 1
	}
	return 0
}

func cmdInterfaces(args []string) int {
	fs := flag.NewFlagSet("interfaces", flag.ExitOnError)
	_ = fs.Parse(args)

	ifaces, err := setup.DetectInterfaces()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	fmt.Println("Доступные интерфейсы:")
	for _, i := range ifaces {
		fmt.Printf("  %-12s %s\n", i.Name, i.Description)
	}
	return 0
}

// --- run ---------------------------------------------------------------

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("c", "enter-box.yaml", "путь к конфигу")
	dump := fs.Bool("dump", false, "напечатать конфиг sing-box и выйти")
	verbose := fs.Bool("v", false, "подробный лог")
	_ = fs.Parse(args)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config load", "err", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool := parser.NewPool()
	if err := pool.LoadFromConfig(ctx, cfg); err != nil {
		logger.Error("load endpoints", "err", err)
		return 1
	}
	logger.Info("endpoints loaded", "count", pool.Len())

	opts, err := singbox.Generate(cfg, pool)
	if err != nil {
		logger.Error("generate sing-box config", "err", err)
		return 1
	}
	if *dump {
		if err := singbox.DumpJSON(os.Stdout, opts); err != nil {
			logger.Error("dump", "err", err)
			return 1
		}
		return 0
	}

	runner, err := singbox.NewRunner(logger, cfg, pool, opts)
	if err != nil {
		logger.Error("runner init", "err", err)
		return 1
	}
	if err := runner.Start(ctx); err != nil {
		logger.Error("start", "err", err)
		return 1
	}

	watcher := subs.New(logger, cfg, pool, runner)
	go watcher.Run(ctx)

	logger.Info("enter-box ready", "version", version)
	<-ctx.Done()
	logger.Info("shutting down")
	runner.Stop()
	return 0
}

// --- proxy -------------------------------------------------------------

func cmdProxy(args []string) int {
	c := proxyapi.New()

	if len(args) == 0 {
		return proxyStatus(c)
	}
	switch args[0] {
	case "status", "st":
		return proxyStatus(c)
	case "list", "ls":
		return proxyList(c)
	case "test":
		tag := "proxy-auto"
		if len(args) > 1 {
			tag = args[1]
		}
		return proxyTest(c, tag)
	case "switch", "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "использование: enter-box proxy switch <tag>")
			return 2
		}
		return proxySwitch(c, args[1])
	case "auto":
		return proxySwitch(c, "proxy-auto")
	}
	fmt.Fprintln(os.Stderr, "неизвестная подкоманда. См. enter-box help")
	return 2
}

func proxyStatus(c *proxyapi.Client) int {
	active, err := c.ActiveNode()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		fmt.Fprintln(os.Stderr, "подсказка: enter-box запущен? Проверьте: systemctl status enter-box")
		return 1
	}
	auto, _ := c.AutoNode()
	fmt.Printf("Активный узел:  %s\n", active)
	if auto != "" && auto != active {
		fmt.Printf("Auto (urltest): %s\n", auto)
	}
	return 0
}

func proxyList(c *proxyapi.Client) int {
	nodes, err := c.Snapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	if len(nodes) == 0 {
		fmt.Println("Узлов не найдено")
		return 0
	}
	fmt.Printf("%-30s %-10s %-8s %s\n", "TAG", "TYPE", "ALIVE", "DELAY")
	for _, n := range nodes {
		fmt.Printf("%-30s %-10s %-8s %s\n", n.Tag, n.Type, n.Alive, n.Delay)
	}
	return 0
}

func proxyTest(c *proxyapi.Client, tag string) int {
	delay, err := c.TestDelay(tag, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", tag, err)
		return 1
	}
	fmt.Printf("%s: %dms\n", tag, delay)
	return 0
}

func proxySwitch(c *proxyapi.Client, tag string) int {
	if err := c.Switch(tag); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	fmt.Println("Переключено на", tag)
	return 0
}

// --- wifi --------------------------------------------------------------

func cmdWifi(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "использование: enter-box wifi <on|off|scan|connect> [аргументы]")
		return 2
	}
	iface := defaultWifiIface()

	switch args[0] {
	case "on":
		if err := wifi.RadioOn(iface); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1
		}
		fmt.Println("WiFi включён")
		return 0

	case "off":
		if err := wifi.RadioOff(iface); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1
		}
		fmt.Println("WiFi выключен")
		return 0

	case "scan":
		out, err := wifi.Scan(iface)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1
		}
		fmt.Print(out)
		return 0

	case "connect":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "использование: enter-box wifi connect <SSID> <PASSWORD>")
			return 2
		}
		if err := wifi.Connect(iface, args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1
		}
		fmt.Printf("Подключено к %s\n", args[1])
		return 0
	}

	fmt.Fprintln(os.Stderr, "неизвестная подкоманда wifi")
	return 2
}

// defaultWifiIface — ищет первый беспроводной интерфейс.
// Если ничего не нашли — возвращаем "wlan0" (типовое имя).
func defaultWifiIface() string {
	ifaces, err := setup.DetectInterfaces()
	if err == nil {
		for _, i := range ifaces {
			if i.Kind == "wireless" {
				return i.Name
			}
		}
	}
	return "wlan0"
}
