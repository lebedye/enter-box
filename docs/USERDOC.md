# enter-box — полный гайд по сборке и установке

Документ из трёх частей:
1. **Сборка** — на дев-машине (Linux/macOS, x86).
2. **Перенос и установка** — на Orange Pi.
3. **Конфигурация и эксплуатация** — что вписать, как проверить, что делать если не работает.

---

## Часть 0. Что понадобится

**Железо:**
- Orange Pi (ARM64), 2 сетевых интерфейса: WAN (USB-модем или Ethernet-аплинк) и LAN (Ethernet в коммутатор).
- MicroSD ≥ 8 ГБ.
- Компьютер с интернетом для сборки и удалённого доступа.

**Софт на дев-машине:**
- Go 1.25.5 или новее.
- `ssh`, `scp` для переноса.
- (Опционально) `make`.

**Софт на Orange Pi:**
- Ubuntu 22.04 / 24.04 ARM64 или Armbian.
- Пакеты: `nftables`, `iproute2`.
- `/dev/net/tun` — модуль ядра TUN.

---

## Часть 1. Сборка

### 1.1. Проверка зависимостей

```bash
go version
# go version go1.25.5 linux/amd64 — минимально требуется 1.25

git --version
ssh -V
```

### 1.2. Клонирование и подготовка

```bash
cd ~/Desktop
git clone <ваш-репозиторий> enter-box-arm
cd enter-box-arm

# Проверить структуру
tree -L 2
```

Ожидаемое дерево — как у тебя есть (35 файлов, включая `cmd/`, `internal/`, `deploy/`).

### 1.3. Установка зависимостей

```bash
go mod tidy
go get github.com/amnezia-vpn/amneziawg-go@latest
go mod tidy
```

### 1.4. Проверка сборки

```bash
# Локально (под твою архитектуру) — быстрая проверка компиляции
go build ./...
go vet ./...

# Форматирование (на всякий случай — если файлы копировались руками)
gofmt -s -w .
```

Если всё чисто — переходим к кросс-сборке.

### 1.5. Кросс-сборка под ARM64

```bash
mkdir -p dist

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o dist/enter-box-arm64 ./cmd/enter-box

file dist/enter-box-arm64
# ожидаемо: ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped

ls -lh dist/enter-box-arm64
# ~15–25 МБ (sing-box + amneziawg-go внутри)
```

Через `make`:

```bash
make build-arm64
```

### 1.6. Проверка бинаря на дев-машине

Бинарь для ARM не запустится на x86 — это нормально. Можно собрать «хостовую» версию для тестов:

```bash
make build-host
./dist/enter-box version
./dist/enter-box help
```

---

## Часть 2. Установка на Orange Pi

### 2.1. Подготовка ОС

Записать Ubuntu ARM64 на SD, включить, зайти по SSH. Дальше:

```bash
sudo apt update && sudo apt upgrade -y

sudo apt install -y \
  nftables iproute2 curl jq ca-certificates \
  tcpdump net-tools
```

Проверка TUN:

```bash
ls -l /dev/net/tun
# crw------- 1 root root 10, 200 ...  ← ожидаемо

# Если нет:
sudo modprobe tun
echo "tun" | sudo tee /etc/modules-load.d/tun.conf
```

Проверка интерфейсов — важно, чтобы WAN и LAN были под правильными именами:

```bash
ip -br link
# пример:
# lo    UNKNOWN
# eth0  UP         aa:bb:cc:dd:ee:ff     ← LAN (в коммутатор)
# usb0  DOWN       11:22:33:44:55:66     ← WAN (модем)
```

**Если usb0 не появился** — модем в неправильном режиме. Для Huawei HiLink/ZTE нужно:

```bash
lsusb                       # увидеть вендора/продукт
dmesg | tail -40            # увидеть, как ядро определило модем
```

При необходимости переключить модем в режим NCM/HiLink через `usb_modeswitch`, либо поднять вручную:

