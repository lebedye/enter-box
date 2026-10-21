// Command amnezia-fetch обменивает Premium-файл vpn:// на
// готовую AmneziaWG-конфигурацию. Утилита нужна и для
// диагностики, и для установочного скрипта.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"enter-box/internal/amneziaapi"
)

func main() {
	input := flag.String("in", "", "файл с Premium-ключом vpn://")
	output := flag.String("out", "", "куда записать готовый vpn://")
	timeout := flag.Duration("timeout", 45*time.Second, "общий таймаут запроса")
	flag.Parse()

	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "использование: amnezia-fetch -in <premium.vpn> -out <resolved.vpn>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		fatal("read input", err)
	}
	key := strings.TrimSpace(string(raw))
	if !amneziaapi.IsBootstrapVPN(key) {
		fatal("validate input", fmt.Errorf("файл не является Premium-ключом Amnezia"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	resolved, err := amneziaapi.Resolve(ctx, key)
	if err != nil {
		fatal("resolve subscription", err)
	}

	// 0600 обязателен: в готовом файле уже есть приватный
	// ключ WireGuard, его нельзя делать доступным другим пользователям.
	if err := os.WriteFile(*output, []byte(resolved+"\n"), 0o600); err != nil {
		fatal("write output", err)
	}
	fmt.Println("готовая AmneziaWG-конфигурация записана без вывода секретов")
}

func fatal(stage string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", stage, err)
	os.Exit(1)
}
