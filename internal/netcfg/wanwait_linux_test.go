package netcfg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// discardLogger — логгер, который никуда не пишет, чтобы не засорять вывод тестов.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestWaitWAN_Immediate — если probe сразу возвращает true,
// waitWAN должен завершиться успешно и вызвать probe ровно один раз.
func TestWaitWAN_Immediate(t *testing.T) {
	calls := 0
	probe := func(ctx context.Context, iface string) (bool, error) {
		calls++
		return true, nil
	}

	err := waitWAN(context.Background(), discardLogger(), "usb0", time.Second, 10*time.Millisecond, probe)
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ожидался 1 вызов probe, получено %d", calls)
	}
}

// TestWaitWAN_DelayedProbeError — probe сначала возвращает ошибку,
// затем ошибку, затем успех. Ожидаем 3 вызова и nil.
func TestWaitWAN_DelayedProbeError(t *testing.T) {
	calls := 0
	probe := func(ctx context.Context, iface string) (bool, error) {
		calls++
		if calls < 3 {
			return false, errors.New("маршрут ещё не готов")
		}
		return true, nil
	}

	err := waitWAN(context.Background(), discardLogger(), "usb0", time.Second, 5*time.Millisecond, probe)
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if calls != 3 {
		t.Fatalf("ожидался 3 вызова probe, получено %d", calls)
	}
}

// TestWaitWAN_Canceled — отмена контекста из probe должна
// приводить к ошибке context.Canceled.
func TestWaitWAN_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probe := func(ctx context.Context, iface string) (bool, error) {
		cancel()
		return false, nil
	}

	err := waitWAN(ctx, discardLogger(), "usb0", time.Second, 5*time.Millisecond, probe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидался context.Canceled, получено: %v", err)
	}
}

// TestWaitWAN_AlreadyCanceled — если контекст уже отменён,
// probe не должен вызываться ни разу, даже если он «готов».
func TestWaitWAN_AlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	probe := func(ctx context.Context, iface string) (bool, error) {
		calls++
		return true, nil
	}

	err := waitWAN(ctx, discardLogger(), "usb0", time.Second, 5*time.Millisecond, probe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидался context.Canceled, получено: %v", err)
	}
	if calls != 0 {
		t.Fatalf("probe не должен вызываться, получено %d вызовов", calls)
	}
}

// TestWaitWAN_Timeout — если probe всегда false, должен сработать
// таймаут и вернуться context.DeadlineExceeded.
func TestWaitWAN_Timeout(t *testing.T) {
	probe := func(ctx context.Context, iface string) (bool, error) {
		return false, nil
	}

	err := waitWAN(context.Background(), discardLogger(), "usb0", 30*time.Millisecond, 5*time.Millisecond, probe)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался context.DeadlineExceeded, получено: %v", err)
	}
}

// TestWaitWAN_ConfiguredIface — проверяем, что имя интерфейса
// передаётся в probe без изменений.
func TestWaitWAN_ConfiguredIface(t *testing.T) {
	var got string
	probe := func(ctx context.Context, iface string) (bool, error) {
		got = iface
		return true, nil
	}

	err := waitWAN(context.Background(), discardLogger(), "wwan0", time.Second, 5*time.Millisecond, probe)
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if got != "wwan0" {
		t.Fatalf("ожидался iface=wwan0, получено %q", got)
	}
}

// routeFixture — образец вывода /proc/net/route для тестов defaultRouteReady.
// Адреса в hex, little-endian. usb0 имеет default (Dest=0, Mask=0, Flags=0x3),
// wlan0 — тоже default, но с большей метрикой.
const routeFixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
wlan0	00000000	0100000A	0003	0	0	600	00000000	0	0	0
usb0	0001A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
`

// TestDefaultRouteReady_UpDefault — в фикстуре есть default через usb0,
// ожидаем true.
func TestDefaultRouteReady_UpDefault(t *testing.T) {
	ok, err := defaultRouteReady(strings.NewReader(routeFixture), "usb0")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if !ok {
		t.Fatalf("ожидался true для usb0")
	}
}

// TestDefaultRouteReady_OtherIface — default через usb0 не должен
// считаться готовностью для eth1.
func TestDefaultRouteReady_OtherIface(t *testing.T) {
	ok, err := defaultRouteReady(strings.NewReader(routeFixture), "eth1")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if ok {
		t.Fatalf("ожидался false для eth1")
	}
}

// TestDefaultRouteReady_NonZeroDestination — строка с ненулевым
// destination не является default-маршрутом.
func TestDefaultRouteReady_NonZeroDestination(t *testing.T) {
	const fixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	0001A8C0	0101A8C0	0003	0	0	100	00000000	0	0	0
`
	ok, err := defaultRouteReady(strings.NewReader(fixture), "usb0")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if ok {
		t.Fatalf("ожидался false для ненулевого destination")
	}
}

// TestDefaultRouteReady_NonZeroMask — строка с ненулевой маской
// (не /0) не является default-маршрутом.
func TestDefaultRouteReady_NonZeroMask(t *testing.T) {
	const fixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	00000000	0101A8C0	0003	0	0	100	00FFFFFF	0	0	0
`
	ok, err := defaultRouteReady(strings.NewReader(fixture), "usb0")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if ok {
		t.Fatalf("ожидался false для ненулевой маски")
	}
}

// TestDefaultRouteReady_DownFlags — маршрут без флага UP (0x1)
// не считается готовым.
func TestDefaultRouteReady_DownFlags(t *testing.T) {
	const fixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	00000000	0101A8C0	0002	0	0	100	00000000	0	0	0
`
	ok, err := defaultRouteReady(strings.NewReader(fixture), "usb0")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if ok {
		t.Fatalf("ожидался false без флага UP")
	}
}

// TestDefaultRouteReady_RejectFlag — маршрут с флагом reject (0x200)
// не считается готовым.
func TestDefaultRouteReady_RejectFlag(t *testing.T) {
	const fixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	00000000	0101A8C0	0203	0	0	100	00000000	0	0	0
`
	ok, err := defaultRouteReady(strings.NewReader(fixture), "usb0")
	if err != nil {
		t.Fatalf("ожидался nil, получено: %v", err)
	}
	if ok {
		t.Fatalf("ожидался false для reject")
	}
}