```bash
sudo ip link set usb0 up
sudo dhclient usb0 -v
ip addr show usb0
curl --interface usb0 -s ifconfig.me   # проверить, что есть интернет
```

### 2.2. Отключение конфликтующих сетевых менеджеров

`NetworkManager`, `systemd-networkd`, `dhcpcd` будут перезаписывать конфигурацию eth0/usb0, которую ставит enter-box. Отключаем:

```bash
for svc in NetworkManager systemd-networkd dhcpcd; do
  sudo systemctl stop    "$svc" 2>/dev/null
  sudo systemctl disable "$svc" 2>/dev/null
done
```

Проверить, что ничего не осталось:

```bash
systemctl is-active NetworkManager systemd-networkd dhcpcd 2>/dev/null
# inactive (или "unknown" — тогда сервис отсутствует, это ок)
```

### 2.3. Копирование бинаря

С дев-машины:

```bash
scp dist/enter-box-arm64 pi@192.168.1.XXX:/tmp/enter-box
```

На Orange Pi:

```bash
sudo install -Dm0755 /tmp/enter-box /usr/local/bin/enter-box
enter-box version
```

### 2.4. Preflight

```bash
sudo enter-box check
```

Ожидаемый вывод:

```
──────────────────────────────────────────────────────
Проверка окружения
──────────────────────────────────────────────────────
✓  запуск от root
✓  ОС Linux
✓  /dev/net/tun присутствует
✓  nftables установлен
✓  iproute2 установлен
```

Если что-то `⚠` — исправить по подсказке в строке `→`.

---

## Часть 3. Конфигурация

### 3.1. Быстрый путь — интерактивный визард

```bash
sudo enter-box setup
```

Диалог выглядит так:

```
──────────────────────────────────────────────────────
enter-box — интерактивная установка
──────────────────────────────────────────────────────
──────────────────────────────────────────────────────
Проверка окружения
──────────────────────────────────────────────────────
✓  запуск от root
✓  ОС Linux
✓  /dev/net/tun присутствует
✓  nftables установлен
✓  iproute2 установлен

──────────────────────────────────────────────────────
Шаг 1/5. Сетевые интерфейсы
──────────────────────────────────────────────────────
Найдены интерфейсы:
  eth0       [PCI,UP,default-route] mac=aa:bb:cc:dd:ee:ff addr=192.168.1.42/24
  usb0       [USB,down] mac=11:22:33:44:55:66 addr=—
  wlan0      [WiFi,down] mac=...

Куда приходит интернет (WAN)?
  * 1) eth0 ...
    2) usb0 ...
    3) wlan0 ...
Выбор [1]: 2

Какой интерфейс смотрит в коммутатор (LAN)?
  * 1) eth0 ...
    2) wlan0 ...
Выбор [1]: 1

──────────────────────────────────────────────────────
Шаг 2/5. LAN-адрес и DHCP
──────────────────────────────────────────────────────
IP-адрес шлюза для клиентов (CIDR) [192.168.88.1/24]:
Включить DHCP-сервер для клиентов? [Y/n]:
Начало диапазона DHCP [192.168.88.10]:
Конец диапазона DHCP [192.168.88.250]:

──────────────────────────────────────────────────────
Шаг 3/5. VPN-узлы
──────────────────────────────────────────────────────
Можно вставить:
  • vless://...    (VLESS + Reality/TLS/WS)
  • vmess://...    (VMess)
  • vpn://...      (AmneziaWG из клиента AmneziaVPN)

Вставьте ссылки по одной на строку. Пустая строка — закончить.
> vless://uuid@vps.example.com:443?type=tcp&security=reality&sni=...
> 
Добавить HTTP(S)-подписку (URL со списком узлов)? [y/N]: y
URL подписки (пусто — закончить): https://provider.com/api/sub/TOKEN
Короткое имя (tag) для этой подписки [sub]: provider
User-Agent (опционально) [enter-box/1.0]: 
Добавить ещё одну подписку? [y/N]: n

──────────────────────────────────────────────────────
Шаг 4/5. Маршрутизация
──────────────────────────────────────────────────────
Обход белых списков: RU → direct, остальное → VPN? [Y/n]:
Добавить свои домены, которые всегда напрямую? [y/N]:

──────────────────────────────────────────────────────
Шаг 5/5. Проверьте настройки
──────────────────────────────────────────────────────
WAN (интернет):      usb0
LAN (коммутатор):    eth0
Адрес шлюза:         192.168.88.1/24
DHCP:                192.168.88.10 — 192.168.88.250 (lease 1h0m0s)
Узлов (endpoints):   1
Подписок:            1
RU → direct:         true

Файл будет записан:  /etc/enter-box/enter-box.yaml

Записать и продолжить? [Y/n]: 
✓  конфиг записан: /etc/enter-box/enter-box.yaml
Установить systemd-сервис и включить автозапуск? [Y/n]: 
✓  служба enter-box включена и запущена
Логи:       sudo journalctl -u enter-box -f
Статус:     sudo systemctl status enter-box
Остановить: sudo systemctl stop enter-box
```

