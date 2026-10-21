#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Реальный тест автоматического failover на плате (root), без выполнения на хосте.

Что делает:
  1. Читает preflight JSON (путь через env PREFLIGHT или по умолчанию;
     creds наружу не печатаются).
  2. Получает список прокси через Clash API http://127.0.0.1:9090/proxies.
  3. Из preflight outbounds выбирает первый VLESS-сервер с IPv4-адресом.
  4. Проверяет его через /proxies/{tag}/delay.
  5. Если жив — переключает proxy-auto -> tag, proxy -> proxy-auto (явный авторежим).
  6. Короткий тест: nft drop на 3 сек, ждём 12 сек, проверяем что inner node НЕ сменился.
  7. Длинный тест: nft drop, ждём смены узла до 60 сек, проверяем elapsed >= 10 сек
     и что новый узел != direct.
  8. Финальный Probe нового узла, печатаем FINAL_VPN_OK tag.

Ограничения:
  - Никогда не меняем IP, SSH, DNS, DHCP, WAN, сервисы компьютера.
  - nft-таблица создаётся ТОЛЬКО своя (inet eb_verify_<PID>) и удаляется в finally.
  - subprocess вызывается списком аргументов, без shell, с check.
  - При ошибке — код возврата 1 и тип ошибки, без тела API и без preflight.
  - При слабом интернете тест может завершиться неудачей; это не скрывается.
