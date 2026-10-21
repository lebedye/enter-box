#!/usr/bin/env bash
# enter-box router installer.
#
# Собирает enter-box из локального дерева исходников (Go скачивает
# проверенные модули) либо устанавливает готовый бинарник, пишет
# защищённый конфиг и файл ключа, включает systemd-юнит.
# NetworkManager сохраняет владение постоянным LAN-адресом;
# enter-box только раздаёт DHCP и поднимает VPN.
#
# Использование:
#   sudo ./deploy/install.sh --wan eth1 --lan eth0 \
#        --key-file /root/premium.vpn [--binary /path/to/enter-box] [--dry-run]
#
# Содержимое ключа никогда не печатается.

set -Eeuo pipefail

PROG="$(basename "$0")"
# Запоминаем каталог вызова до cd: относительные --binary/--key-file
# должны разрешаться относительно него, а не корня проекта.
CALLER_PWD="$(pwd)"
# Абсолютные пути позволяют запускать установщик из любого каталога.
PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"
ETC_DIR="/etc/enter-box"
CONF_FILE="${ETC_DIR}/enter-box.yaml"
KEY_DEST="${ETC_DIR}/premium.vpn"
BIN_DEST="/usr/local/bin/enter-box"
UNIT_SRC="deploy/systemd/enter-box.service"
UNIT_DEST="/etc/systemd/system/enter-box.service"
TEMPLATE="deploy/enter-box.router.yaml"

LAN_ADDR="192.168.88.1/24"
LAN_GW="192.168.88.1"
DHCP_START="192.168.88.100"
DHCP_END="192.168.88.220"

WAN=""
LAN=""
KEY_FILE=""
BINARY=""
DRY_RUN=0

# Единая метка установки: используется и для резервных копий, и для отката.
INSTALL_STAMP="$(date +%Y%m%d-%H%M%S)"

log()  { printf '[%s] %s\n' "$PROG" "$*" >&2; }
die()  { printf '[%s] ERROR: %s\n' "$PROG" "$*" >&2; exit 1; }
run()  {
	if (( DRY_RUN )); then
		printf '[dry-run] %q ' "$@" >&2; printf '\n' >&2
	else
		"$@"
	fi
}

usage() {
	cat >&2 <<EOF
Usage: $PROG --wan IFACE --lan IFACE --key-file PATH [--binary PATH] [--dry-run]

  --wan IFACE       WAN-интерфейс (не изменяется, например eth1)
  --lan IFACE       LAN-интерфейс (NetworkManager manual IPv4)
  --key-file PATH   Конфиг провайдера (копируется в $KEY_DEST, режим 0600)
  --binary PATH     Готовый бинарник enter-box (без сборки из исходников)
  --dry-run         Печать действий без изменений в системе
EOF
	exit 2
}

while (( $# )); do
	case "$1" in
		--wan)      WAN="${2:-}"; shift 2 ;;
		--lan)      LAN="${2:-}"; shift 2 ;;
		--key-file) KEY_FILE="${2:-}"; shift 2 ;;
		--binary)   BINARY="${2:-}"; shift 2 ;;
		--dry-run)  DRY_RUN=1; shift ;;
		-h|--help)  usage ;;
		*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$WAN" && -n "$LAN" && -n "$KEY_FILE" ]] || usage
[[ "$WAN" != "$LAN" ]] || die "WAN и LAN должны различаться"

