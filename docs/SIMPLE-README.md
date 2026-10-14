# enter-box

VPN-шлюз для Orange Pi: VLESS + VMess + AmneziaWG (vpn://).
RU-сервисы идут напрямую через WAN, всё остальное — через VPN.

## Быстрый старт

### Сборка (на дев-машине, Linux/macOS)

```bash
git clone <repo> enter-box && cd enter-box
go mod tidy
make build-arm64
# бинарь появится в dist/enter-box-arm64
```

### Установка (на Orange Pi)

```bash
scp dist/enter-box-arm64 pi@<ipi>:/tmp/enter-box
ssh pi@<ipi>

sudo apt install -y nftables iproute2
sudo install -Dm0755 /tmp/enter-box /usr/local/bin/enter-box
sudo enter-box check     # проверка окружения
sudo enter-box setup     # интерактивная установка
```

Готово. Сервис включён и работает. Логи:

```bash
sudo journalctl -u enter-box -f
```

## Требования

- Linux (ARM64 для Orange Pi, amd64 для дев-машины)
- root для запуска демона (создаёт TUN-интерфейсы и правила nftables)
- `/dev/net/tun` — модуль ядра (`sudo modprobe tun`)
- `nftables`, `iproute2` — устанавливаются apt

## Управление

```bash
sudo systemctl status enter-box
sudo systemctl restart enter-box
sudo systemctl stop enter-box
```

Активный узел (Clash API на 127.0.0.1:9090):

```bash
curl -s http://127.0.0.1:9090/proxies/proxy | jq .now
```

Полный гайд — `docs/USERDOC.md`.