"""

import ipaddress
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PREFLIGHT_PATH = os.environ.get(
    "PREFLIGHT",
    "/var/lib/enter-box/subscriptions/preflight.json",
)
CLASH_API = "http://127.0.0.1:9090"
PROBE_URL = "https://1.1.1.1/cdn-cgi/trace"
PROBE_URL_ENC = urllib.parse.quote(PROBE_URL, safe="")

SHORT_DROP_SEC = 3
SHORT_WAIT_SEC = 12
LONG_MAX_WAIT_SEC = 60
LONG_POLL_SEC = 2
LONG_MIN_SWITCH_SEC = 10

NFT_TABLE = "eb_verify_%d" % os.getpid()


class VerifyError(Exception):
    """Ошибка теста с коротким типом (без тела API и без preflight)."""

    def __init__(self, kind, detail=""):
        super().__init__(kind)
        self.kind = kind
        self.detail = detail


#  HTTP к Clash API 

def _api(method, path, timeout=3.0):
    url = CLASH_API + path
    req = urllib.request.Request(url, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
    except urllib.error.HTTPError as e:
        # Тело ответа не печатаем и не сохраняем.
        raise VerifyError("api_http_%d" % e.code)
    except urllib.error.URLError:
        raise VerifyError("api_unreachable")
    except TimeoutError:
        raise VerifyError("api_timeout")
    if not body:
        return {}
    try:
        return json.loads(body.decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        raise VerifyError("api_bad_json")


def api_proxies():
    return _api("GET", "/proxies")


def api_delay(tag, timeout_ms):
    """Возвращает delay в мс или None, если узел не ответил."""
    path = "/proxies/%s/delay?url=%s&timeout=%d" % (
        urllib.parse.quote(tag, safe=""),
        PROBE_URL_ENC,
        timeout_ms,
    )
    try:
        data = _api("GET", path, timeout=3.0)
    except VerifyError as e:
        if e.kind in ("api_http_408", "api_http_500", "api_http_503", "api_http_504", "api_timeout"):
            return None
        raise
    delay = data.get("delay")
    if isinstance(delay, int) and delay > 0:
        return delay
    return None


def api_put_proxy(group, tag):
    body = json.dumps({"name": tag}).encode("utf-8")
    url = CLASH_API + "/proxies/" + urllib.parse.quote(group, safe="")
    req = urllib.request.Request(url, data=body, method="PUT",
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=3.0) as resp:
            resp.read()
    except urllib.error.HTTPError as e:
        raise VerifyError("api_put_http_%d" % e.code)
    except urllib.error.URLError:
        raise VerifyError("api_put_unreachable")
    except TimeoutError:
        raise VerifyError("api_put_timeout")


def inner_node(proxies):
    """Текущий узел внутри proxy-auto (или сам proxy-auto, если не найден)."""
    auto = proxies.get("proxies", {}).get("proxy-auto", {})
    return auto.get("now") or ""


#  preflight 

def load_preflight():
    try:
        with open(PREFLIGHT_PATH, "r", encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        raise VerifyError("preflight_missing")
    except PermissionError:
        raise VerifyError("preflight_permission")
    except (ValueError, UnicodeDecodeError):
        raise VerifyError("preflight_bad_json")


def pick_vless_ipv4(preflight):
    """Первый VLESS outbound с IPv4-адресом сервера. Возвращает (tag, ip, port)."""
    outbounds = preflight.get("outbounds") or []
    for ob in outbounds:
        if not isinstance(ob, dict):
            continue
        if ob.get("type") != "vless":
            continue
        tag = ob.get("tag")
        server = ob.get("server")
        port = ob.get("server_port")
        if not tag or not server or not isinstance(port, int):
            continue
        try:
            ip = ipaddress.IPv4Address(server)
        except (ipaddress.AddressValueError, ValueError):
            continue
        # Заглушка «автовыбор» 0.0.0.0 — не сервер.
        if ip.is_unspecified or ip.is_loopback:
            continue
        if api_delay(tag, 2000) is None:
            continue
        return tag, str(ip), port
    raise VerifyError("no_vless_ipv4")


# nft 

def nft_run(args):
    """Вызов nft списком аргументов, без shell, с проверкой кода возврата."""
    subprocess.run(["nft"] + args, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def nft_create_drop(ip, port):
    # Своя таблица, свой chain, единственное правило drop на конкретный сервер.
    nft_run(["add", "table", "inet", NFT_TABLE])
    nft_run(["add", "chain", "inet", NFT_TABLE, "output",
             "{", "type", "filter", "hook", "output",
             "priority", "-100", ";", "policy", "accept", ";", "}"])
    nft_run(["add", "rule", "inet", NFT_TABLE, "output",
             "ip", "daddr", ip, "tcp", "dport", str(port), "drop"])


def nft_delete_table():
    # Удаляем ТОЛЬКО свою таблицу; ошибку игнорируем.
    try:
        subprocess.run(["nft", "delete", "table", "inet", NFT_TABLE],
                       check=False,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass


# тесты 

def short_test(selected):
    """Короткий drop: за 12 сек узел НЕ должен смениться."""
    nft_create_drop(selected["ip"], selected["port"])
    try:
        started = time.monotonic()
        api_delay(selected["tag"], 1000)
        time.sleep(max(0, SHORT_DROP_SEC - (time.monotonic() - started)))
    finally:
        nft_delete_table()
    time.sleep(SHORT_WAIT_SEC)

    proxies = api_proxies()
    now = inner_node(proxies)
    if now != selected["tag"]:
        raise VerifyError("short_outage_switched", "inner=%s" % now)
    print("SHORT_OUTAGE_NO_SWITCH")


def long_test(selected):
    """Длинный drop: ждём смены узла до 60 сек, проверяем elapsed >= 10 сек."""
    nft_create_drop(selected["ip"], selected["port"])
    start = time.monotonic()
    new_tag = None
    try:
        while True:
            elapsed = time.monotonic() - start
            if elapsed > LONG_MAX_WAIT_SEC:
                break
            api_delay(selected["tag"], 1000)
            proxies = api_proxies()
            now = inner_node(proxies)
            if now and now != selected["tag"]:
                new_tag = now
                break
            time.sleep(LONG_POLL_SEC)
    finally:
        nft_delete_table()

    if new_tag is None:
        raise VerifyError("long_outage_no_switch")

    elapsed = time.monotonic() - start
    if elapsed < LONG_MIN_SWITCH_SEC:
        raise VerifyError("long_outage_switched_too_fast",
                          "elapsed=%.1f" % elapsed)
    if new_tag == "direct":
        raise VerifyError("long_outage_switched_to_direct")

    print("LONG_OUTAGE_SWITCH %.1f" % elapsed)
    return new_tag


def final_probe(tag):
    delay = api_delay(tag, 2000)
    if delay is None or delay <= 0:
        raise VerifyError("final_probe_failed", "tag=%s" % tag)
    print("FINAL_VPN_OK %s" % tag)


# main 

def _sigterm_handler(signum, frame):
    # Превращаем SIGTERM в исключение, чтобы сработал finally и удалил таблицу.
    raise SystemExit(128 + signum)


def main():
    signal.signal(signal.SIGTERM, _sigterm_handler)

    preflight = load_preflight()
    tag, ip, port = pick_vless_ipv4(preflight)
    selected = {"tag": tag, "ip": ip, "port": port}

    if api_delay(tag, 2000) is None:
        raise VerifyError("selected_node_dead", "tag=%s" % tag)

    # Явный авторежим: proxy-auto -> tag, proxy -> proxy-auto.
    api_put_proxy("proxy-auto", tag)
    api_put_proxy("proxy", "proxy-auto")

    proxies = api_proxies()
    if inner_node(proxies) != tag:
        raise VerifyError("auto_mode_not_applied")

    short_test(selected)
    new_tag = long_test(selected)
    final_probe(new_tag)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except VerifyError as e:
        # Никаких тел API и никаких данных preflight — только тип ошибки.
        msg = "ERROR %s" % e.kind
        if e.detail:
            msg += " (%s)" % e.detail
        print(msg, file=sys.stderr)
        sys.exit(1)
    except subprocess.CalledProcessError as e:
        print("ERROR nft_failed (rc=%d)" % e.returncode, file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("ERROR interrupted", file=sys.stderr)
        sys.exit(1)
    finally:
        # Уборка обязательна даже при частичном создании таблицы или SIGTERM.
        nft_delete_table()