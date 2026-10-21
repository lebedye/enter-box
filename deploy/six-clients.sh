#!/usr/bin/env bash
# six-clients.sh — обратимый тест форвардинга enter-box через шесть
# клиентских netns, подключённых к временному Linux-мосту.
#
# ВАЖНО: тест проверяет ТОЛЬКО форвардинг (nftables forward + NAT + маршрут
# в WAN). DHCP, физический коммутатор и пропускная способность НЕ тестируются.
#
# Требования: root, iproute2, nftables, curl, mktemp.
# Все созданные ресурсы удаляются по trap (в т.ч. при сигнале/частичной ошибке).
#
# Использование:
#   sudo bash deploy/tests/six-clients.sh --wan IF

set -u
set -o pipefail

BRIDGE="eb-check-br"
BRIDGE_ADDR="192.168.89.1/24"
SUBNET="192.168.89.0/24"
NS_PREFIX="eb-check-"
VETH_PREFIX="ebc-h"
N_CLIENTS=6
NFT_TABLE="enter_box"
NFT_TEST_TABLE="eb_check_test"
NFT_COMMENT="eb-check"
CURL_MAX_TIME=20
TRACE_URL="https://1.1.1.1/cdn-cgi/trace"

CREATED_NS=()
CREATED_VETH=()
BRIDGE_CREATED=0
NFT_RULE_HANDLE=""
NFT_RULE_INSERTED=0
NFT_TABLE_CREATED=0
TMPDIR_RUN=""
EXIT_CODE=0
pids=()

