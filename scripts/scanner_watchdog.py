#!/usr/bin/env python3
"""Alert the operator when an ocrypt scanner stops being ready or falls behind.

Reads only each scanner's /readyz and /metrics. It never touches the database,
providers, cursors or containers. Run once per systemd timer tick, or loop
with --interval. Secrets (bot token, webhook URL) are read from files and are
never written to logs, state or status.
"""
import argparse
import json
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse

LAG_LINE = re.compile(r"^merchant_scanner_head_lag_blocks(?:\{[^}]*\})?\s+(\S+)\s*$", re.MULTILINE)
NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
TIMEOUT = 5


def load_config_dict(raw):
    targets = raw.get("targets") or []
    if not targets:
        raise ValueError("at least one target is required")
    names = set()
    for target in targets:
        name, url = target.get("name", ""), target.get("url", "")
        parsed = urlparse(url)
        if not NAME.match(name) or name in names:
            raise ValueError("target names must be unique lowercase slugs")
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.query or parsed.fragment:
            raise ValueError("target %s needs an http(s) origin" % name)
        lag = target.get("max_lag_blocks")
        if lag is not None and (not isinstance(lag, int) or lag < 0):
            raise ValueError("target %s max_lag_blocks must be a non-negative integer" % name)
        names.add(name)
    telegram = raw.get("telegram")
    if telegram is not None and (not telegram.get("bot_token_file") or not str(telegram.get("chat_id", "")).strip()):
        raise ValueError("telegram needs bot_token_file and chat_id")
    if telegram is None and not raw.get("webhook_url_file"):
        raise ValueError("configure telegram and/or webhook_url_file")
    cfg = {
        "targets": [{"name": t["name"], "url": t["url"].rstrip("/"), "max_lag_blocks": t.get("max_lag_blocks")} for t in targets],
        "alert_after_seconds": int(raw.get("alert_after_seconds", 300)),
        "repeat_after_seconds": int(raw.get("repeat_after_seconds", 3600)),
        "state_file": raw.get("state_file", "/var/lib/ocrypt-scanner-watchdog/state.json"),
        "status_file": raw.get("status_file", "/var/lib/ocrypt-scanner-watchdog/status.json"),
        "telegram": telegram,
        "webhook_url_file": raw.get("webhook_url_file"),
    }
    if cfg["alert_after_seconds"] < 0 or cfg["repeat_after_seconds"] <= 0:
        raise ValueError("alert/repeat intervals must be positive")
    return cfg


def http_fetch(url, timeout):
    request = urllib.request.Request(url, headers={"User-Agent": "ocrypt-scanner-watchdog"})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, response.read(1 << 20).decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        return error.code, error.read(1 << 16).decode("utf-8", "replace")


def http_post(url, payload, timeout):
    body = json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json", "User-Agent": "ocrypt-scanner-watchdog"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.status


def parse_lag(metrics):
    match = LAG_LINE.search(metrics)
    if not match:
        return None
    try:
        return int(float(match.group(1)))
    except ValueError:
        return None


def probe(target, fetch):
    base = target["url"]
    try:
        status, _ = fetch(base + "/readyz", TIMEOUT)
    except (OSError, ValueError):
        return {"ready": False, "lag": None, "problem": "unreachable"}
    lag = None
    try:
        metrics_status, metrics = fetch(base + "/metrics", TIMEOUT)
        if metrics_status == 200:
            lag = parse_lag(metrics)
    except (OSError, ValueError):
        pass
    ready = status == 200
    problem = None
    if not ready:
        problem = "not ready"
    elif target.get("max_lag_blocks") is not None and lag is not None and lag > target["max_lag_blocks"]:
        problem = "%d blocks behind (limit %d)" % (lag, target["max_lag_blocks"])
    return {"ready": ready, "lag": lag, "problem": problem}


