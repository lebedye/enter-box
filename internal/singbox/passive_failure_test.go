package singbox

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
)

func TestPassiveFailureEmptyNotPending(t *testing.T) {
	var p passiveFailure
	now := time.Now()
	if p.Pending(now) {
		t.Fatal("пустой сигнал не должен быть pending")
	}
}

func TestPassiveFailureIgnoresNonNetwork(t *testing.T) {
	var p passiveFailure
	now := time.Now()
	p.ObserveError(now, "inbound connection from 192.168.1.2")
	p.ObserveError(now, "outbound/vpn: started")
	p.ObserveError(now, "some info message")
	if p.Pending(now.Add(time.Hour)) {
		t.Fatal("нерелевантные сообщения не должны открывать окно")
	}
}

func TestPassiveFailureSingleErrorWindow(t *testing.T) {
	var p passiveFailure
	start := time.Now()
	p.ObserveError(start, "outbound/vpn: open connection: i/o timeout")
	if p.Pending(start.Add(9 * time.Second)) {
		t.Fatal("до 10 секунд активная проверка запрещена")
	}
	if !p.Pending(start.Add(10 * time.Second)) {
		t.Fatal("после 10 секунд окно должно быть открыто")
	}
}

func TestPassiveFailureRepeatedErrorKeepsStart(t *testing.T) {
	var p passiveFailure
	start := time.Now()
	p.ObserveError(start, "outbound/vpn: open connection: connection reset")
	p.ObserveError(start.Add(5*time.Second), "outbound/vpn: open connection: EOF")
	if p.Pending(start.Add(9 * time.Second)) {
		t.Fatal("повторная ошибка не должна сдвигать начало окна")
	}
	if !p.Pending(start.Add(10 * time.Second)) {
		t.Fatal("окно должно считаться от первой ошибки")
	}
}

func TestPassiveFailureClear(t *testing.T) {
	var p passiveFailure
	start := time.Now()
	p.ObserveError(start, "outbound/vpn: open connection: no route to host")
	p.Clear()
	if p.Pending(start.Add(time.Hour)) {
		t.Fatal("Clear должен сбрасывать окно")
	}
}

func TestPassiveFailureWriteMessageLevel(t *testing.T) {
	var p passiveFailure
	now := time.Now()
	p.WriteMessage(log.LevelInfo, "outbound/vpn: open connection: i/o timeout")
	if p.Pending(now.Add(time.Hour)) {
		t.Fatal("info-сообщения не должны учитываться")
	}
	p.WriteMessage(log.LevelError, "outbound/vpn: open connection: i/o timeout")
	if !p.Pending(time.Now().Add(10 * time.Second)) {
		t.Fatal("error-сообщение должно открывать окно")
	}
}
