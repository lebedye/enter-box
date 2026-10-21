package netcfg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// pollInterval — интервал опроса готовности WAN по умолчанию.
const pollInterval = 500 * time.Millisecond

// procNetRoute — путь к таблице маршрутов ядра (hex-формат).
const procNetRoute = "/proc/net/route"

// Флаги маршрута из /proc/net/route (см. include/uapi/linux/route.h).
const (
	rtfUp     = 0x0001 // маршрут активен
	rtfReject = 0x0200 // маршрут-«отказ», трафик не пойдёт
)

// WaitWANReady — публичная точка входа: ждёт готовности WAN-интерфейса
// с разумными значениями по умолчанию (интервал опроса 500ms,
// проверка через probeDefaultRoute).
func WaitWANReady(ctx context.Context, logger *slog.Logger, iface string, timeout time.Duration) error {
	return waitWAN(ctx, logger, iface, timeout, pollInterval, probeDefaultRoute)
}

// waitWAN — цикл ожидания: периодически вызывает probe, пока он не
// вернёт true, не истечёт timeout или не будет отменён ctx.
func waitWAN(ctx context.Context, logger *slog.Logger, iface string, timeout, interval time.Duration, probe func(context.Context, string) (bool, error)) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Сразу проверяем, не отменён ли контекст: уже отменённый контекст
	// нельзя считать успешным, даже если probe вернул бы true.
	if err := ctx.Err(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	logger.Info("ждём WAN", "iface", iface, "timeout", timeout)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Ошибки оборачиваем через %w, чтобы errors.Is работал.
			return fmt.Errorf("wait WAN %s: %w", iface, ctx.Err())
		case <-timer.C:
		}

		// Повторная проверка отмены перед probe: не вызываем probe
		// на уже отменённом контексте.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait WAN %s: %w", iface, err)
		}

		ok, err := probe(ctx, iface)
		if err != nil {
			logger.Debug("WAN probe", "iface", iface, "err", err)
		}
		if ok {
			logger.Info("WAN готов", "iface", iface, "elapsed", time.Since(start).Round(time.Millisecond))
			return nil
		}

		timer.Reset(interval)
	}
}

// probeDefaultRoute — проверка готовности WAN: интерфейс существует,
// поднят, имеет IPv4-адрес (не loopback/unspecified/link-local),
// и в /proc/net/route есть default-маршрут через него.
func probeDefaultRoute(ctx context.Context, iface string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return false, fmt.Errorf("interface %s: %w", iface, err)
	}
	if ifi.Flags&net.FlagUp == 0 {
		return false, nil
	}

	addrs, err := ifi.Addrs()
	if err != nil {
		return false, fmt.Errorf("addrs %s: %w", iface, err)
	}
	hasIPv4 := false
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipn.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsLoopback() || ip4.IsUnspecified() || ip4.IsLinkLocalUnicast() {
			continue
		}
		hasIPv4 = true
		break
	}
	if !hasIPv4 {
		return false, nil
	}

	f, err := os.Open(procNetRoute)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", procNetRoute, err)
	}
	defer f.Close()

	return defaultRouteReady(f, iface)
}

// defaultRouteReady — разбирает /proc/net/route и сообщает, есть ли
// подходящий default-маршрут через iface.
//
// Формат строки (hex, little-endian для адресов):
//
//	Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
//
// Default-маршрут: Destination == 0 и Mask == 0, флаг UP установлен,
// флаг REJECT отсутствует.
func defaultRouteReady(r io.Reader, iface string) (bool, error) {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Первая строка — заголовок.
		if first {
			first = false
			if strings.HasPrefix(line, "Iface") {
				continue
			}
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		if fields[0] != iface {
			continue
		}

		dest, err := strconv.ParseUint(fields[1], 16, 32)
		if err != nil {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
		mask, err := strconv.ParseUint(fields[7], 16, 32)
		if err != nil {
			continue
		}

		if dest != 0 || mask != 0 {
			continue
		}
		if flags&rtfUp == 0 {
			continue
		}
		if flags&rtfReject != 0 {
			continue
		}
		return true, nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("scan %s: %w", procNetRoute, err)
	}
	return false, nil
}
