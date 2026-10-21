#!/usr/bin/env bash
# wait-wan.sh — проверка управляемого ожидания WAN в ИЗОЛИРОВАННОМ
# network namespace на самой плате.
#
# Что делает:
#   * создаёт УНИКАЛЬНЫЙ netns (mktemp-имя, чужой не трогаем);
#   * запускает РЕАЛЬНЫЙ бинарник enter-box run -c <config> в фоне ВНУТРИ
#     этого netns, где НЕТ WAN (нет default-маршрута, нет IPv4 на iface);
#   * ждёт до 10с появления строки "ждём WAN" и живого процесса;
#   * убеждается, что НЕ появились "endpoints loaded", "WAN готов" и
#     netcfg-эффекты (nftables/DHCP/DNS) — т.е. main ждёт ДО pool.Load;
#   * шлёт SIGTERM, ждёт ≤5с (poll), ожидает ненулевой exit и
#     "context canceled" в логе — это успешная проверка отмены;
#   * cleanup по trap: убивает ТОЛЬКО свой PID, удаляет ТОЛЬКО свой
#     netns и свои temp-файлы. Реальную сеть/модем не трогает.
#
# Ограничения:
#   * запускать ТОЛЬКО на целевой плате под root;
#   * никаких внешних HTTP/S/DNS, чтения ключей, вывода конфигов,
#     изменений NetworkManager, файлов в /etc.
#
# Использование:
#   sudo deploy/tests/wait-wan.sh [--binary PATH] [--config PATH]
#
# Коды выхода: 0 — PASS, 1 — FAIL, 2 — ошибка аргументов/окружения.

set -u
set -o pipefail

BINARY="/usr/local/bin/enter-box"
CONFIG="/etc/enter-box/enter-box.yaml"

usage() {
	cat >&2 <<EOF
Использование: $0 [--binary PATH] [--config PATH]

  --binary PATH   путь к бинарнику enter-box (по умолчанию $BINARY)
  --config PATH   путь к конфигу enter-box (по умолчанию $CONFIG)
  -h, --help      эта справка
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
		--binary)
			[ $# -ge 2 ] || { echo "ошибка: --binary требует значение" >&2; usage; exit 2; }
			BINARY="$2"; shift 2 ;;
		--binary=*)
			BINARY="${1#--binary=}"; shift ;;
		--config)
			[ $# -ge 2 ] || { echo "ошибка: --config требует значение" >&2; usage; exit 2; }
			CONFIG="$2"; shift 2 ;;
		--config=*)
			CONFIG="${1#--config=}"; shift ;;
		-h|--help)
			usage; exit 0 ;;
		--)
			shift; break ;;
		-*)
			echo "ошибка: неизвестный флаг: $1" >&2; usage; exit 2 ;;
		*)
			echo "ошибка: позиционные аргументы не поддерживаются: $1" >&2; usage; exit 2 ;;
	esac
