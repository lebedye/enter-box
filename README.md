```
.
├── go.mod
├── go.sum
├── cmd/enter-box/main.go
├── internal/
│   ├── config/config.go
│   ├── parser/
│   │   ├── types.go
│   │   ├── vless.go
│   │   ├── vmess.go
│   │   ├── amnezia.go      // vpn://
│   │   ├── subscription.go
│   │   └── pool.go
│   ├── singbox/
│   │   ├── generate.go
│   │   └── runner.go
│   ├── net/
│   │   ├── setup_linux.go
│   │   └── dhcp.go
│   └── routing/ru.go
├── enter-box.example.yaml
└── Makefile
```


# enter-box

Гибридный VPN-шлюз для Orange Pi: VLESS + VMess + AmneziaWG (vpn://),
RU-сервисы напрямую, всё остальное — через прокси.

## Схема

```
USB0 (WAN) ─┐
            ├─→ [enter-box] ─→ ETH0 ─→ switch ─→ 3–5 клиентов
TUN0        ┘
```

## Сборка

```bash
make build                # arm64 (по умолчанию)
make build ARCH=amd64     # amd64
make dump                 # посмотреть сгенерированный конфиг sing-box
```

## Установка

```bash
sudo make install
sudo systemctl enable --now enter-box
```

## Конфигурация

См. `enter-box.example.yaml`. Основные секции:

- `wan.name` — интерфейс с интернетом.
- `lan.interface` — интерфейс в коммутатор.
- `endpoints` / `subscriptions` — источники узлов.
- `routing.use_ru_direct` — включить geosite-ru/geoip-ru → direct.

## AmneziaWG

sing-box не поддерживает AWG-параметры обфускации (Jc, Jmin, S1, S2, H1–H4,
I1–I5). Поэтому enter-box:

1. Парсит `vpn://…` → извлекает INI-конфиг AWG.
2. Поднимает userspace-интерфейс `awg-<tag>` через `amneziawg-go`.
3. В sing-box добавляет `outbound type=direct bind_interface=awg-<tag>`.

Трафик, попавший в этот outbound, идёт через обфусцированный туннель.

## Проверка

```bash
# 1. Конфиг распарсился и сгенерился:
enter-box -c /etc/enter-box/enter-box.yaml -dump | head -50

# 2. Запуск в дебаге:
enter-box -c /etc/enter-box/enter-box.yaml -v

# 3. Проверка с клиента:
curl -s ifconfig.me                     # должно быть IP прокси
curl -s https://vk.com                  # должно работать напрямую
```