// Package setup — интерактивный установщик enter-box.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func NewPrompter() *Prompter {
	return &Prompter{in: bufio.NewReader(os.Stdin), out: os.Stdout}
}

func (p *Prompter) Title(s string) {
	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, "──────────────────────────────────────────────────────")
	fmt.Fprintln(p.out, s)
	fmt.Fprintln(p.out, "──────────────────────────────────────────────────────")
}

func (p *Prompter) Info(format string, a ...any) {
	fmt.Fprintf(p.out, format+"\n", a...)
}

func (p *Prompter) Warn(format string, a ...any) {
	fmt.Fprintf(p.out, "⚠  "+format+"\n", a...)
}

func (p *Prompter) OK(format string, a ...any) {
	fmt.Fprintf(p.out, "✓  "+format+"\n", a...)
}

func (p *Prompter) AskString(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// AskPassword читает пароль без эха (если stdin — терминал).
// Если не терминал (пайп) — падает в обычный ReadString.
func (p *Prompter) AskPassword(label string) string {
	fmt.Fprintf(p.out, "%s: ", label)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		data, err := term.ReadPassword(fd)
		fmt.Fprintln(p.out)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	line, _ := p.in.ReadString('\n')
	return strings.TrimSpace(line)
}

// AskSSID запрашивает SSID с базовой валидацией: 1..32 байта, без пробелов по краям.
func (p *Prompter) AskSSID(label string) string {
	for {
		ssid := p.AskString(label, "")
		if ssid == "" {
			p.Warn("SSID не может быть пустым")
			continue
		}
		if len(ssid) > 32 {
			p.Warn("SSID длиннее 32 байт — не влезет в 802.11-кадр")
			continue
		}
		return ssid
	}
}

// AskWifiPassword запрашивает пароль WiFi. Требуется WPA2-минимум 8 символов.
// По желанию пользователя можно ввести открытую сеть — но политика enter-box
// требует пароль (см. wizard).
func (p *Prompter) AskWifiPassword(label string) string {
	for {
		pw := p.AskPassword(label)
		if pw == "" {
			p.Warn("пароль не может быть пустым (open-сети не поддерживаются)")
			continue
		}
		if len(pw) < 8 {
			p.Warn("WPA2 требует минимум 8 символов, получили %d", len(pw))
			continue
		}
		if len(pw) > 63 {
			p.Warn("пароль длиннее 63 символов")
			continue
		}
		return pw
	}
}

func (p *Prompter) AskYesNo(label string, def bool) bool {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s: ", label, suffix)
		line, _ := p.in.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		switch line {
		case "":
			return def
		case "y", "yes", "д", "да":
			return true
		case "n", "no", "н", "нет":
			return false
		}
		p.Warn("введите y или n")
	}
}

func (p *Prompter) AskChoice(label string, options []string, defIdx int) int {
	fmt.Fprintln(p.out, label)
	for i, o := range options {
		marker := " "
		if i == defIdx {
			marker = "*"
		}
		fmt.Fprintf(p.out, "  %s %2d) %s\n", marker, i+1, o)
	}
	for {
		fmt.Fprintf(p.out, "Выбор [%d]: ", defIdx+1)
		line, _ := p.in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defIdx
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			p.Warn("введите число от 1 до %d", len(options))
			continue
		}
		return n - 1
	}
}

func (p *Prompter) AskMultiLine(label, hint string) []string {
	fmt.Fprintln(p.out, label)
	if hint != "" {
		fmt.Fprintln(p.out, "   "+hint)
	}
	fmt.Fprintln(p.out, "   (пустая строка — закончить)")
	var lines []string
	for {
		fmt.Fprint(p.out, "> ")
		line, _ := p.in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