**Готово.** Система уже работает.

### 3.2. Ручной путь — редактирование YAML

Файл: `/etc/enter-box/enter-box.yaml`. Структура:

```yaml
log:
  level: info

wan:
  name: usb0

lan:
  interface: eth0
  address: 192.168.88.1/24
  dhcp:
    enabled: true
    range_start: 192.168.88.10
    range_end: 192.168.88.250
    lease_time: 1h

tun:
  interface: tun0
  address: 10.10.10.1/24
  mtu: 1420
  stack: system

dns:
  domestic: ["77.88.8.8", "77.88.8.1"]
  remote:   ["1.1.1.1", "8.8.8.8"]

endpoints:
  - "vless://..."
  - "vmess://..."
  - "vpn://..."

subscriptions:
  - url: https://provider.com/api/sub/TOKEN
    tag: provider
    user_agent: enter-box/1.0
    auto_update: 6h

routing:
  use_ru_direct: true
  domestic_domains: [vk.com, gosuslugi.ru, sberbank.ru]
  domestic_suffix: [.ru, .рф]
  domestic_ip_cidr: []
  force_proxy: []
  urltest_interval: 3m
  urltest_url: http://cp.cloudflare.com/generate_204
  urltest_tolerance: 50
  preferred: ""
  final: proxy
```

#### Разбор полей, которые чаще всего трогают

**`wan.name` / `lan.interface`** — точные имена из `ip -br link`. Если ошибётесь, при старте получите `netcfg apply: ...` в логах.

**`lan.address`** — CIDR-адрес, который Orange Pi вешает на LAN-интерфейс и отдаёт клиентам как шлюз. Убедитесь, что эта подсеть не пересекается с домашней сетью.

**`lan.dhcp.enabled`** — если у вас уже есть DHCP-сервер в сети, поставьте `false` и раздавайте адреса с него, указывая Orange Pi как gateway.

**`endpoints`** — массив строк. Поддерживаются:

| Схема | Формат | Пример |
|---|---|---|
| VLESS | `vless://UUID@host:port?params#tag` | `vless://...@vps1.com:443?type=tcp&security=reality&sni=...&pbk=...&sid=...&fp=chrome#node1` |
| VMess | `vmess://base64(JSON)` | Получаете у провайдера в личном кабинете как одну длинную строку |
| AmneziaWG | `vpn://base64(JSON)` | Экспорт из клиента AmneziaVPN: «Поделиться → vpn://» |

**`subscriptions`** — массив URL, отдающих список конфигов. Провайдер может вернуть:
- plain-текст — ссылки по одной на строку;
- base64 от текста — enter-box декодирует сам.

