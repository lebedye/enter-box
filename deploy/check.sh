#!/usr/bin/env bash
# check.sh — read-only снимок состояния enter-box на плате.
# Код возврата: 0 — VPN_OK, 1 — FAIL, 2 — аргументы, 3 — WARNING_DIRECT.

set -Eeuo pipefail

PROG="$(basename "$0")"
TARGET=""

log() { printf '[%s] %s\n' "$PROG" "$*" >&2; }
die() { printf '[%s] ERROR: %s\n' "$PROG" "$*" >&2; exit 1; }

usage() {
	cat >&2 <<EOF
Usage: $PROG [--target user@host]

  --target user@host   Проверить по SSH (без него — локально)
EOF
	exit 2
}

while (( $# )); do
	case "$1" in
		--target)
			[[ $# -ge 2 ]] || die "для --target нужен user@host"
			TARGET="$2"; shift 2 ;;
		-h|--help) usage ;;
		*) die "неизвестный аргумент: $1" ;;
	esac
done

[[ -z "$TARGET" || "$TARGET" == *@* ]] || die "target должен быть user@host"

SSH_OPTS=(
	-o BatchMode=yes
	-o ConnectTimeout=8
	-o StrictHostKeyChecking=accept-new
)

CHECKS=$(cat <<'REMOTE'
set -u
active="$(systemctl is-active enter-box.service 2>/dev/null || true)"
enabled="$(systemctl is-enabled enter-box.service 2>/dev/null || true)"
nrestarts="$(systemctl show -p NRestarts --value enter-box.service 2>/dev/null || true)"
printf 'ACTIVE=%s\n' "${active:-unknown}"
printf 'ENABLED=%s\n' "${enabled:-unknown}"
printf 'NRESTARTS=%s\n' "${nrestarts:-unknown}"

status_out="$(/usr/local/bin/enter-box proxy status 2>&1)"
status_rc=$?
printf 'PROXY_STATUS_RC=%s\n' "$status_rc"
printf 'PROXY_STATUS=%s\n' "$(printf '%s' "$status_out" | tr '\n' ' ' | cut -c1-400)"

list_out="$(/usr/local/bin/enter-box proxy list 2>&1)"
list_rc=$?
printf 'PROXY_LIST_RC=%s\n' "$list_rc"
printf 'PROXY_LIST=%s\n' "$(printf '%s' "$list_out" | tr '\n' ' ' | cut -c1-800)"

printf 'IPADDR=%s\n' "$(ip -br addr 2>/dev/null | tr '\n' ';' | cut -c1-800)"
printf 'NMCLI=%s\n' "$(nmcli device status 2>/dev/null | tr '\n' ';' | cut -c1-800)"

trace="$(curl -fsS --max-time 10 https://1.1.1.1/cdn-cgi/trace 2>/dev/null || true)"
printf 'TRACE_IP=%s\n' "$(printf '%s\n' "$trace" | awk -F= '$1=="ip"{print $2}')"
printf 'TRACE_LOC=%s\n' "$(printf '%s\n' "$trace" | awk -F= '$1=="loc"{print $2}')"

yandex_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 https://yandex.ru 2>/dev/null || true)"
printf 'YANDEX_CODE=%s\n' "${yandex_code:-000}"

dns_check() {
	local name="$1"
	python3 - "$name" <<'PY'
import random, socket, struct, sys
name = sys.argv[1]
server = ("192.168.88.1", 53)
txid = random.randint(0, 0xFFFF)
header = struct.pack("!HHHHHH", txid, 0x0100, 1, 0, 0, 0)
qname = b""
for label in name.split("."):
    qname += bytes([len(label)]) + label.encode("ascii")
qname += b"\x00"
question = qname + struct.pack("!HH", 1, 1)
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.settimeout(5)
try:
    sock.sendto(header + question, server)
    data, _ = sock.recvfrom(4096)
except Exception as exc:
    print(f"DNS_FAIL {name} {exc}"); sys.exit(1)
finally:
    sock.close()
if len(data) < 12:
    print(f"DNS_FAIL {name} short_response"); sys.exit(1)
rid, flags, qd, an, ns, ar = struct.unpack("!HHHHHH", data[:12])
if rid != txid:
    print(f"DNS_FAIL {name} txid_mismatch"); sys.exit(1)
if (flags >> 15) & 1 != 1:
    print(f"DNS_FAIL {name} not_response"); sys.exit(1)
if flags & 0x000F != 0:
    print(f"DNS_FAIL {name} rcode={flags & 0x000F}"); sys.exit(1)
if an < 1:
    print(f"DNS_FAIL {name} ancount=0"); sys.exit(1)
print(f"DNS_OK {name} ancount={an}")
PY
}

dns_example="$(dns_check example.com 2>&1 || true)"
printf 'DNS_EXAMPLE=%s\n' "$(printf '%s' "$dns_example" | tr '\n' ' ')"
dns_yandex="$(dns_check yandex.ru 2>&1 || true)"
printf 'DNS_YANDEX=%s\n' "$(printf '%s' "$dns_yandex" | tr '\n' ' ')"
REMOTE
)

if [[ -n "$TARGET" ]]; then
	log "проверка ${TARGET} (только чтение)"
	remote_out="$(ssh "${SSH_OPTS[@]}" "$TARGET" 'bash -s' <<< "$CHECKS")" \
		|| die "не удалось выполнить проверку по SSH"
else
	log "проверка локально (только чтение)"
	remote_out="$(bash -s <<< "$CHECKS")" \
		|| die "не удалось выполнить проверку локально"
fi

active=""; enabled=""; nrestarts=""
proxy_status_rc=""; proxy_status=""
proxy_list_rc=""; proxy_list=""
ipaddr=""; nmcli=""
trace_ip=""; trace_loc=""; yandex_code=""
dns_example=""; dns_yandex=""

while IFS='=' read -r key value; do
	case "$key" in
		ACTIVE)          active="$value" ;;
		ENABLED)         enabled="$value" ;;
		NRESTARTS)       nrestarts="$value" ;;
		PROXY_STATUS_RC) proxy_status_rc="$value" ;;
		PROXY_STATUS)    proxy_status="$value" ;;
		PROXY_LIST_RC)   proxy_list_rc="$value" ;;
		PROXY_LIST)      proxy_list="$value" ;;
		IPADDR)          ipaddr="$value" ;;
		NMCLI)           nmcli="$value" ;;
		TRACE_IP)        trace_ip="$value" ;;
		TRACE_LOC)       trace_loc="$value" ;;
		YANDEX_CODE)     yandex_code="$value" ;;
		DNS_EXAMPLE)     dns_example="$value" ;;
		DNS_YANDEX)      dns_yandex="$value" ;;
	esac
