# enter-box

VPN-шлюз для Orange Pi и других Linux-устройств: **VLESS**, **VMess**,
**AmneziaWG** (формат `vpn://`), с автоматической маршрутизацией
«RU — напрямую, всё остальное — через VPN».

Разворачивается одной командой, работает как systemd-сервис,
настраивается через интерактивный визард.

```
USB0 (WAN, интернет) ─┐
                      ├──→ [enter-box] ──→ ETH0 (LAN, 192.168.88.1/24)
TUN0 (VPN)           ─┘                         │
                                                ▼
                                            switch
                                                │
                                   клиенты (ноут/телефон/ТВ)
```

## Возможности

- **VLESS** (включая Reality и XTLS-Vision), **VMess**, **AmneziaWG** —
  всё в одном процессе через `sing-box` + `amneziawg-go`.
- **Обход белых списков**: RU-сервисы (ВК, Госуслуги, Сбер, `.ru`, `.рф`)
  идут напрямую через WAN, остальное — в VPN.
- **Автовыбор узла**: `urltest` каждые 3 минуты проверяет задержки,
  сам переключается на самый быстрый рабочий узел.
- **Автообновление подписок**: скачивает свежий список раз в `auto_update`,
  применяет без перезапуска демона.
- **Встроенный DHCP-сервер** для LAN-клиентов, с диапазоном и
  исключениями под статику.
- **Встроенный DNS-форвардер**: RU-домены → Яндекс DNS напрямую,
  остальные → Cloudflare через VPN.
- **WiFi как дополнительный LAN**: раздача через AP (`hostapd`),
  поднимается один раз, дальше enter-box использует готовый интерфейс.
- **Статический IP страховкой**: systemd-юнит ставит `192.168.88.1/24`
  на `eth0` **до** старта enter-box. Если демон упадёт — SSH всё равно работает.
- **Управление в runtime**: команда `enter-box proxy` — статус, список
  узлов с задержками, форсированный тест, ручное переключение.

## Требования

**Железо:**
- Orange Pi или любой Linux-ARM64-компьютер.
- 2+ сетевых интерфейса (WAN + LAN). Для USB-модема — 3.
- MicroSD ≥ 8 ГБ.

**Софт:**
- Ubuntu 22.04 / 24.04 ARM64, Armbian или совместимый дистрибутив.
- Go 1.25.5+ (только для сборки, на целевой машине не нужен).
- `nftables`, `iproute2`, `openssh-server` — визард предложит установить.

**Ядро:**
- Модуль `tun` (`/dev/net/tun`). Если нет — `sudo modprobe tun`.

## Сборка

```bash
git clone <репозиторий> enter-box
cd enter-box

# Привести зависимости в порядок (один раз после клонирования)
go mod tidy

# Собрать под ARM64 (для Orange Pi)
make build-arm64
# → dist/enter-box-arm64

# Или под свою архитектуру (для тестов)
make build-host
# → dist/enter-box
```

Проверить бинарь:

```bash
file dist/enter-box-arm64
# ELF 64-bit LSB executable, ARM aarch64, statically linked
```

## Установка

### 1. Подготовить Orange Pi

```bash
sudo apt update && sudo apt install -y nftables iproute2 openssh-server
ls /dev/net/tun      # должен существовать
```

### 2. Скопировать бинарь с дев-машины

```bash
scp dist/enter-box-arm64 pi@192.168.1.XXX:/tmp/enter-box
```

### 3. Установить

На Orange Pi:

```bash
sudo install -m0755 /tmp/enter-box /usr/local/bin/enter-box
enter-box version
```

### 4. Проверить окружение

```bash
sudo enter-box check
```

Проверяются: root, `/dev/net/tun`, `nftables`, `iproute2`, SSH,
конфликтующие менеджеры сети (NetworkManager, systemd-networkd, dhcpcd).

### 5. Интерактивная установка

```bash
sudo enter-box setup
```

Визард проведёт через 7 шагов:

1. **Сетевые интерфейсы** — откуда интернет (WAN) и куда коммутатор (LAN).
2. **Дополнительные LAN** — WiFi (AP) или второй USB-Ethernet, опционально.
3. **LAN-адрес и DHCP** — адрес шлюза, диапазон, исключения.
4. **VPN-узлы** — `vless://`, `vmess://`, `vpn://` или URL подписки.
5. **Маршрутизация** — RU → direct, остальное → VPN, свои домены.
6. **DNS-сервер** — поднимать ли встроенный.
7. **Подтверждение** — запись конфига и создание systemd-юнитов.

Всё. Сервис включён и запущен.

## Проверка