WAN=""
if [[ $# -ne 2 || "$1" != --wan ]]; then
    echo 'usage: bash six-clients.sh --wan IF' >&2
    exit 2
fi
WAN="$2"
[[ "$WAN" =~ ^[a-zA-Z0-9_.:-]+$ ]] || exit 2

log()  { printf '[verify] %s\n' "$*" >&2; }
die()  { printf '[verify] ОШИБКА: %s\n' "$*" >&2; exit 1; }

require_root() {
    [ "$(id -u)" -eq 0 ] || die "скрипт должен запускаться от root"
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "не найдена команда: $1"
}

cleanup() {
    local rc=$?
    [ "$EXIT_CODE" -ne 0 ] && rc=$EXIT_CODE

    log "очистка ресурсов..."

    for pid in "${pids[@]}"; do
        kill "$pid" 2>/dev/null || true
    done

    if [ "$NFT_RULE_INSERTED" -eq 1 ] && [ -n "$NFT_RULE_HANDLE" ]; then
        if nft delete rule inet "$NFT_TABLE" forward handle "$NFT_RULE_HANDLE" 2>/dev/null; then
            log "nft-правило handle $NFT_RULE_HANDLE удалено"
        else
            log "не удалось удалить nft-правило handle $NFT_RULE_HANDLE (возможно, уже удалено)"
        fi
    fi

    if [ "$NFT_TABLE_CREATED" -eq 1 ]; then
        if nft delete table inet "$NFT_TEST_TABLE" 2>/dev/null; then
            log "nft-таблица $NFT_TEST_TABLE удалена"
        else
            log "не удалось удалить nft-таблицу $NFT_TEST_TABLE"
        fi
    fi

    local ns
    for ns in "${CREATED_NS[@]:-}"; do
        [ -n "$ns" ] || continue
        ip netns del "$ns" 2>/dev/null && log "netns $ns удалён" || true
    done

    local v
    for v in "${CREATED_VETH[@]:-}"; do
        [ -n "$v" ] || continue
        ip link del "$v" 2>/dev/null && log "veth $v удалён" || true
    done

    if [ "$BRIDGE_CREATED" -eq 1 ]; then
        ip link del "$BRIDGE" 2>/dev/null && log "мост $BRIDGE удалён" || true
    fi

    if [ -n "$TMPDIR_RUN" ] && [ -d "$TMPDIR_RUN" ]; then
        find "$TMPDIR_RUN" -mindepth 1 -maxdepth 1 -type f -delete 2>/dev/null || true
        rmdir "$TMPDIR_RUN" 2>/dev/null || true
    fi

    exit "$rc"
}

trap cleanup EXIT
trap 'EXIT_CODE=130; exit 130' INT
trap 'EXIT_CODE=143; exit 143' TERM
trap 'EXIT_CODE=129; exit 129' HUP

require_root
require_cmd ip
require_cmd nft
require_cmd curl
require_cmd mktemp

ip link show "$WAN" >/dev/null 2>&1 \
    || die "WAN-интерфейс $WAN не найден (укажите существующий через --wan)"

nft list table inet "$NFT_TABLE" >/dev/null 2>&1 \
    || die "nft-таблица inet $NFT_TABLE не найдена — enter-box не запущен?"

nft list chain inet "$NFT_TABLE" forward >/dev/null 2>&1 \
    || die "цепочка inet $NFT_TABLE forward не найдена"

if ip link show "$BRIDGE" >/dev/null 2>&1; then
    die "интерфейс $BRIDGE уже существует — откажитесь от запуска"
fi

if ip -o -4 addr show | awk '{print $4}' | grep -q '^192\.168\.89\.'; then
    die "подсеть 192.168.89.0/24 уже используется на хосте"
fi
if ip route show | grep -q '192\.168\.89\.'; then
    die "маршрут в подсеть 192.168.89.0/24 уже существует"
fi

for i in $(seq 1 "$N_CLIENTS"); do
    ns="${NS_PREFIX}${i}"
    if ip netns list | awk '{print $1}' | grep -qx "$ns"; then
        die "netns $ns уже существует"
    fi
done

if nft list table inet "$NFT_TEST_TABLE" >/dev/null 2>&1; then
    die "nft-таблица inet $NFT_TEST_TABLE уже существует"
fi

TMPDIR_RUN="$(mktemp -d /tmp/eb-check.XXXXXX)" || die "не удалось создать временный каталог"
log "временный каталог: $TMPDIR_RUN"

log "создаём мост $BRIDGE ($BRIDGE_ADDR)"
ip link add name "$BRIDGE" type bridge || die "не удалось создать мост"
BRIDGE_CREATED=1
ip addr add "$BRIDGE_ADDR" dev "$BRIDGE" || die "не удалось назначить адрес мосту"
ip link set "$BRIDGE" up || die "не удалось поднять мост"

for i in $(seq 1 "$N_CLIENTS"); do
    ns="${NS_PREFIX}${i}"
    veth="${VETH_PREFIX}${i}"
    ns_ip="192.168.89.$((i + 1))"

    log "создаём netns $ns (eth0=$ns_ip) и veth $veth"

    ip netns add "$ns" || die "не удалось создать netns $ns"
    CREATED_NS+=("$ns")

    ip link add "$veth" type veth peer name eth0 netns "$ns" \
        || die "не удалось создать veth $veth"
    CREATED_VETH+=("$veth")

    ip link set "$veth" master "$BRIDGE" || die "не удалось подключить $veth к мосту"
    ip link set "$veth" up || die "не удалось поднять $veth"

    ip -n "$ns" link set lo up || die "не удалось поднять lo в $ns"
    ip -n "$ns" addr add "$ns_ip/24" dev eth0 || die "не удалось назначить адрес в $ns"
    ip -n "$ns" link set eth0 up || die "не удалось поднять eth0 в $ns"
    ip -n "$ns" route add default via 192.168.89.1 || die "не удалось добавить default в $ns"
done

# Вставляем в начало forward-цепочки, чтобы гарантированно разрешить трафик
# с моста (даже если в enter_box есть более ранние drop-правила).
log "вставляем временное правило forward для $BRIDGE"
nft insert rule inet "$NFT_TABLE" forward iifname "$BRIDGE" accept comment "$NFT_COMMENT" \
    || die "не удалось вставить nft-правило"
NFT_RULE_INSERTED=1

NFT_RULE_HANDLE="$(
    nft -a list chain inet "$NFT_TABLE" forward \
    | awk -v c="\"$NFT_COMMENT\"" '
        $0 ~ "comment " c { for (i = 1; i <= NF; i++) if ($i == "handle") { print $(i+1); exit } }
    '
)"
[[ "$NFT_RULE_HANDLE" =~ ^[0-9]+$ ]] || die "не удалось получить числовой handle вставленного nft-правила"
log "nft-правило handle=$NFT_RULE_HANDLE"

log "создаём временную NAT-таблицу inet $NFT_TEST_TABLE"
nft add table inet "$NFT_TEST_TABLE" || die "не удалось создать nft-таблицу"
NFT_TABLE_CREATED=1

nft add chain inet "$NFT_TEST_TABLE" postrouting \
    '{ type nat hook postrouting priority srcnat; policy accept; }' \
    || die "не удалось создать postrouting-цепочку"
nft add rule inet "$NFT_TEST_TABLE" postrouting \
    ip saddr "$SUBNET" oifname "$WAN" masquerade \
    || die "не удалось добавить masquerade-правило"

log "запускаем $N_CLIENTS параллельных запросов к $TRACE_URL"

pids=()
for i in $(seq 1 "$N_CLIENTS"); do
    ns="${NS_PREFIX}${i}"
    out="$TMPDIR_RUN/client${i}.out"
    err="$TMPDIR_RUN/client${i}.err"
    (
        ip netns exec "$ns" \
            curl -sS --max-time "$CURL_MAX_TIME" "$TRACE_URL" \
            >"$out" 2>"$err"
        echo $? >"$TMPDIR_RUN/client${i}.rc"
    ) &
    pids+=("$!")
done

for pid in "${pids[@]}"; do
    wait "$pid" 2>/dev/null || true
done

failures=0
for i in $(seq 1 "$N_CLIENTS"); do
    rc_file="$TMPDIR_RUN/client${i}.rc"
    out_file="$TMPDIR_RUN/client${i}.out"
    err_file="$TMPDIR_RUN/client${i}.err"

    rc="$(cat "$rc_file" 2>/dev/null || echo 1)"
    ip_field="$(awk -F= '/^ip=/{print $2; exit}' "$out_file" 2>/dev/null || true)"

    if [ "$rc" = "0" ] && [ -n "$ip_field" ]; then
        printf 'client%d ip=%s\n' "$i" "$ip_field"
    else
        printf 'client%d FAIL (rc=%s)\n' "$i" "$rc"
        if [ -s "$err_file" ]; then
            sed 's/^/    /' "$err_file" >&2
        fi
        failures=$((failures + 1))
    fi
done

if [ "$failures" -ne 0 ]; then
    log "провалено клиентов: $failures из $N_CLIENTS"
    EXIT_CODE=1
    exit 1
fi

log "все $N_CLIENTS клиентов успешно прошли через форвардинг"
EXIT_CODE=0
exit 0