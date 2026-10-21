#!/usr/bin/env bash
# deploy.sh — сверка и установка enter-box на ARM64-плате.
#
#   ./deploy/deploy.sh --check [--host user@host] [--binary PATH]
#   ./deploy/deploy.sh --apply --host user@host --wan IF --lan IF
#
# --check без --host проверяет локальную машину. --apply требует --host,
# --wan и --lan. Ключ должен уже лежать на плате по пути --key (по
# умолчанию /etc/enter-box/premium.vpn) и не перезаписывается.
# Содержимое ключа не читается и не печатается.

set -Eeuo pipefail

PROG="$(basename "$0")"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

MODE="check"
HOST=""
WAN=""
LAN=""
REMOTE_KEY="/etc/enter-box/premium.vpn"
LOCAL_BIN="${PROJECT_DIR}/dist/enter-box-linux-arm64"
REMOTE_DEPLOY_DIR="/tmp/enter-box-deploy"

log() { printf '[%s] %s\n' "$PROG" "$*" >&2; }
die() { printf '[%s] ERROR: %s\n' "$PROG" "$*" >&2; exit 1; }

usage() {
	cat >&2 <<EOF
Usage:
  $PROG --check [--host user@host] [--binary PATH]
  $PROG --apply --host user@host --wan IF --lan IF [--key PATH] [--binary PATH]

  --check          Сверка sha256 и статуса (по умолчанию)
  --apply          Установка на плату
  --host           user@host платы; без него --check идёт локально
  --wan            WAN-интерфейс (обязателен для --apply)
  --lan            LAN-интерфейс (обязателен для --apply)
  --key            Путь к ключу НА ПЛАТЕ (по умолчанию $REMOTE_KEY)
  --binary         Локальный ARM64-бинарник (по умолчанию $LOCAL_BIN)
EOF
	exit 2
}

while (( $# )); do
	case "$1" in
		--check)   MODE="check"; shift ;;
		--apply)   MODE="apply"; shift ;;
		--host)    HOST="${2:-}"; shift 2 ;;
		--wan)     WAN="${2:-}"; shift 2 ;;
		--lan)     LAN="${2:-}"; shift 2 ;;
		--key)     REMOTE_KEY="${2:-}"; shift 2 ;;
		--binary)  LOCAL_BIN="${2:-}"; shift 2 ;;
		-h|--help) usage ;;
		*) die "unknown argument: $1" ;;
	esac
done

# общее

[[ -f "$LOCAL_BIN" ]] || die "локальный бинарник не найден: $LOCAL_BIN"
local_sha="$(sha256sum -- "$LOCAL_BIN" | awk '{print $1}')"
[[ -n "$local_sha" ]] || die "не удалось вычислить sha256"

SSH_OPTS=(
	-o BatchMode=yes
	-o ConnectTimeout=8
	-o StrictHostKeyChecking=accept-new
)

REMOTE_BODY=$(cat <<'REMOTE'
set -u
sha=""
if [ -f /usr/local/bin/enter-box ]; then
	sha="$(sha256sum /usr/local/bin/enter-box 2>/dev/null | awk '{print $1}')"
fi
active="$(systemctl is-active enter-box.service 2>/dev/null || true)"
enabled="$(systemctl is-enabled enter-box.service 2>/dev/null || true)"
proxy="fail"
if /usr/local/bin/enter-box proxy status >/dev/null 2>&1; then
	proxy="ok"
fi
printf 'SHA=%s\n' "$sha"
printf 'ACTIVE=%s\n' "$active"
printf 'ENABLED=%s\n' "$enabled"
printf 'PROXY=%s\n' "$proxy"
REMOTE
)

read_state() {
	if [[ -n "$HOST" ]]; then
		ssh "${SSH_OPTS[@]}" "$HOST" 'bash -s' <<< "$REMOTE_BODY"
	else
		bash -s <<< "$REMOTE_BODY"
	fi
}