def evaluate(previous, name, observation, now, cfg):
    state = {"bad_since": previous.get("bad_since"), "alerted_at": previous.get("alerted_at")}
    event = {"target": name, "problem": observation["problem"], "lag": observation["lag"]}
    if observation["problem"] is None:
        events = []
        if state["alerted_at"] is not None:
            events.append(dict(event, kind="recovered", bad_for=now - state["bad_since"]))
        return {"bad_since": None, "alerted_at": None}, events
    if state["bad_since"] is None:
        state["bad_since"] = now
    bad_for = now - state["bad_since"]
    if state["alerted_at"] is None and bad_for >= cfg["alert_after_seconds"]:
        state["alerted_at"] = now
        return state, [dict(event, kind="alert", bad_for=bad_for)]
    if state["alerted_at"] is not None and now - state["alerted_at"] >= cfg["repeat_after_seconds"]:
        state["alerted_at"] = now
        return state, [dict(event, kind="repeat", bad_for=bad_for)]
    return state, []


def format_message(event):
    minutes = int(event["bad_for"] // 60)
    lag = "" if event["lag"] is None else ", lag %d blocks" % event["lag"]
    if event["kind"] == "recovered":
        return "ocrypt: scanner %s recovered after %d min%s" % (event["target"], minutes, lag)
    prefix = "still down" if event["kind"] == "repeat" else "ALERT"
    return "ocrypt %s: scanner %s %s for %d min%s" % (prefix, event["target"], event["problem"], minutes, lag)


def read_secret(path):
    with open(path, encoding="utf-8") as handle:
        return handle.read().strip()


def notify(cfg, text, post):
    sent = False
    if cfg["telegram"]:
        token = read_secret(cfg["telegram"]["bot_token_file"])
        post("https://api.telegram.org/bot" + token + "/sendMessage", {"chat_id": str(cfg["telegram"]["chat_id"]), "text": text}, TIMEOUT)
        sent = True
    if cfg["webhook_url_file"]:
        post(read_secret(cfg["webhook_url_file"]), {"text": text}, TIMEOUT)
        sent = True
    return sent


def write_json(path, value):
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    handle, temporary = tempfile.mkstemp(dir=directory, prefix=".watchdog-")
    with os.fdopen(handle, "w", encoding="utf-8") as output:
        json.dump(value, output, sort_keys=True)
    os.replace(temporary, path)


def read_state(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, ValueError):
        return {}


def run_once(cfg, now, fetch=http_fetch, post=http_post):
    previous = read_state(cfg["state_file"])
    state, status = {}, []
    for target in cfg["targets"]:
        name = target["name"]
        observation = probe(target, fetch)
        target_state, events = evaluate(previous.get(name, {}), name, observation, now, cfg)
        for event in events:
            try:
                notify(cfg, format_message(event), post)
            except (OSError, ValueError) as error:
                # Keep the alert pending so the next run retries it. Only the
                # exception type is logged: messages may contain the secret URL.
                print("watchdog: %s notification for %s failed (%s)" % (event["kind"], name, type(error).__name__), file=sys.stderr)
                if event["kind"] == "recovered":
                    target_state = dict(previous.get(name, {}))
                else:
                    target_state["alerted_at"] = previous.get(name, {}).get("alerted_at")
        state[name] = target_state
        down = observation["problem"] is not None
        status.append({
            "name": name,
            "state": "down" if down and not observation["ready"] else ("degraded" if down else "ok"),
            "ready": observation["ready"],
            "lag_blocks": observation["lag"],
            "problem": observation["problem"] if down else None,
            "bad_since": target_state["bad_since"],
        })
    write_json(cfg["state_file"], state)
    write_json(cfg["status_file"], {"generated_at": int(now), "targets": status})
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--config", required=True, help="JSON configuration file")
    parser.add_argument("--interval", type=int, default=0, help="loop every N seconds instead of running once")
    args = parser.parse_args()
    try:
        with open(args.config, encoding="utf-8") as handle:
            cfg = load_config_dict(json.load(handle))
    except (OSError, ValueError) as error:
        print("watchdog: invalid configuration: %s" % error, file=sys.stderr)
        return 2
    while True:
        run_once(cfg, time.time())
        if args.interval <= 0:
            return 0
        time.sleep(args.interval)


if __name__ == "__main__":
    sys.exit(main())