`tag` добавляется как префикс: получите узлы с тегами `provider/node1`, `provider/node2`. Удобно для диагностики.

**`routing.use_ru_direct: true`** — включает автозагрузку `geosite-ru.srs` и `geoip-ru.srs`. Всё, что попадает под RU-домены/IP, идёт `direct` (через usb0). Всё остальное — в VPN.

**`routing.domestic_domains` / `domestic_suffix`** — ручные RU-правила, в дополнение к geosite.

**`routing.final`** — что делать с трафиком, который не попал ни под одно правило:
- `proxy` (по умолчанию) — в VPN;
- `direct` — напрямую. Полезно для отладки (когда хотите проверить, что вообще сеть работает).

**`routing.urltest_interval`** — как часто `urltest` проверяет узлы. Меньше 3 минут ставить не стоит, чаще — просто лишний трафик.

**`routing.preferred`** — прибить конкретный узел по тегу. Пусто = автоматический выбор через `urltest`.

#### Применение изменений

После правки YAML:

```bash
sudo systemctl restart enter-box
sudo journalctl -u enter-box -f
```

Конфиг читается только при старте. Если хотите применять подписки без рестарта — это уже на будущее (поле `auto_update` задел).

---

## Часть 4. Проверка, что всё работает

### 4.1. Сервис запущен

```bash
systemctl is-active enter-box
# active

sudo journalctl -u enter-box -n 30 --no-pager
```

В логе должно быть:

```
INFO endpoints loaded count=7
INFO awg interface up iface=awg-node3 tag=awg-node3     ← если есть vpn://
INFO sing-box started
INFO dhcp server started iface=eth0                     ← если lan.dhcp.enabled=true
INFO enter-box ready version=...
```

Если что-то из этого отсутствует — смотрите `Part 6. Troubleshooting`.

### 4.2. Активный узел выбран

Clash API публикуется на `127.0.0.1:9090`:

```bash
curl -s http://127.0.0.1:9090/proxies/proxy-auto | jq '{now, all: (.all|length), type}'
```

Ожидаемо:

```json
{
  "now": "provider/node3",
  "all": 7,
  "type": "URLTest"
}
```

`now` — тег текущего активного узла. Если `null` — ни один узел не прошёл urltest (см. Troubleshooting).

Список всех узлов со статусами:

```bash
curl -s http://127.0.0.1:9090/proxies | jq -r '
  .proxies
  | to_entries[]
  | select(.value.type=="VLESS" or .value.type=="VMess" or .value.type=="Direct")
  | "\(.key)\t\(.value.alive // "?")\t\(.value.history[-1].delay // "-")ms"
'
```

Пример вывода:

```
node1            true    42ms
node2            true    87ms
awg-node3        true    120ms
```

`alive: false` — узел не отвечает (проверьте доступность сервера, ключи, UUID).

### 4.3. Внешний IP — через VPN

На Orange Pi:

```bash
# запрос пойдёт через tun0 (auto_route)
curl -s ifconfig.me
# ожидаемо — IP вашего VPN-сервера, а не мобильного оператора
```

RU-домен — должен идти `direct`:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://gosuslugi.ru
# 200 или 301 — сайт работает
```

Проверка, что пакет реально ушёл через правильную цепочку:

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[]
  | {host: .metadata.host, chain: .chains}
' | head -20
```

В `chain` для RU-сайтов должно быть `["direct"]`, для остальных `["proxy", "proxy-auto", "nodeN"]`.

### 4.4. Клиенты в LAN

Подключите ноут/телефон в коммутатор. Устройство должно получить адрес из DHCP-диапазона.

На клиенте:

```bash
# Linux/macOS
ip route | grep default
# default via 192.168.88.1 dev eth0

ping -c 3 192.168.88.1
# 3 packets transmitted, 3 received

curl -s ifconfig.me
# IP VPN

curl -s https://vk.com -o /dev/null -w '%{http_code}\n'
# 200 — direct работает
```