done <<< "$remote_out"

printf '\n Служба \n'
printf 'active=%s enabled=%s NRestarts=%s\n' "${active:-?}" "${enabled:-?}" "${nrestarts:-?}"

printf '\n Узлы (proxy status) \n'
printf '%s\n' "${proxy_status:-<пусто>}"

printf '\n Список узлов (proxy list) \n'
printf '%s\n' "${proxy_list:-<пусто>}"

printf '\n Адреса \n'
printf '%s\n' "${ipaddr:-<пусто>}"

printf '\n Устройства (nmcli) \n'
printf '%s\n' "${nmcli:-<пусто>}"

printf '\n Внешний IP \n'
printf 'ip=%s loc=%s\n' "${trace_ip:-?}" "${trace_loc:-?}"

printf '\n HTTPS \n'
printf 'yandex.ru HTTP=%s\n' "${yandex_code:-?}"

printf '\n DNS для клиентов (192.168.88.1:53) \n'
printf 'example.com: %s\n' "${dns_example:-<нет ответа>}"
printf 'yandex.ru:   %s\n' "${dns_yandex:-<нет ответа>}"

fail=0
warn=0

if [[ "$active" != "active" ]]; then
	log "FAIL: служба не active (${active:-?})"; fail=1
fi
if [[ "$enabled" != "enabled" ]]; then
	log "FAIL: служба не enabled (${enabled:-?})"; fail=1
fi
if [[ "$proxy_status_rc" != "0" ]]; then
	log "FAIL: enter-box proxy status завершился с ошибкой"; fail=1
fi
if [[ "$proxy_list_rc" != "0" ]]; then
	log "FAIL: enter-box proxy list завершился с ошибкой"; fail=1
fi
if [[ "$dns_example" != DNS_OK* ]]; then
	log "FAIL: DNS example.com через 192.168.88.1:53"; fail=1
fi
if [[ "$dns_yandex" != DNS_OK* ]]; then
	log "FAIL: DNS yandex.ru через 192.168.88.1:53"; fail=1
fi
if [[ ! "$yandex_code" =~ ^[23][0-9][0-9]$ ]]; then
	log "FAIL: HTTPS yandex.ru вернул ${yandex_code:-?}"; fail=1
fi
if [[ -z "$trace_ip" || -z "$trace_loc" ]]; then
	log "FAIL: не удалось получить внешний IP"; fail=1
fi

if [[ "$proxy_status" == *"ВНИМАНИЕ: VPN"* ]]; then
	log "WARNING: активен режим direct — интернет работает без VPN"
	warn=1
fi

if (( fail )); then
	printf '\nRESULT=FAIL\n'; exit 1
fi
if (( warn )); then
	printf '\nRESULT=WARNING_DIRECT\n'; exit 3
fi

printf '\nRESULT=VPN_OK\n'
exit 0