# enter-box

VPN-шлюз для ARM64-платформ (Raspberry Pi, Orange Pi). Предоставляет LAN-клиентам маршрутизацию через VPN с разделением трафика по доменам и IP-подсетям, встроенные DHCP и DNS, автоматическое переключение при отказе VPN.

Один статический бинарник. Встроенный sing-box. nftables. Без веб-интерфейса.

---

## Содержание

- [Обзор](#обзор)
- [Возможности](#возможности)
- [Архитектура](#архитектура)
- [Требования](#требования)
- [Сборка](#сборка)
- [Установка](#установка)
- [Конфигурация](#конфигурация)
- [Эксплуатация](#эксплуатация)
- [CLI-справочник](#cli-справочник)
- [HTTP API](#http-api)
- [Внутреннее устройство](#внутреннее-устройство)
- [Безопасность](#безопасность)
- [Обновление и откат](#обновление-и-откат)
- [Тестирование](#тестирование)
- [Миграция со старых версий](#миграция-со-старых-версий)
- [Структура проекта](#структура-проекта)
- [Ограничения](#ограничения)
- [Диагностика](#диагностика)
- [FAQ](#faq)

---

## Обзор

`enter-box` — маршрутизатор с VPN-транспортом, работающий на ARM64 SBC. Плата выполняет роль шлюза между LAN-сегментом и внешними сетями через один или несколько VPN-туннелей.

Функциональные блоки:

| Блок | Назначение |
|---|---|
| WAN | Подключение к внешней сети (USB-модем, Ethernet-аплинк) |
| LAN | Сегмент клиентов, шлюз, DHCP, DNS |
| VPN | VLESS, VMess, AmneziaWG через sing-box |
| Маршрутизация | Разделение по доменам, IP-подсетям, geo-наборам |
| Failover | Автоматическое переключение при отказах |
| Управление | CLI, Clash API, systemd |

---

## Возможности

- Маршрутизация по правилам: `geosite`/`geoip` наборы, доменные списки, суффиксы, IP-CIDR, принудительный proxy. Правила применяются в sing-box на уровне tun-inbound.
- Мультипротокольный VPN: VLESS (REALITY, TLS, WS), VMess, AmneziaWG. Несколько узлов одновременно, выбор через URLTest.
- Автоматический failover: обнаружение отказа по реальному трафику, переключение на `direct` при недоступности всех узлов, возврат в VPN при восстановлении.
- Ручной выбор узла: selector `proxy` с сохранением между перезапусками. Автоматика не переопределяет ручной выбор.
- Встроенные DHCP и DNS: раздача адресов клиентам, ответы на A/AAAA-запросы, использование для маршрутизации по домену.
- Подписки: HTTP(S) подписки с периодическим обновлением, кэш на диске, тегирование узлов по источнику.
- Устойчивость к boot race: ожидание WAN до 3 минут перед применением конфигурации.
- Clash API: управление и мониторинг на `127.0.0.1:9090`.
- Управление через CLI: меню, статус, список, тесты, переключение, переподключение.

---

## Архитектура

```
LAN-клиенты
     │
     ├── eth0 (LAN) ─────────────┐
     │                            │
     │                     ┌──────┴───────┐
     │                     │  enter-box   │
     │                     │              │
     │                     │  nftables    │
     │                     │  DHCP        │
     │                     │  DNS         │
     │                     │  sing-box    │
     │                     │   ├ tun0     │
     │                     │   ├ proxy    │
     │                     │   ├ proxy-auto
     │                     │   ├ direct   │
     │                     │   └ awg-*    │
     │                     └──────┬───────┘
     │                            │
     │                     eth1 / usb0 (WAN)
     │                            │
     └───────────────────────── интернет
```

### Сетевой слой

NetworkManager управляет адресами интерфейсов. `enter-box` не перезапускает NetworkManager и не изменяет его профили в runtime. LAN-адрес, WAN-адрес по DHCP, дополнительные адреса хранятся в `/etc/NetworkManager/system-connections`.

`enter-box` применяет к LAN-интерфейсу адрес шлюза и запускает DHCP. Маршрут по умолчанию для LAN не устанавливается.

### DHCP

Встроенный сервер на LAN-интерфейсе. Пул адресов, шлюз и DNS настраиваются в YAML. В сегменте допускается один DHCP-сервер. При старте `enter-box` проверяет, что на порту 67 не висит чужой сервер.

### DNS

Встроенный сервер на адресе шлюза `192.168.88.1:53`. Отвечает на запросы клиентов. Используется sing-box для разрешения доменов при маршрутизации.

### Маршрутизация

sing-box в режиме tun-inbound.

| Правило | Действие |
|---|---|
| `geosite-ru` + `geoip-ru` | `direct` |
| `routing.domestic_domains` | `direct` |
| `routing.domestic_suffix` | `direct` |
| `routing.domestic_ip_cidr` | `direct` |
| `routing.force_proxy` | `proxy` |
| `routing.final` | `proxy` (по умолчанию) или `direct` |

Селекторы sing-box:

- `proxy-auto`: URLTest по всем узлам.
- `proxy`: selector, по умолчанию указывает на `proxy-auto`.
- `direct`: прямой выход через WAN.

### Селекторы и URLTest

`proxy-auto` выполняет периодические замеры задержки по узлам с интервалом `routing.urltest_interval` (по умолчанию 3 минуты). Узел с минимальной задержкой в пределах `urltest_tolerance` становится активным.

`proxy` может быть переведён на конкретный узел вручную; в этом случае URLTest не влияет на выбор.

### Failover

Монитор `monitorVPNFallback` отслеживает активный узел.

Условия запуска:

- Ошибки реальных соединений, а не периодические ICMP-пробы.
- Окно ожидания 10 секунд после первой ошибки.
- Подтверждение отказа отдельной HTTPS-пробой.

Действия:

| Состояние | Действие |
|---|---|
| WAN недоступен | Ожидание восстановления WAN, затем reconnect |
| WAN доступен, все VPN недоступны | Переключение на `direct` |
| `direct` активен, кандидат прошёл 2 проверки подряд | Возврат в VPN |
| Ручной выбор узла | Монитор не вмешивается |

При переподключении VPN SSH, LAN, DHCP, DNS не перезапускаются. Частота переподключений ограничена, не чаще 1 раза в 60 секунд.

### Рейтинг скорости

Запускается после подтверждённого отказа и аварийного восстановления. Последовательная загрузка до 512 КиБ через каждый доступный узел. Текущий узел заменяется при преимуществе более 25%. При равной скорости используется задержка. Периодического обхода нет.

### nftables

Одна таблица `inet enter_box`:

- NAT LAN в WAN;
- NAT LAN в tun0;
- forwarding;
- правила фильтрации.

`iptables` не используется. При остановке сервиса таблица удаляется.

### Boot race

Перед применением конфигурации `waitWAN` ожидает до 3 минут:

- появления WAN-интерфейса;
- назначения IPv4;
- появления маршрута по умолчанию.

При получении `SIGTERM` ожидание прерывается, код выхода 1.

### Сохранение выбора

Ручной выбор узла сохраняется между перезапусками через `selector.db` в `/var/lib/enter-box`.

---

## Требования

### Аппаратное обеспечение

- ARM64 SBC: Raspberry Pi 3B/4, Orange Pi и аналоги.
- Два сетевых интерфейса: WAN и LAN.
- MicroSD не меньше 8 ГБ.
- Блок питания с запасом по току. Для Raspberry Pi 3B рекомендуется 5,1 В / 2,5 А и короткий кабель.

### Программное обеспечение

- 64-битная Debian, Ubuntu, Armbian или Raspberry Pi OS; `uname -m` возвращает `aarch64`.
- `nftables`, `iproute2`.
- `/dev/net/tun`.
- Go 1.25+ (только для сборки из исходников).

### Сеть

- WAN с доступом в интернет.
- LAN-сегмент без второго DHCP-сервера.
- LAN-подсеть не пересекается с WAN-подсетью и VPN-подсетью.

---

## Сборка

Сборка выполняется на Linux или macOS с Go 1.25+. Кросс-компиляция ARM64 поддерживается.

```bash
git clone <repo> enter-box && cd enter-box
go mod tidy
```

### ARM64 (целевая платформа)

```bash
mkdir -p dist
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -tags with_clash_api,with_utls,with_quic \
  -o dist/enter-box-linux-arm64 ./cmd/enter-box
```

Или через `make`:

```bash
make build-arm64
```

Теги:

| Тег | Назначение |
|---|---|
| `with_clash_api` | Clash API управления |
| `with_utls` | REALITY, TLS-мимикрия |
| `with_quic` | Hysteria2, QUIC-транспорт |

### Хостовая сборка (для разработки)

```bash
make build-host
./dist/enter-box version
./dist/enter-box help
```

ARM64-бинарник на x86 не запустится, это нормально. Для проверки CLI на дев-машине используется host-сборка.

### Проверка

```bash
go vet ./...
go test ./...
```

---

## Установка

### Из готового бинарника

```bash
cd /path/to/enter-box
chmod 600 /path/to/premium.vpn

sudo bash deploy/install.sh \
  --wan eth1 \
  --lan eth0 \
  --key-file /path/to/premium.vpn \
  --binary ./dist/enter-box-linux-arm64
```

`install.sh` выполняет:

- установку зависимостей;
- резервное копирование существующих файлов с суффиксом `.bak.<timestamp>`;
- установку бинарника в `/usr/local/bin/enter-box`;
- запись `/etc/enter-box/enter-box.yaml`;
- копирование ключа в `/etc/enter-box/premium.vpn` (режим `0600`);
- настройку NetworkManager для LAN;
- регистрацию и запуск systemd-сервиса.

NetworkManager не перезапускается. При ошибке старта выполняется откат на предыдущий бинарник и резервные копии конфига, ключа и unit.

### Из исходников

```bash
sudo bash deploy/install.sh \
  --wan eth1 \
  --lan eth0 \
  --key-file /path/to/premium.vpn
```

Сборка выполняется на целевой плате. Требуется доступ в интернет и свободное место. Параллелизм сборки ограничен для плат с 1 ГБ RAM.

### С хоста по SSH

`deploy/deploy.sh` копирует бинарник и файлы установки на плату, запускает `install.sh` через `sudo -n` и сверяет sha256 после установки. Ключ должен уже лежать на плате по пути `--key` (по умолчанию `/etc/enter-box/premium.vpn`) и не перезаписывается.

```bash
./deploy/deploy.sh --apply --host user@board \
  --wan eth1 --lan eth0 \
  --binary ./dist/enter-box-linux-arm64
```

Проверка без изменений:

```bash
./deploy/deploy.sh --check --host user@board
# или локально на самой плате:
./deploy/deploy.sh --check
```

### Ручная установка

```bash
sudo apt install -y nftables iproute2
sudo install -Dm0755 ./enter-box-linux-arm64 /usr/local/bin/enter-box
sudo enter-box check
sudo enter-box setup
```

`enter-box setup` проводит интерактивную конфигурацию: интерфейсы, LAN, DHCP, ключи, подписки, маршрутизация.

### Проверка окружения перед установкой

```bash
ip -br address
ip -4 route show default
uname -m
ls -l /dev/net/tun
```

Имена интерфейсов на разных платформах отличаются: `eth0`, `end0`, `enx...`, `usb0`, `eth1`. Значения передаются в аргументах установщика и в YAML.

---

## Конфигурация

Файл `/etc/enter-box/enter-box.yaml` читается при старте. Изменения применяются через `sudo systemctl restart enter-box`.

Пример ниже соответствует `deploy/enter-box.router.yaml`; установщик подставляет `wan.name` и `lan.interface` из аргументов `--wan` и `--lan`.

```yaml
log:
  level: info

wan:
  name: eth1

lan:
  interface: eth0
  address: 192.168.88.1/24
  dhcp:
    enabled: true
    range_start: 192.168.88.100
    range_end: 192.168.88.220
    lease_time: 12h
    gateway: 192.168.88.1

tun:
  interface: tun0
  address: 10.10.10.1/24
  mtu: 1380
  stack: system

dns:
  serve: true
  domestic: ["77.88.8.8", "77.88.8.1"]
  remote:   ["1.1.1.1", "8.8.8.8"]

endpoint_files:
  - /etc/enter-box/premium.vpn

routing:
  use_ru_direct: true
  domestic_domains: []
  domestic_suffix: ["ru", "рф", "xn--p1ai"]
  domestic_ip_cidr: []
  force_proxy: []
  urltest_interval: 3m
  urltest_url: http://cp.cloudflare.com/generate_204
  urltest_tolerance: 50
  preferred: ""
  final: proxy
```

Если нужно задать endpoint или подписку прямо в YAML вместо `endpoint_files`, используются секции `endpoints` и `subscriptions`:

```yaml
endpoints:
  - "vless://..."
  - "vmess://..."
  - "vpn://..."

subscriptions:
  - url: https://provider.example/api/sub/TOKEN
    tag: provider
    user_agent: enter-box/1.0
    auto_update: 6h
```

### Параметры

| Секция | Параметр | Описание |
|---|---|---|
| `log` | `level` | `debug`, `info`, `warn`, `error` |
| `wan` | `name` | Имя WAN-интерфейса |
| `lan` | `interface` | Имя LAN-интерфейса |
| `lan` | `address` | CIDR-адрес шлюза |
| `lan.dhcp` | `enabled` | Включение DHCP-сервера |
| `lan.dhcp` | `range_start`, `range_end` | Пул адресов |
| `lan.dhcp` | `lease_time` | Время аренды |
| `lan.dhcp` | `gateway` | Шлюз, выдаваемый клиентам |
| `tun` | `interface`, `address`, `mtu`, `stack` | Параметры tun |
| `dns` | `serve` | Включение встроенного DNS |
| `dns` | `domestic`, `remote` | DNS для разных маршрутов |
| `endpoint_files` | list | Пути к файлам с ключами |
| `endpoints` | list | Ссылки `vless://`, `vmess://`, `vpn://` |
| `subscriptions` | list | URL подписок |
| `subscriptions` | `tag` | Префикс тегов узлов |
| `subscriptions` | `auto_update` | Период обновления |
| `routing` | `use_ru_direct` | Загрузка `geosite-ru` и `geoip-ru` |
| `routing` | `domestic_domains` | Домены, идущие напрямую |
| `routing` | `domestic_suffix` | Суффиксы доменов, идущих напрямую |
| `routing` | `domestic_ip_cidr` | CIDR, идущие напрямую |
| `routing` | `force_proxy` | Домены, принудительно идущие через proxy |
| `routing` | `urltest_interval` | Период URLTest |
| `routing` | `urltest_tolerance` | Допуск при выборе узла |
| `routing` | `preferred` | Прибить узел по тегу |
| `routing` | `final` | Поведение по умолчанию |

### Секреты

- Ключ провайдера хранится в отдельном файле `/etc/enter-box/premium.vpn` (root, `0600`).
- Кэш подписок: `/var/lib/enter-box/subscriptions` (`0700`, файлы `0600`).
- Секреты в YAML и журналы не пишутся.

При обновлении со старой схемы `install.sh` делает резервную копию прежнего файла с суффиксом `.bak.<timestamp>`, но не удаляет его. Если ключ раньше назывался иначе, старый файл стоит удалить вручную, чтобы он не остался рядом с новым:

```bash
sudo rm -f /etc/enter-box/<старое-имя>.vpn
```

### Форматы endpoint

| Схема | Формат | Источник |
|---|---|---|
| VLESS | `vless://UUID@host:port?params#tag` | Панель провайдера |
| VMess | `vmess://base64(JSON)` | Панель провайдера |
| AmneziaWG | `vpn://base64(JSON)` | Экспорт AmneziaVPN |

---

## Эксплуатация

### Управление сервисом

```bash
sudo systemctl status enter-box
sudo systemctl restart enter-box
sudo systemctl stop enter-box
sudo systemctl enable enter-box
sudo systemctl disable enter-box
sudo journalctl -u enter-box -f
```

### Последовательность запуска

1. Включение `ip_forward` и sysctl.
2. `waitWAN` (до 3 минут).
3. Назначение LAN-адреса.
4. Применение nftables.
5. Подъём AmneziaWG-туннелей.
6. Запуск sing-box.
7. Запуск DHCP.

### Последовательность остановки

1. Остановка DHCP.
2. Остановка sing-box.
3. Удаление nftables-таблицы.
4. Остановка AWG-туннелей.

### Проверка состояния

```bash
systemctl is-active enter-box
systemctl is-enabled enter-box
ip -br address
enter-box proxy status
sudo ss -ulpn 'sport = :67'
sudo journalctl -u enter-box -n 50 --no-pager
```

Read-only снимок состояния с проверкой сервиса, узлов, DNS, внешнего IP и HTTPS:

```bash
./deploy/check.sh --target user@board
# локально на самой плате:
./deploy/check.sh
```

Код возврата: `0` — VPN_OK, `1` — FAIL, `3` — WARNING_DIRECT (интернет без VPN).

### Проверка трафика с клиента

```bash
ip route | grep default
curl -s ifconfig.me
curl -s -o /dev/null -w '%{http_code}\n' https://gosuslugi.ru
```

Ожидается: default через `192.168.88.1`, `curl ifconfig.me` возвращает IP VPN, `gosuslugi.ru` отвечает 200 или 301.

---

## CLI-справочник

```
enter-box proxy menu
enter-box proxy status
enter-box proxy list
enter-box proxy test [TAG]
enter-box proxy switch TAG
enter-box proxy auto
enter-box proxy reconnect
```

| Команда | Назначение |
|---|---|
| `proxy menu` | Интерактивное меню узлов |
| `proxy status` | Текущий режим и активный узел |
| `proxy list` | Узлы, результат HTTPS-проб, задержки |
| `proxy test [TAG]` | Форсировать проверку |
| `proxy switch TAG` | Ручной выбор узла |
| `proxy auto` | Возврат к URLTest |
| `proxy reconnect` | Переподключение VPN |

`proxy reconnect` не перезапускает LAN, DHCP, DNS, SSH. Частота — не более одного раза в 60 секунд.

### Меню `proxy menu`

| Ввод | Действие |
|---|---|
| номер | Ручной выбор узла |
| `r` | Обновление списка и проб |
| `a` | Возврат к автовыбору |
| `p` | Переподключение |
| `q` | Выход |

Столбец `ALIVE` показывает результат двух HTTPS-проб, а не ICMP-пинг. Список сохранённых серверов не эквивалентен списку доступных.

---

## HTTP API

Clash API на `127.0.0.1:9090`.

### Активный узел

```bash
curl -s http://127.0.0.1:9090/proxies/proxy | jq .now
```

### Список узлов

```bash
curl -s http://127.0.0.1:9090/proxies | jq -r '
  .proxies | to_entries[]
  | select(.value.type=="VLESS" or .value.type=="VMess")
  | "\(.key)\t\(.value.alive // "?")\t\(.value.history[-1].delay // "-")ms"
'
```

### Активные соединения

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[] | {host: .metadata.host, chain: .chains}
'
```

### Переключение узла

```bash
curl -X PUT http://127.0.0.1:9090/proxies/proxy -d '{"name":"node2"}'
curl -X PUT http://127.0.0.1:9090/proxies/proxy -d '{"name":"proxy-auto"}'
```

### Форсировать URLTest

```bash
curl -s 'http://127.0.0.1:9090/proxies/proxy-auto/delay?timeout=5000&url=http://cp.cloudflare.com/generate_204'
```

---

## Внутреннее устройство

| Пакет | Ответственность |
|---|---|
| `cmd/enter-box` | Точка входа, разбор CLI, запуск `run` и `proxy` команд |
| `internal/config` | Парсинг и валидация YAML, миграция старых полей |
| `internal/netcfg` | `waitWAN`, nftables, sysctl, адреса LAN, интерфейсы |
| `internal/singbox` | Генерация конфига sing-box, failover, urltest, selector, `selector.db` |
| `internal/dhcp` | DHCP-сервер, пул адресов, аренды |
| `internal/dns` | DNS-сервер для клиентов на адресе шлюза |
| `internal/awg` | Подъём AmneziaWG-туннелей из `vpn://`, `awg-<tag>` интерфейсы |
| `internal/subscriptions` | Загрузка подписок, кэш, тегирование узлов |

### Поток данных

1. `config.Load` читает YAML, валидирует, разрешает `endpoint_files`.
2. `subscriptions.Load` обновляет подписки и кэш.
3. `awg.Up` поднимает AWG-туннели, если есть `vpn://`.
4. `singbox.Build` собирает конфиг, запускает sing-box с tun-inbound.
5. `dhcp.Start` и `dns.Start` поднимают серверы для клиентов.
6. `singbox.MonitorVPNFallback` запускается в фоне.
7. `netcfg.Apply` накатывает nftables и sysctl.

### Завершение

По `SIGTERM` контекст отменяется, `waitWAN` прерывается, сервисы останавливаются в обратном порядке: DHCP, DNS, sing-box, AWG, nftables.

---

## Безопасность

### Файлы

| Путь | Права | Содержимое |
|---|---|---|
| `/etc/enter-box/enter-box.yaml` | `0600` | Конфиг без секретов |
| `/etc/enter-box/premium.vpn` | `0600`, root | Ключ провайдера |
| `/etc/enter-box/backup-<stamp>/networkmanager` | `0700` | Резервные копии NM-профилей |
| `/var/lib/enter-box` | `0700` | `selector.db`, состояние |
| `/var/lib/enter-box/subscriptions` | `0700`, файлы `0600` | Кэш подписок |
| `/run/enter-box-control` | `0755` | Блокировка выбора для SSH-клиента |

Секреты не попадают в YAML, журналы и историю shell. `install.sh` не печатает содержимое ключа.

### systemd

Юнит работает с минимальными capabilities:

| Capability | Назначение |
|---|---|
| `CAP_NET_ADMIN` | nftables, интерфейсы, адреса |
| `CAP_NET_BIND_SERVICE` | DHCP на порту 67, DNS на 53 |
| `CAP_NET_RAW` | Сырые сокеты для AWG |

`NoNewPrivileges=true`, `StateDirectory=enter-box`, `RuntimeDirectory=enter-box-control` с `RuntimeDirectoryPreserve=yes`.

### Сеть

- DHCP проверяет, что на порту 67 нет чужого сервера.
- NetworkManager остаётся владельцем LAN-адреса, `enter-box` не отбирает интерфейсы.
- `iptables` не используется, только `nftables` в отдельной таблице `inet enter_box`.

---

## Обновление и откат

### Обновление через install.sh

Повторный запуск `install.sh` с теми же аргументами обновляет систему:

- бинарник заменяется атомарно через `.new` и `mv -f`;
- предыдущий бинарник сохраняется как `.prev.<timestamp>`;
- конфиг, ключ и unit копируются с суффиксом `.bak.<timestamp>`;
- NetworkManager-профили копируются в `/etc/enter-box/backup-<stamp>/networkmanager`.

При ошибке старта `install.sh` автоматически откатывается на предыдущий бинарник и бэкапы, перезапускает сервис и сообщает об откате.

### Ручной откат

```bash
sudo systemctl stop enter-box
sudo mv /usr/local/bin/enter-box.prev.<timestamp> /usr/local/bin/enter-box
sudo cp /etc/enter-box/enter-box.yaml.bak.<timestamp> /etc/enter-box/enter-box.yaml
sudo systemctl daemon-reload
sudo systemctl start enter-box
```

### Обновление только бинарника

```bash
sudo systemctl stop enter-box
sudo install -m0755 ./dist/enter-box-linux-arm64 /usr/local/bin/enter-box
sudo systemctl start enter-box
```

### Обновление геозон

sing-box обновляет `.srs` раз в 24 часа. Принудительно через рестарт:

```bash
sudo systemctl restart enter-box
```

---

## Тестирование

### Go-тесты

```bash
go test ./...
go vet ./...
```

Тесты запускаются на самой плате (ARM64), а не только на дев-машине.

### Проверка форвардинга (6 netns-клиентов)

```bash
sudo bash deploy/tests/six-clients.sh --wan eth1
```

Создаёт временный мост и шесть network namespace, делает параллельные HTTPS-запросы, проверяет, что все получили IP VPN, и удаляет тестовые ресурсы. Это проверка форвардинга, а не шести физических ПК и не замер скорости.

### Изолированный тест waitWAN

```bash
sudo bash deploy/tests/wait-wan.sh
```

Запускает бинарник в изолированном netns без WAN, проверяет, что он ждёт появления интерфейса и корректно завершается по `SIGTERM`. Реальную сеть и модем не трогает.

### Тест автоматики failover

```bash
sudo python3 deploy/tests/failover.py
```

По умолчанию скрипт читает `/var/lib/enter-box/subscriptions/preflight.json`. Если preflight лежит в другом месте, путь задаётся через переменную окружения:

```bash
sudo PREFLIGHT=/path/to/preflight.json python3 deploy/tests/failover.py
```

Проверяет, что короткий обрыв не меняет узел, а длинный приводит к переключению не раньше 10 секунд и не на `direct`. Свою nft-таблицу удаляет в `finally`.

### Проверка перед перезагрузкой

```bash
./deploy/check.sh --target user@board
```

Read-only, коды возврата 0, 1, 3. Ничего не меняет.

---

## Миграция со старых версий

### Ключ `amnezia-premium.vpn` в `premium.vpn`

`install.sh` копирует новый ключ в `/etc/enter-box/premium.vpn`, старый файл остаётся как `.bak.<timestamp>` и не удаляется. После проверки работоспособности старый файл можно удалить:

```bash
sudo rm -f /etc/enter-box/amnezia-premium.vpn
sudo rm -f /etc/enter-box/amnezia-premium.vpn.bak.*
```

### NetworkManager: `99-enter-box-unmanaged.conf`

Старая схема переводила LAN в unmanaged. `install.sh` удаляет этот файл, делает `nmcli general reload conf` и возвращает `managed yes`, сохраняя SSH-сессию через `reapply`.

### Старые тестовые скрипты

Старые имена `verify-wan-wait.sh`, `verify-six-clients.sh`, `verify-auto-failover.py`, `pre-reboot-check.sh` заменены на `deploy/tests/wait-wan.sh`, `deploy/tests/six-clients.sh`, `deploy/tests/failover.py`, `deploy/check.sh`. Старые файлы можно удалить.

### WAN `usb0` в `eth1`

При смене модема меняется только `wan.name` в YAML и аргумент `--wan` в `install.sh`. Nftables-таблица строится из YAML, `iptables` не используется.

---

## Структура проекта

```
cmd/enter-box/           CLI и точка входа
internal/
  config/                Парсинг и валидация YAML
  netcfg/                nftables, sysctl, интерфейсы, waitWAN
  singbox/               Конфиг sing-box, failover, urltest, selector
  dhcp/                  DHCP-сервер
  dns/                   DNS-сервер для клиентов
  awg/                   AmneziaWG-туннели
  subscriptions/         Загрузка и кэш подписок
deploy/
  install.sh             Установка на плате (root)
  deploy.sh              Установка с хоста по SSH и --check
  check.sh               Read-only снимок состояния
  enter-box.router.yaml  Шаблон конфига
  systemd/
    enter-box.service
  tests/
    wait-wan.sh          Изолированный тест waitWAN
    failover.py          Тест автоматики failover
    six-clients.sh       Тест форвардинга (6 netns-клиентов)
docs/                    Инструкции и отчёты диагностики
```

---

## Ограничения

| Область | Состояние |
|---|---|
| Питание | Зафиксированы `Undervoltage detected` и `get_throttled = 0x50005`. Замена БП не устранила просадки. Требуется короткий качественный кабель и длительная проверка |
| USB-модем | Наблюдались множественные USB-disconnect за одно измерение. Корреляция с просадками питания присутствует. Причинно-следственная связь не установлена |
| Длительная стабильность | Проверки в течение минут. Круглосуточная стабильность не подтверждена |
| Полный ребут | Фикс `waitWAN` подтверждён изолированным тестом. Полный `reboot` с текущей сборкой не выполнялся |
| Мультиклиентная нагрузка | Проверена на netns-клиентах. Шесть физических ПК не проверялись |
| Пропускная способность | Не измерялась |
| Выбор страны на стороне API | Клиент не передаёт параметр выбора страны. Возможность на стороне провайдера не проверялась |
| Рейтинг скорости | Оценка на 512 КиБ. Не является тарифной скоростью |

---

## Диагностика

### Сервис не стартует

```bash
sudo journalctl -u enter-box -n 50 --no-pager
```

| Сообщение | Причина | Действие |
|---|---|---|
| `config load: yaml parse` | Опечатка в YAML | `enter-box run -c /etc/enter-box/enter-box.yaml -dump` |
| `config load: wan.name is required` | Пустой `wan.name` | Указать интерфейс |
| `netcfg apply: sysctl` | Нет прав | Запуск под `sudo` |
| `netcfg apply: nft apply` | Остатки старой таблицы | `sudo nft list ruleset`; при необходимости `sudo nft delete table inet enter-box` |
| `netcfg apply: set LAN addr` | Интерфейс занят | Отключить конфликтующий менеджер |
| `dhcp init: invalid dhcp range` | Пул вне подсети | Сверить `lan.address` и `lan.dhcp.range_*` |
| `awg up ...` | Параметры AWG | `journalctl -u enter-box | grep awg` |

### Клиенты без доступа

```bash
cat /proc/sys/net/ipv4/ip_forward
sudo nft list table inet enter-box
ip route show table all | grep tun0
sudo tcpdump -ni eth0 -c 10
sudo tcpdump -ni tun0 -c 10
```

Если на `eth0` пакеты видны, а на `tun0` нет, проблема в правилах sing-box. Смотреть `enter-box run -c /path -dump`.

### RU-домены через VPN

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[]
  | select(.metadata.host | contains("gosuslugi"))
  | {host: .metadata.host, chain: .chains, rule: .rule}
'
```

Если в `chain` есть `proxy`, проверить `use_ru_direct`, факт загрузки `geosite-ru.srs` в журнале и отсутствие домена в `force_proxy`.

### Узлы не проходят URLTest

```bash
curl -s http://127.0.0.1:9090/proxies | jq '
  .proxies | to_entries[]
  | select(.value.alive == false) | .key
'
```

Пустой список означает, что все узлы живы. Если мертвы: `ping vps`, проверить UUID/pbk/sid для VLESS, base64 для VMess, `journalctl | grep awg` для AmneziaWG.

### Полный сброс

```bash
sudo systemctl stop enter-box
sudo rm -f /etc/enter-box/enter-box.yaml
sudo nft delete table inet enter-box 2>/dev/null
sudo enter-box setup
```

---

## FAQ

### Клиент получил адрес, но интернета нет

Проверить `ip_forward`, nftables, маршрут до `tun0`. Если пакеты уходят в `tun0`, но ответа нет, смотреть `journalctl -u enter-box` на ошибки sing-box.

### RU-сайт открывается через VPN

Проверить `use_ru_direct: true`, что `domestic_suffix` содержит `ru`, `рф`, `xn--p1ai`, и что домен не в `force_proxy`. Посмотреть `chain` в `/connections`.

### Все узлы мертвы, интернет работает через direct

Это ожидаемое поведение failover. `enter-box proxy status` покажет предупреждение. После восстановления VPN автоматика вернётся в `proxy-auto` после двух успешных проверок одного кандидата.

### Ручной выбор сбрасывается после рестарта

Ручной выбор сохраняется в `selector.db`. Если сбрасывается, проверить `/var/lib/enter-box` (права `0700`) и `StateDirectory=enter-box` в unit.

### Модем не появился после ребута

`waitWAN` ждёт до 3 минут. Если WAN не появился, проверить `ip -br address`, `dmesg | tail`, `nmcli device status`. На старых версиях это давало `NRestarts=9`; в текущей версии сервис не падает, а ждёт.

### Как обновить только конфиг без пересборки

Отредактировать `/etc/enter-box/enter-box.yaml`, затем:

```bash
sudo systemctl restart enter-box
```

### Как переключиться на другой сервер

```bash
enter-box proxy menu
# или
enter-box proxy switch TAG
enter-box proxy auto   # вернуться к автовыбору
```

### Как посмотреть, через какой узел идёт конкретный запрос

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[] | {host: .metadata.host, chain: .chains}
'
```

### Как откатиться на предыдущую версию

См. раздел [Обновление и откат](#обновление-и-откат). `install.sh` сохраняет `.prev.<timestamp>` и `.bak.<timestamp>`.

### Как убрать всё и начать с нуля

```bash
sudo systemctl stop enter-box
sudo rm -f /etc/enter-box/enter-box.yaml
sudo nft delete table inet enter-box 2>/dev/null
sudo enter-box setup
```