resolve_path() {
	local p="$1"
	if [[ "$p" != /* ]]; then
		p="${CALLER_PWD}/${p}"
	fi
	readlink -m -- "$p"
}
KEY_FILE="$(resolve_path "$KEY_FILE")"
if [[ -n "$BINARY" ]]; then
	BINARY="$(resolve_path "$BINARY")"
fi

# preflight 

[[ $EUID -eq 0 ]] || die "нужен root"

case "$(uname -m)" in
	x86_64|aarch64|arm64) ;;
	*) die "неподдерживаемая архитектура: $(uname -m)" ;;
esac

[[ -f /etc/os-release ]] || die "/etc/os-release отсутствует"
. /etc/os-release
case "${ID:-}${ID_LIKE:-}" in
	*debian*|*ubuntu*|*armbian*) ;;
	*) die "неподдерживаемый дистрибутив: ${PRETTY_NAME:-unknown}" ;;
esac

[[ "$WAN" =~ ^[a-zA-Z0-9_.:-]+$ && "$LAN" =~ ^[a-zA-Z0-9_.:-]+$ ]] || die "неверное имя интерфейса"
[[ -e "/sys/class/net/$WAN" ]] || die "WAN-интерфейс $WAN не найден"
[[ -e "/sys/class/net/$LAN" ]] || die "LAN-интерфейс $LAN не найден"

[[ -f "$KEY_FILE" ]] || die "файл ключа не найден: $KEY_FILE"
[[ -s "$KEY_FILE" ]] || die "файл ключа пуст: $KEY_FILE"

[[ -f "$TEMPLATE" ]] || die "шаблон отсутствует: $TEMPLATE"
[[ -f "$UNIT_SRC" ]] || die "systemd-юнит отсутствует: $UNIT_SRC"

# Проверки до изменений сети. Системные зависимости ставятся пакетным
# менеджером дистрибутива, без исполнения загруженных shell-файлов.
# Повторная установка с готовым бинарником не требует обновления apt cache.
missing_packages=()
for package in ca-certificates iproute2 nftables; do
	if [[ "$(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true)" != 'install ok installed' ]]; then
		missing_packages+=("$package")
	fi
done
if (( ${#missing_packages[@]} )); then
	run apt-get update
	run apt-get install -y "${missing_packages[@]}"
fi
if [[ -z "$BINARY" ]] && ! command -v go >/dev/null 2>&1; then
	run apt-get install -y golang-go
fi

# Отказ, если на порту 67 уже висит чужой DHCP.
if command -v ss >/dev/null 2>&1; then
	if ss -Hulpn 'sport = :67' 2>/dev/null | grep -qv 'enter-box'; then
		die "на порту 67 уже слушает другой DHCP-сервер"
	fi
elif command -v netstat >/dev/null 2>&1; then
	if netstat -ulpn 2>/dev/null | awk '$4 ~ /:67$/ {print}' | grep -qv 'enter-box'; then
		die "на порту 67 уже слушает другой DHCP-сервер"
	fi
fi

#  build 

build_binary() {
	if (( ! DRY_RUN )); then
		command -v go >/dev/null 2>&1 || die "go не найден (установите golang или передайте --binary)"
		# Автозагрузка toolchain появилась в Go 1.21; старые Ubuntu дают 1.18.
		go_minor="$(go version | sed -n 's/.*go1\.\([0-9]*\).*/\1/p')"
		[[ -n "$go_minor" && "$go_minor" -ge 21 ]] || die "нужен Go >=1.21 или --binary с готовым бинарником"
	fi
	log "сборка enter-box (CGO disabled, GOMAXPROCS=2, -p 1)"
	# uTLS и QUIC нужны узлам REALITY и Hysteria2.
	run env CGO_ENABLED=0 GOMAXPROCS=2 GOTOOLCHAIN=auto \
		go build -mod=mod -p 1 -tags with_clash_api,with_utls,with_quic -trimpath -ldflags "-s -w" -o "$PROJECT_DIR/enter-box.install" ./cmd/enter-box
}

if [[ -n "$BINARY" ]]; then
	[[ -x "$BINARY" ]] || die "бинарник не исполняемый: $BINARY"
	log "установка готового бинарника из $BINARY"
else
	build_binary
	BINARY="$PROJECT_DIR/enter-box.install"
fi

# config

run install -d -m 0700 "$ETC_DIR"

backup_if_exists() {
	local path="$1"
	if [[ -e "$path" ]]; then
		log "резервная копия $path -> ${path}.bak.${INSTALL_STAMP}"
		run cp -a "$path" "${path}.bak.${INSTALL_STAMP}"
	fi
}

restore_if_backed_up() {
	local path="$1"
	local bak="${path}.bak.${INSTALL_STAMP}"
	if [[ -e "$bak" ]]; then
		log "восстановление $path из $bak"
		run cp -a "$bak" "$path"
	else
		log "резервной копии для $path нет; оставляем как есть"
	fi
}

backup_if_exists "$CONF_FILE"
backup_if_exists "$KEY_DEST"
backup_if_exists "$BIN_DEST"
backup_if_exists "$UNIT_DEST"

render_config() {
	local tmp
	tmp="$(mktemp)"
	# shellcheck disable=SC2016
	awk -v wan="$WAN" -v lan="$LAN" '
		/^wan:/            { print; getline; sub(/name:.*/, "name: " wan); print; next }
		/^lan:/            { print; getline; sub(/interface:.*/, "interface: " lan); print; next }
		{ print }
	' "$TEMPLATE" > "$tmp"
	install -m 0600 "$tmp" "$CONF_FILE"
	rm -f "$tmp"
}

if (( DRY_RUN )); then
	log "[dry-run] рендер $CONF_FILE из $TEMPLATE (wan=$WAN lan=$LAN)"
else
	render_config
fi

# Копируем ключ с строгими правами; содержимое не печатается.
if (( DRY_RUN )); then
	log "[dry-run] установка ключа в $KEY_DEST (режим 0600)"
else
	# При повторной установке источник может совпадать с целью.
	# -ef сравнивает inode/устройство, а не строки путей.
	if [[ ! "$KEY_FILE" -ef "$KEY_DEST" ]]; then
		install -m 0600 -o root -g root "$KEY_FILE" "$KEY_DEST"
	fi
fi

# NetworkManager LAN

if systemctl is-active --quiet NetworkManager; then
	command -v nmcli >/dev/null 2>&1 || die "NetworkManager активен, но nmcli не найден"

	# Резервная копия ВСЕХ профилей NM перед изменением.
	NM_CONN_DIR="/etc/NetworkManager/system-connections"
	if [[ -d "$NM_CONN_DIR" ]]; then
		NM_BACKUP_DIR="${ETC_DIR}/backup-${INSTALL_STAMP}/networkmanager"
		if (( DRY_RUN )); then
			log "[dry-run] копия $NM_CONN_DIR -> $NM_BACKUP_DIR (режим 0700)"
		else
			run install -d -m 0700 "$NM_BACKUP_DIR"
			run cp -a "$NM_CONN_DIR/." "$NM_BACKUP_DIR/"
			log "профили NetworkManager сохранены в $NM_BACKUP_DIR"
		fi
	fi

	# Миграция старой схемы: только файл enter-box, без перезапуска NM.
	legacy_nm=/etc/NetworkManager/conf.d/99-enter-box-unmanaged.conf
	if [[ -f "$legacy_nm" ]]; then
		backup_if_exists "$legacy_nm"
		run rm -- "$legacy_nm"
		run nmcli general reload conf
		run nmcli device set "$LAN" managed yes
	fi

	conn="$(nmcli -g GENERAL.CONNECTION device show "$LAN" 2>/dev/null || true)"
	conn="${conn//[$'\r\n']/}"
	if [[ -z "$conn" || "$conn" == "--" ]]; then
		conn="enter-box-lan-${LAN}"
		run nmcli connection add type ethernet ifname "$LAN" \
			con-name "$conn" connection.autoconnect yes
	fi

	# Сохраняем существующие IPv4-адреса (например, текущий SSH-IP).
	existing="$(ip -4 -o addr show dev "$LAN" 2>/dev/null \
		| awk '{print $4}' | grep -v '^127\.' || true)"
	addrs="$LAN_ADDR"
	while IFS= read -r a; do
		[[ -z "$a" ]] && continue
		[[ "$a" == "$LAN_ADDR" ]] && continue
		addrs="${addrs},${a}"
	done <<< "$existing"

	log "настройка профиля NetworkManager $conn на $LAN (manual IPv4, без default)"
	run nmcli connection modify "$conn" \
		connection.interface-name "$LAN" \
		connection.autoconnect yes \
		ipv4.method manual \
		ipv4.addresses "$addrs" \
		ipv4.gateway "" \
		ipv4.never-default yes \
		ipv4.ignore-auto-dns yes \
		ipv6.method disabled

	# reapply сохраняет SSH-сессию. У неактивного нового профиля — up.
	if [[ "$(nmcli -g GENERAL.STATE device show "$LAN")" == 100* ]]; then
		run nmcli device reapply "$LAN"
	else
		run nmcli connection up "$conn"
	fi
	# NM иногда применяет адрес асинхронно; runtime-адрес нужен до DHCP.
	run ip address replace "$LAN_ADDR" dev "$LAN"
else
	log "NetworkManager не активен; пропускаем настройку LAN-профиля"
fi

# systemd

# Атомарная замена бинарника: сначала кандидат .new, затем mv -f.
# Останавливать сервис заранее не нужно — замена происходит до restart.
BIN_NEW="${BIN_DEST}.new"
PREV_BIN=""
if [[ -e "$BIN_DEST" ]]; then
	PREV_BIN="${BIN_DEST}.prev.${INSTALL_STAMP}"
	log "сохранение предыдущего бинарника $BIN_DEST -> $PREV_BIN"
	run cp -a "$BIN_DEST" "$PREV_BIN"
fi
run install -m 0755 "$BINARY" "$BIN_NEW"
run mv -f "$BIN_NEW" "$BIN_DEST"
run install -m 0644 "$UNIT_SRC" "$UNIT_DEST"
run systemctl daemon-reload
run systemctl enable enter-box.service

# Ожидание готовности: Type=simple возвращает управление сразу,
# поэтому опрашиваем статус прокси до 120 секунд с шагом 2 секунды.
# Досрочный выход, если юнит перешёл в failed.
wait_ready() {
	local deadline=$(( SECONDS + 120 ))
	while (( SECONDS < deadline )); do
		if systemctl is-failed --quiet enter-box.service; then
			log "enter-box.service перешёл в failed"
			return 1
		fi
		if "$BIN_DEST" proxy status >/dev/null 2>&1; then
			log "enter-box proxy status OK"
			return 0
		fi
		sleep 2
	done
	log "enter-box не готов за 120с"
	return 1
}

# Перезапуск с откатом при ошибке. Откат восстанавливает бинарник,
# конфиг, ключ и unit из резервных копий текущей установки.
if (( DRY_RUN )); then
	log "[dry-run] перезапуск enter-box.service и ожидание готовности"
else
	if ! systemctl restart enter-box.service || ! wait_ready; then
		log "enter-box.service не запустился; последние строки журнала:"
		journalctl -u enter-box.service -n 50 --no-pager >&2 || true
		if [[ -n "$PREV_BIN" && -e "$PREV_BIN" ]]; then
			log "откат на $PREV_BIN"
			install -m 0755 "$PREV_BIN" "$BIN_NEW"
			mv -f "$BIN_NEW" "$BIN_DEST"
			restore_if_backed_up "$CONF_FILE"
			restore_if_backed_up "$KEY_DEST"
			restore_if_backed_up "$UNIT_DEST"
			systemctl daemon-reload
			if systemctl restart enter-box.service && wait_ready; then
				log "откат успешен; служба работает на предыдущем бинарнике"
				die "новая версия не запустилась; восстановлена предыдущая версия"
			else
				log "перезапуск после отката тоже не удался; журнал:"
				journalctl -u enter-box.service -n 50 --no-pager >&2 || true
				die "enter-box.service не запускается после отката"
			fi
		else
			die "enter-box.service не запустился, а предыдущего бинарника для отката нет"
		fi
	fi
fi

log "готово. Конфиг: $CONF_FILE  Ключ: $KEY_DEST  Юнит: $UNIT_DEST"
log "статус: systemctl status enter-box"