```bash
# Служба активна
systemctl is-active enter-box

# Живой лог
sudo journalctl -u enter-box -f

# Активный узел
enter-box proxy

# Все узлы с задержками
enter-box proxy list

# Внешний IP — через VPN
curl -s ifconfig.me

# RU — напрямую
curl -s -o /dev/null -w '%{http_code}\n' https://gosuslugi.ru
```

С клиента в коммутаторе (после получения IP по DHCP):

```bash
curl -s ifconfig.me
# IP VPN-сервера

curl https://vk.com
# работает напрямую
```

## Конфигурация

Файл: `/etc/enter-box/enter-box.yaml`. Создаётся визардом, правится
руками, применяется через `sudo systemctl restart enter-box`.

### Минимальный пример

```yaml
wan:
  name: usb0

lan:
  interface: eth0
  address: 192.168.88.1/24
  dhcp:
    enabled: true
    range_start: 192.168.88.100
    range_end: 192.168.88.250

tun:
  interface: tun0
  address: 10.10.10.1/24

dns:
  serve: true
  domestic: ["77.88.8.8", "77.88.8.1"]
  remote:   ["1.1.1.1", "8.8.8.8"]

endpoints:
  - "vless://uuid@vps.example.com:443?type=tcp&security=reality&sni=...&pbk=...&sid=...&fp=chrome&flow=xtls-rprx-vision#node1"

routing:
  use_ru_direct: true
  final: proxy
```

### Секции

| Секция | Что описывает |
|---|---|
| `wan.name` | Интерфейс с интернетом (usb0, wwan0, eth1). |
| `lan.interface` | Основной LAN-интерфейс (eth0). |
| `lan.extra_interfaces` | Дополнительные LAN (wlan0, второй USB). |
| `lan.address` | IP шлюза, который получат клиенты. |
| `lan.dhcp.*` | Встроенный DHCP. `exclude` — адреса под статику. |
| `tun.*` | Виртуальный интерфейс sing-box. Трогать не нужно. |
| `dns.serve` | Поднимать ли встроенный DNS. |
| `dns.domestic` / `dns.remote` | DNS для RU- и прочих доменов. |
| `endpoints` | Массив прямых ссылок (`vless://`, `vmess://`, `vpn://`). |
| `subscriptions` | URL подписок с `auto_update`. |
| `routing.use_ru_direct` | Включить geosite-ru / geoip-ru → direct. |
| `routing.domestic_*` | Свои домены/суффиксы/IP для direct. |
| `routing.force_proxy` | Домены, которые всегда через VPN. |
| `routing.final` | По умолчанию `proxy`. Для отладки — `direct`. |

### Подписки

```yaml
subscriptions:
  - url: https://provider.com/api/sub/TOKEN
    tag: provider             # префикс к тегам: provider/node1
    user_agent: enter-box/1.0 # некоторые панели фильтруют по UA
    auto_update: 6h           # обновлять раз в 6 часов
```

`auto_update` — рабочий параметр. Раз в указанный интервал enter-box
скачивает свежий список, сравнивает с текущим, и если набор изменился —
перезапускает sing-box (AWG, nftables, DHCP, DNS не трогаются).

### AmneziaWG

Ссылка `vpn://` получается в клиенте AmneziaVPN: «Поделиться → vpn://».
enter-box:

1. Декодирует base64.
2. Извлекает INI-конфиг AWG.
3. Поднимает userspace-интерфейс `awg-<tag>` через `amneziawg-go`.
4. В sing-box добавляет `outbound type=direct bind_interface=awg-<tag>`.

Трафик, попавший в этот outbound, идёт через обфусцированный туннель.

## CLI

```
enter-box <команда> [аргументы]

setup [-c <path>]        интерактивная установка
check                    проверка окружения
interfaces               список сетевых интерфейсов
run  -c <path>           запуск демона (вручную)
run  -c <path> -dump     напечатать JSON sing-box и выйти
run  -c <path> -v        подробный лог
version                  версия
help                     справка

proxy [status|list|test|switch|auto]
wifi  [on|off|scan|connect]
```

### Управление VPN-узлами

```bash
# Активный узел
enter-box proxy

# Список всех узлов с задержками
enter-box proxy list

# Форсировать urltest прямо сейчас
enter-box proxy test

# Проверить конкретный узел
enter-box proxy test provider/node3

# Переключиться вручную
enter-box proxy switch provider/node3

# Вернуться к автовыбору
enter-box proxy auto
```

### WiFi

```bash
# Скан сетей
sudo enter-box wifi scan

# Подключение
sudo enter-box wifi connect MySSID secretpassword

# Включить / выключить радио
sudo enter-box wifi on
sudo enter-box wifi off
```

## Управление сервисом

```bash
sudo systemctl status enter-box
sudo systemctl restart enter-box
sudo systemctl stop enter-box
sudo journalctl -u enter-box -f
```

Что enter-box делает при старте:

1. Включает `ip_forward` и настраивает `rp_filter`.
2. Вешает `lan.address` на `lan.interface`.
3. Поднимает WAN и LAN.
4. Создаёт nftables-таблицу `enter_box` (forward + NAT).
5. Поднимает AWG-туннели `awg-<tag>` для всех `vpn://`.
6. Запускает sing-box (tun-inbound, urltest, selector, route-rules).
7. Запускает встроенный DNS-форвардер (если `dns.serve: true`).
8. Запускает DHCP (если `lan.dhcp.enabled: true`).

`iptables` не трогается вообще — используется только `nftables`.

## Обновление

```bash
# На дев-машине
git pull
make build-arm64
scp dist/enter-box-arm64 pi@<ipi>:/tmp/enter-box

# На Orange Pi
sudo systemctl stop enter-box
sudo install -m0755 /tmp/enter-box /usr/local/bin/enter-box
sudo systemctl start enter-box
```

Конфиг и systemd-юниты не трогаются.

## Удаление

```bash
sudo systemctl stop enter-box enter-box-lan-addr
sudo systemctl disable enter-box enter-box-lan-addr
sudo rm /etc/systemd/system/enter-box*.service
sudo systemctl daemon-reload
sudo rm /usr/local/bin/enter-box /usr/local/bin/enter-box-lan-addr
sudo nft delete table inet enter_box

# Конфиг оставляем — удаляется отдельно:
sudo rm -rf /etc/enter-box
```

## Troubleshooting

### Сервис не стартует

```bash
sudo journalctl -u enter-box -n 50 --no-pager
```

Частые причины:

| Ошибка | Что делать |
|---|---|
| `config load: yaml parse` | Опечатка в YAML. `enter-box run -c ... -dump` покажет место. |
| `netcfg apply: ...` | Интерфейс занят. Отключить NetworkManager/systemd-networkd. |
| `box.New: ...` | Несовместимость схемы sing-box. Смотрите вывод `-dump`. |
| `awg up: ...` | Неверные параметры AWG в `vpn://`. |
| `dhcp init: ...` | Диапазон вне подсети. Проверьте `lan.address` и `lan.dhcp.range_*`. |

### Клиенты не получают интернет

```bash
# Форвардинг?
cat /proc/sys/net/ipv4/ip_forward     # → 1

# Таблица nftables на месте?
sudo nft list table inet enter_box

# Трафик идёт?
sudo tcpdump -ni eth0 -c 10
sudo tcpdump -ni tun0 -c 10
```

### RU-сайты идут через VPN, а не напрямую

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[]
  | select(.metadata.host | contains("gosuslugi"))
  | {host, chains: .chains}
'
```

В `chains` должно быть `["direct"]`. Если `["proxy", ...]`:

- `use_ru_direct: false` в конфиге;
- `geosite-ru.srs` не загрузился (см. `journalctl | grep rule-set`);
- домен попал под `force_proxy`.

### Узлы мертвы

```bash
enter-box proxy list
```

Если все `fail` — серверы недоступны или неверные ключи. Проверьте
доступность с Orange Pi: `ping vps.example.com`.

### DNS не отвечает

```bash
sudo ss -tulpn | grep ':53 '
# должен быть enter-box на 0.0.0.0:53

dig @127.0.0.1 google.com
```

Если порт занят `systemd-resolved` — отключить:

```bash
sudo systemctl stop systemd-resolved
sudo systemctl disable systemd-resolved
sudo systemctl restart enter-box
```

### После setup пропал SSH

Если ты был подключён через `eth0`, IP сменился на `192.168.88.1`.
Подключись заново:

```bash
ssh pi@192.168.88.1
```

Статический IP ставит `enter-box-lan-addr.service` при загрузке,
поэтому доступ должен сохраняться даже если enter-box не стартанул.
Проверить:

```bash
systemctl status enter-box-lan-addr
```

## Архитектура

```
cmd/enter-box/          CLI: setup, check, run, proxy, wifi
internal/
  config/               YAML-конфиг + валидация + сохранение
  parser/               vless://, vmess://, vpn:// → Endpoint
  singbox/              генератор JSON sing-box + runner
  netcfg/               sysctl, nftables, userspace AWG, static IP unit
  dhcp/                 DHCPv4-сервер с exclude-списком
  dns/                  DNS-форвардер RU/не-RU
  subs/                 фоновое обновление подписок
  proxyapi/             клиент Clash API (status, list, test, switch)
  wifi/                 WiFi on/off/connect через nmcli или wpa_cli
  setup/                интерактивный визард + preflight + detect
  routing/              константы и списки RU-доменов
deploy/
  systemd/              шаблон unit-файла
  nftables/             справочная копия таблицы
docs/                   USERDOC.md, SIMPLE-README.md
```