done
if [ $# -gt 0 ]; then
	echo "ошибка: лишние аргументы: $*" >&2; usage; exit 2
fi

if [ "$(id -u)" -ne 0 ]; then
	echo "FAIL: скрипт нужно запускать под root" >&2
	exit 2
fi
if ! command -v ip >/dev/null 2>&1; then
	echo "FAIL: не найден 'ip' (iproute2)" >&2
	exit 2
fi
if [ ! -x "$BINARY" ]; then
	echo "FAIL: бинарник не найден или не исполняемый: $BINARY" >&2
	exit 2
fi
if [ ! -r "$CONFIG" ]; then
	echo "FAIL: конфиг не читается: $CONFIG" >&2
	exit 2
fi

# Имя netns берём из basename временного каталога — без mktemp -u,
# чтобы не было гонки между «проверить имя» и «создать netns».
TMPDIR_RUN="$(mktemp -d /tmp/eb-wanwait.XXXXXXXX)" || {
	echo "FAIL: не удалось создать временный каталог в /tmp" >&2
	exit 2
}
if [ -z "${TMPDIR_RUN:-}" ] || [ ! -d "$TMPDIR_RUN" ]; then
	echo "FAIL: mktemp вернул пустой/несуществующий каталог" >&2
	exit 2
fi
NS_NAME="$(basename "$TMPDIR_RUN")"
LOG="$TMPDIR_RUN/run.log"
NETNS_ERR="$TMPDIR_RUN/netns.err"

CHILD_PID=""
NS_CREATED=0

cleanup() {
	if [ -n "${CHILD_PID:-}" ] && kill -0 "$CHILD_PID" 2>/dev/null; then
		kill -TERM "$CHILD_PID" 2>/dev/null || true
		for _ in 1 2 3 4; do
			kill -0 "$CHILD_PID" 2>/dev/null || break
			sleep 0.5
		done
		kill -KILL "$CHILD_PID" 2>/dev/null || true
	fi
	if [ "$NS_CREATED" -eq 1 ]; then
		ip netns delete "$NS_NAME" 2>/dev/null || true
	fi
	if [ -n "${TMPDIR_RUN:-}" ] && [ -d "$TMPDIR_RUN" ]; then
		rm -f -- "$TMPDIR_RUN/run.log" "$TMPDIR_RUN/netns.err" 2>/dev/null || true
		rmdir -- "$TMPDIR_RUN" 2>/dev/null || true
	fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM
trap 'cleanup; exit 129' HUP

if ip netns list 2>/dev/null | awk '{print $1}' | grep -qx "$NS_NAME"; then
	echo "FAIL: netns '$NS_NAME' уже существует, не перезаписываем" >&2
	exit 2
fi
if ! ip netns add "$NS_NAME" 2>"$NETNS_ERR"; then
	echo "FAIL: не удалось создать netns '$NS_NAME':" >&2
	cat "$NETNS_ERR" >&2 || true
	exit 2
fi
NS_CREATED=1

# Внутри netns — только loopback, БЕЗ WAN. Никаких veth/маршрутов наружу.
ip netns exec "$NS_NAME" ip link set lo up 2>/dev/null || true

# Реальный бинарник с реальным конфигом, но в изолированном netns.
ip netns exec "$NS_NAME" "$BINARY" run -c "$CONFIG" >"$LOG" 2>&1 &
CHILD_PID=$!

WAIT_LOG_SECS=10
WAIT_LOG_STEPS=$((WAIT_LOG_SECS * 10))
SAW_WAIT=0
for _ in $(seq 1 "$WAIT_LOG_STEPS"); do
	if ! kill -0 "$CHILD_PID" 2>/dev/null; then
		break
	fi
	if grep -q "ждём WAN" "$LOG" 2>/dev/null; then
		SAW_WAIT=1
		break
	fi
	sleep 0.1
done

if [ "$SAW_WAIT" -eq 1 ]; then
	sleep 1
	if ! kill -0 "$CHILD_PID" 2>/dev/null; then
		SAW_WAIT=0
	fi
fi

FAIL_MSG=""

if [ "$SAW_WAIT" -ne 1 ]; then
	FAIL_MSG="не дождались строки 'ждём WAN' за ${WAIT_LOG_SECS}с"
fi

if [ -z "$FAIL_MSG" ] && ! kill -0 "$CHILD_PID" 2>/dev/null; then
	FAIL_MSG="процесс завершился до SIGTERM (ожидалось живое ожидание WAN)"
fi

if [ -z "$FAIL_MSG" ] && grep -q "endpoints loaded" "$LOG" 2>/dev/null; then
	FAIL_MSG="в логе есть 'endpoints loaded' — main прошёл ДО pool.Load"
fi
if [ -z "$FAIL_MSG" ] && grep -q "WAN готов" "$LOG" 2>/dev/null; then
	FAIL_MSG="в логе есть 'WAN готов' — ожидание не сработало в изоляции"
fi
if [ -z "$FAIL_MSG" ] && ip netns exec "$NS_NAME" nft list table inet enter_box >/dev/null 2>&1; then
	FAIL_MSG="в netns появилась nftables-таблица enter_box — netcfg применился"
fi

EXIT_CODE=""
stop_child() {
	[ -n "${CHILD_PID:-}" ] || return 0
	if ! kill -0 "$CHILD_PID" 2>/dev/null; then
		wait "$CHILD_PID" 2>/dev/null
		EXIT_CODE=$?
		CHILD_PID=""
		return 0
	fi
	kill -TERM "$CHILD_PID" 2>/dev/null || true
	WAIT_TERM_SECS=5
	WAIT_TERM_STEPS=$((WAIT_TERM_SECS * 10))
	EXITED=0
	for _ in $(seq 1 "$WAIT_TERM_STEPS"); do
		if ! kill -0 "$CHILD_PID" 2>/dev/null; then
			EXITED=1
			break
		fi
		sleep 0.1
	done
	if [ "$EXITED" -ne 1 ]; then
		kill -KILL "$CHILD_PID" 2>/dev/null || true
		for _ in 1 2 3 4 5; do
			kill -0 "$CHILD_PID" 2>/dev/null || break
			sleep 0.1
		done
		if [ -z "$FAIL_MSG" ]; then
			FAIL_MSG="процесс не завершился за ${WAIT_TERM_SECS}с после SIGTERM"
		fi
	fi
	wait "$CHILD_PID" 2>/dev/null
	EXIT_CODE=$?
	CHILD_PID=""
}

stop_child

if [ -z "$FAIL_MSG" ]; then
	if [ "$EXIT_CODE" = "0" ]; then
		FAIL_MSG="ожидался ненулевой exit после SIGTERM, получен 0"
	fi
fi
if [ -z "$FAIL_MSG" ] && ! grep -q "context canceled" "$LOG" 2>/dev/null; then
	FAIL_MSG="в логе нет 'context canceled' — отмена не отработала штатно"
fi

if [ -n "$FAIL_MSG" ]; then
	echo "FAIL: $FAIL_MSG"
	head -n 40 "$LOG" 2>/dev/null || true
	exit 1
fi

echo "PASS: main ждёт WAN до pool.Load, SIGTERM отменяет ожидание (exit=$EXIT_CODE)"
exit 0