### 4.5. Проверка автопереключения

Можно руками «сломать» узел и убедиться, что `urltest` уводит трафик:

```bash
# прибить активный узел, выбрать другой
curl -X PUT http://127.0.0.1:9090/proxies/proxy \
     -d '{"name":"node2"}'

curl -s http://127.0.0.1:9090/proxies/proxy | jq .now
# "node2"

# вернуть auto
curl -X PUT http://127.0.0.1:9090/proxies/proxy \
     -d '{"name":"proxy-auto"}'
```

Форсировать urltest прямо сейчас:

```bash
curl -s 'http://127.0.0.1:9090/proxies/proxy-auto/delay?timeout=5000&url=http://cp.cloudflare.com/generate_204'
```

---

## Часть 5. Управление сервисом

```bash
# статус
sudo systemctl status enter-box

# логи в реальном времени
sudo journalctl -u enter-box -f

# логи за последний час
sudo journalctl -u enter-box --since "1 hour ago"

# перезапуск после правки конфига
sudo systemctl restart enter-box

# остановка (снимает nftables-таблицу, тушит AWG)
sudo systemctl stop enter-box

# временно отключить автозапуск
sudo systemctl disable enter-box
```

Проверить, что при остановке всё чистится:

```bash
sudo systemctl stop enter-box
sudo nft list tables | grep enter-box         # пусто
ip link show | grep awg-                  # пусто
```

При старте enter-box сам:
1. Включает `ip_forward` и настраивает sysctl.
2. Вешает `lan.address` на `lan.interface`.
3. Поднимает `wan.name` и `lan.interface`.
4. Накатывает nftables (NAT LAN → WAN и LAN → tun0).
5. Создаёт AWG-туннели (`awg-<tag>`) для `vpn://`.
6. Запускает sing-box с tun-inbound, urltest и selector.
7. Стартует DHCP, если `lan.dhcp.enabled`.

`iptables` не трогается вообще — используется только `nftables`.

---

## Часть 6. Troubleshooting

### 6.1. Сервис не стартует

```bash
sudo journalctl -u enter-box -n 50 --no-pager
```

Частые ошибки и что делать:

| Ошибка в логе | Причина | Решение |
|---|---|---|
| `config load: yaml parse: ...` | Опечатка в YAML | `sudo enter-box run -c /etc/enter-box/enter-box.yaml -dump` — покажет место |
| `config load: wan.name is required` | Пустой `wan.name` | Впишите реальное имя интерфейса |
| `netcfg apply: sysctl ...` | Нет прав | Запустили без sudo? |
| `netcfg apply: nft apply: ...` | Ошибка в nftables | `sudo nft list ruleset` — посмотрите остатки старой таблицы |
| `netcfg apply: set LAN addr: ...` | Интерфейс занят другим менеджером | Отключите NetworkManager/systemd-networkd |
| `box.New: ...` | Ошибка в конфиге sing-box | `enter-box run -c /path -dump` покажет JSON |
| `awg up node3: ...` | Проблема с AWG | Смотрите `.AWG` параметры в исходном `vpn://` |
| `dhcp init: invalid dhcp range` | Диапазон DHCP вне подсети | Проверьте `lan.address` и `lan.dhcp.range_*` |

### 6.2. Клиенты не получают интернет

```bash
# 1. IP forwarding включён?
cat /proc/sys/net/ipv4/ip_forward
# 1

# 2. nftables-таблица на месте?
sudo nft list table inet enter-box

# 3. Маршрут до tun0 есть?
ip route show table all | grep tun0

# 4. Пакеты идут?
sudo tcpdump -ni eth0 -c 10
sudo tcpdump -ni tun0 -c 10
```

Если `tcpdump` на eth0 видит пакеты, а на tun0 — нет, значит sing-box не принимает их. Смотрите `route.rules` в `-dump`.