parse_state() {
	STATE_SHA=""; STATE_ACTIVE=""; STATE_ENABLED=""; STATE_PROXY=""
	while IFS='=' read -r key value; do
		case "$key" in
			SHA)     STATE_SHA="$value" ;;
			ACTIVE)  STATE_ACTIVE="$value" ;;
			ENABLED) STATE_ENABLED="$value" ;;
			PROXY)   STATE_PROXY="$value" ;;
		esac
	done
}

healthy() {
	[[ "$STATE_SHA" == "$local_sha" \
	   && "$STATE_ACTIVE" == "active" \
	   && "$STATE_ENABLED" == "enabled" \
	   && "$STATE_PROXY" == "ok" ]]
}

# check

if [[ "$MODE" == "check" ]]; then
	log "цель: ${HOST:-локально}"
	state="$(read_state)" || die "не удалось прочитать состояние"
	parse_state <<< "$state"

	log "локальный sha256:  $local_sha"
	log "удалённый sha256:  ${STATE_SHA:-<нет>}"
	log "сервис: active=${STATE_ACTIVE:-?} enabled=${STATE_ENABLED:-?} proxy=${STATE_PROXY:-?}"

	if healthy; then
		printf 'ALREADY_INSTALLED_OK\n'
		exit 0
	fi
	log "расхождение: бинарник или сервис не совпадают"
	exit 1
fi

# --- apply ---

[[ -n "$HOST" ]] || die "--apply требует --host"
[[ -n "$WAN" && -n "$LAN" ]] || die "--apply требует --wan и --lan"
[[ "$WAN" != "$LAN" ]] || die "WAN и LAN должны различаться"

log "проверка sudo -n на ${HOST}"
ssh "${SSH_OPTS[@]}" "$HOST" 'sudo -n true' >/dev/null 2>&1 \
	|| die "sudo без пароля недоступен для ${HOST}; изменения не выполнялись"

state="$(read_state)" || true
parse_state <<< "$state"
if healthy; then
	printf 'ALREADY_INSTALLED_OK\n'
	exit 0
fi

log "подготовка ${HOST}:${REMOTE_DEPLOY_DIR}"
ssh "${SSH_OPTS[@]}" "$HOST" "mkdir -p '${REMOTE_DEPLOY_DIR}/deploy/systemd'"

log "копирование файлов"
scp "${SSH_OPTS[@]}" -- "$LOCAL_BIN" \
	"${HOST}:${REMOTE_DEPLOY_DIR}/enter-box-linux-arm64"
scp "${SSH_OPTS[@]}" -- "${SCRIPT_DIR}/install.sh" \
	"${HOST}:${REMOTE_DEPLOY_DIR}/deploy/install.sh"
scp "${SSH_OPTS[@]}" -- "${SCRIPT_DIR}/enter-box.router.yaml" \
	"${HOST}:${REMOTE_DEPLOY_DIR}/deploy/enter-box.router.yaml"
scp "${SSH_OPTS[@]}" -- "${SCRIPT_DIR}/systemd/enter-box.service" \
	"${HOST}:${REMOTE_DEPLOY_DIR}/deploy/systemd/enter-box.service"

log "запуск installer на ${HOST}"
ssh "${SSH_OPTS[@]}" "$HOST" "sudo -n bash '${REMOTE_DEPLOY_DIR}/deploy/install.sh' \
	--wan '${WAN}' --lan '${LAN}' \
	--key-file '${REMOTE_KEY}' \
	--binary '${REMOTE_DEPLOY_DIR}/enter-box-linux-arm64'"

log "проверка результата"
final_state="$(read_state)" || die "не удалось проверить состояние после установки"
parse_state <<< "$final_state"
[[ "$STATE_SHA" == "$local_sha" ]] || die "sha256 после установки не совпадает: ${STATE_SHA:-<нет>}"
[[ "$STATE_ACTIVE" == "active" ]] || die "сервис не active: ${STATE_ACTIVE:-?}"
[[ "$STATE_ENABLED" == "enabled" ]] || die "сервис не enabled: ${STATE_ENABLED:-?}"
[[ "$STATE_PROXY" == "ok" ]] || die "proxy status не ok"

printf 'DEPLOY_OK\n'