### 6.3. RU-сайты идут через VPN

```bash
curl -s http://127.0.0.1:9090/connections | jq '
  .connections[]
  | select(.metadata.host | contains("vk") or contains("gosuslugi"))
  | {host: .metadata.host, chain: .chains, rule: .rule}
'
```

Если в `chain` есть `proxy`, а не `direct` — правило не сработало. Причины:
- `use_ru_direct: false` в конфиге;
- `geosite-ru.srs` не загрузился (проверьте логи sing-box, раздел `rule-set`);
- домен попадает под `force_proxy` (проверьте секцию).

### 6.4. Узлы не проходят urltest

```bash
curl -s http://127.0.0.1:9090/proxies | jq '
  .proxies
  | to_entries[]
  | select(.value.alive == false)
  | .key
'
```

Пустой вывод — все узлы живые.

Если узлы мертвы:
- проверьте доступность серверов с Orange Pi напрямую: `ping vps.example.com`;
- для VLESS — верны ли `UUID`, `pbk`, `sid`;
- для VMess — корректна ли base64-строка (можно декодировать вручную через `echo '...' | base64 -d`);
- для AWG — смотрите `sudo journalctl -u enter-box | grep awg`.

### 6.5. AmneziaWG-интерфейс не поднимается

```bash
ip link show | grep awg-
# если пусто — интерфейс не создан

sudo journalctl -u enter-box | grep -i awg
```

Частые ошибки:

| Ошибка | Решение |
|---|---|
| `create tun awg-node3: operation not permitted` | Нужен root (`sudo`) |
| `ipc set: ...` | Проверьте параметры AWG (private_key, peer_public, endpoint) |
| `device up: ...` | Несовместимость версий `amneziawg-go` с параметрами обфускации |

Проверить параметры AWG в исходной ссылке `vpn://` можно так:

```bash
echo 'eyJo...' | base64 -d | jq '.containers[].awg.last_config'
# вернёт текстовый INI-конфиг
```

### 6.6. Всё сломалось, хочу с нуля

```bash
sudo systemctl stop enter-box
sudo rm -f /etc/enter-box/enter-box.yaml
sudo nft delete table inet enter-box 2>/dev/null

sudo enter-box setup
```

---

## Часть 7. Обновление

### 7.1. Обновить бинарь

На дев-машине:

```bash
cd ~/Desktop/enter-box-arm
git pull
go mod tidy
make build-arm64
scp dist/enter-box-arm64 pi@192.168.1.XXX:/tmp/enter-box
```

На Orange Pi:

```bash
sudo systemctl stop enter-box
sudo install -m0755 /tmp/enter-box /usr/local/bin/enter-box
sudo systemctl start enter-box
```

### 7.2. Обновить геозоны без пересборки

sing-box сам раз в 24 часа обновляет `.srs` (в `rulesets.go` указан `update_interval: "24h"`). Принудительно:

```bash
sudo systemctl restart enter-box
```

Он при старте перекачает актуальные наборы.

---

## Часть 8. Что делать дальше

Полезные команды в повседневном использовании:

```bash
# активный узел
curl -s http://127.0.0.1:9090/proxies/proxy | jq .now

# сводка по узлам с задержками
curl -s http://127.0.0.1:9090/proxies | jq -r '
  .proxies
  | to_entries[]
  | select(.value.type=="VLESS" or .value.Type=="VMess")
  | "\(.key)\t\(.value.alive)\t\(.value.history[-1].delay // "-")ms"
'

# активные соединения
curl -s http://127.0.0.1:9090/connections | jq '.connections | length'

# переключить узел вручную
curl -X PUT http://127.0.0.1:9090/proxies/proxy -d '{"name":"node2"}'

# вернуть автовыбор
curl -X PUT http://127.0.0.1:9090/proxies/proxy -d '{"name":"proxy-auto"}'
```