#!/usr/bin/env python3
"""Alert the operator when an ocrypt scanner stops being ready or falls behind.

Reads only each scanner's /readyz and /metrics. It never touches the database,
providers, cursors or containers. Run once per systemd timer tick, or loop
with --interval. Secrets (bot token, webhook URL) are read from files and are
never written to logs, state or status. Each notification channel keeps its
own outbox, so one failing channel neither silences nor spams the other.
"""
import argparse
import json
import math
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse

LAG_LINE = re.compile(r"^merchant_scanner_head_lag_blocks(?:\{[^}]*\})?\s+(\S+)(?:\s+\d+)?\s*$", re.MULTILINE)
UNCREDITED_LINE = re.compile(r"^merchant_scanner_unattributed_open(?:\{[^}]*\})?\s+(\S+)(?:\s+\d+)?\s*$", re.MULTILINE)
NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
TELEGRAM_TOKEN = re.compile(r"^\d+:[A-Za-z0-9_-]+$")
CHAT_ID = re.compile(r"^(-?\d+|@[A-Za-z0-9_]{5,})$")
TIMEOUT = 5
DEADLINE = 10
OUTBOX_LIMIT = 50


def _integer(raw, key, default, minimum):
    value = raw.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise ValueError("%s must be an integer >= %d" % (key, minimum))
    return value


def read_secret(path):
    with open(path, encoding="utf-8") as handle:
        return handle.read().strip()


def check_secret_files(cfg):
    """Validate secret files without ever echoing their contents."""
    if cfg["telegram"]:
        try:
            token = read_secret(cfg["telegram"]["bot_token_file"])
        except OSError as error:
            raise ValueError("telegram bot token file is unreadable (%s)" % type(error).__name__) from None
        if not TELEGRAM_TOKEN.match(token):
            raise ValueError("telegram bot token file is malformed")
    if cfg["webhook_url_file"]:
        try:
            url = read_secret(cfg["webhook_url_file"])
        except OSError as error:
            raise ValueError("webhook URL file is unreadable (%s)" % type(error).__name__) from None
        parsed = urlparse(url)
        if any(c.isspace() for c in url) or parsed.scheme != "https" or not parsed.hostname:
            raise ValueError("webhook URL file must hold one https URL")


def load_config_dict(raw, check_secrets=False):
    if not isinstance(raw, dict):
        raise ValueError("configuration must be a JSON object")
    targets = raw.get("targets")
    if not isinstance(targets, list) or not targets:
        raise ValueError("at least one target is required")
    names = set()
    for target in targets:
        if not isinstance(target, dict):
            raise ValueError("each target must be an object")
        name, url = target.get("name", ""), target.get("url", "")
        if not isinstance(name, str) or not NAME.match(name) or name in names:
            raise ValueError("target names must be unique lowercase slugs")
        parsed = urlparse(url) if isinstance(url, str) else None
        try:
            parsed and parsed.port
        except ValueError:
            parsed = None
        if not parsed or parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.path not in ("", "/") or parsed.query or parsed.fragment or parsed.username:
            raise ValueError("target %s needs an http(s) origin such as http://ocrypt-scanner-bsc:9091" % name)
        lag = target.get("max_lag_blocks")
        if lag is not None and (isinstance(lag, bool) or not isinstance(lag, int) or lag < 0):
            raise ValueError("target %s max_lag_blocks must be a non-negative integer" % name)
        names.add(name)
    telegram = raw.get("telegram")
    if telegram is not None:
        if not isinstance(telegram, dict) or not telegram.get("bot_token_file") or not CHAT_ID.match(str(telegram.get("chat_id", "")).strip()):
            raise ValueError("telegram needs bot_token_file and a numeric chat_id or @channel")
    if telegram is None and not raw.get("webhook_url_file"):
        raise ValueError("configure telegram and/or webhook_url_file")
    cfg = {
        "targets": [{"name": t["name"], "url": t["url"].rstrip("/"), "max_lag_blocks": t.get("max_lag_blocks")} for t in targets],
        "alert_after_seconds": _integer(raw, "alert_after_seconds", 300, 0),
        "repeat_after_seconds": _integer(raw, "repeat_after_seconds", 3600, 60),
        "state_file": raw.get("state_file", "/var/lib/ocrypt-scanner-watchdog/state.json"),
        "status_file": raw.get("status_file", "/var/lib/ocrypt-scanner-watchdog/status.json"),
        "telegram": telegram,
        "webhook_url_file": raw.get("webhook_url_file"),
    }
    if check_secrets:
        check_secret_files(cfg)
    return cfg


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, "redirect refused", headers, fp)


_OPENER = urllib.request.build_opener(_NoRedirect)


def _read_bounded(response, limit):
    """Read at most limit bytes, giving up after DEADLINE seconds in total."""
    started, chunks, size = time.monotonic(), [], 0
    while size < limit:
        if time.monotonic() - started > DEADLINE:
            raise TimeoutError("response too slow")
        chunk = response.read(min(65536, limit - size))
        if not chunk:
            break
        chunks.append(chunk)
        size += len(chunk)
    return b"".join(chunks).decode("utf-8", "replace")


def http_fetch(url, timeout):
    request = urllib.request.Request(url, headers={"User-Agent": "ocrypt-scanner-watchdog"})
    try:
        with _OPENER.open(request, timeout=timeout) as response:
            return response.status, _read_bounded(response, 1 << 20)
    except urllib.error.HTTPError as error:
        return error.code, ""


def http_post(url, payload, timeout):
    body = json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json", "User-Agent": "ocrypt-scanner-watchdog"})
    with _OPENER.open(request, timeout=timeout) as response:
        if not 200 <= response.status < 300:
            raise urllib.error.HTTPError(url, response.status, "unexpected status", response.headers, None)
        return response.status


def parse_lag(metrics):
    return _parse_gauge(LAG_LINE, metrics)


def parse_uncredited(metrics):
    return _parse_gauge(UNCREDITED_LINE, metrics)


def _parse_gauge(line, metrics):
    match = line.search(metrics)
    if not match:
        return None
    try:
        value = float(match.group(1))
    except ValueError:
        return None
    if not math.isfinite(value) or value < 0:
        return None
    return int(value)


def probe(target, fetch):
    base = target["url"]
    try:
        status, _ = fetch(base + "/readyz", TIMEOUT)
    except Exception:  # noqa: BLE001 - any transport failure means unreachable
        return {"ready": False, "lag": None, "problem": "unreachable"}
    lag = uncredited = None
    try:
        metrics_status, metrics = fetch(base + "/metrics", TIMEOUT)
        if metrics_status == 200:
            lag = parse_lag(metrics)
            uncredited = parse_uncredited(metrics)
    except Exception:  # noqa: BLE001 - lag is optional evidence
        pass
    ready = status == 200
    limit = target.get("max_lag_blocks")
    problem = None
    if not ready:
        problem = "not ready"
    elif limit is not None and lag is None:
        problem = "lag unknown"
    elif limit is not None and lag > limit:
        problem = "%d blocks behind (limit %d)" % (lag, limit)
    elif uncredited:
        problem = "%d internal transfer records wait for an operator" % uncredited
    return {"ready": ready, "lag": lag, "problem": problem}


def _clean_target_state(value):
    if not isinstance(value, dict):
        return {"bad_since": None, "alerted_at": None}
    cleaned = {}
    for key in ("bad_since", "alerted_at"):
        item = value.get(key)
        cleaned[key] = item if isinstance(item, (int, float)) and not isinstance(item, bool) else None
    return cleaned


def evaluate(previous, name, observation, now, cfg):
    state = _clean_target_state(previous)
    # A clock that stepped backwards must not stretch or invert durations.
    for key in ("bad_since", "alerted_at"):
        if state[key] is not None and state[key] > now:
            state[key] = now
    event = {"target": name, "problem": observation["problem"], "lag": observation["lag"]}
    if observation["problem"] is None:
        events = []
        if state["alerted_at"] is not None:
            events.append(dict(event, kind="recovered", bad_for=max(0, now - (state["bad_since"] or now))))
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
    minutes = int(max(0, event.get("bad_for", 0)) // 60)
    lag = "" if event.get("lag") is None else ", last lag %d blocks" % event["lag"]
    if event["kind"] == "recovered":
        return "ocrypt: scanner %s recovered after %d min%s" % (event["target"], minutes, lag)
    if event["kind"] == "removed":
        return "ocrypt: scanner %s no longer monitored while alerting; check it by hand" % event["target"]
    prefix = "still down" if event["kind"] == "repeat" else "ALERT"
    return "ocrypt %s: scanner %s %s for %d min%s" % (prefix, event["target"], event["problem"], minutes, lag)


def channels(cfg):
    names = []
    if cfg["telegram"]:
        names.append("telegram")
    if cfg["webhook_url_file"]:
        names.append("webhook")
    return names


def deliver(cfg, channel, text, post):
    if channel == "telegram":
        token = read_secret(cfg["telegram"]["bot_token_file"])
        post("https://api.telegram.org/bot" + token + "/sendMessage", {"chat_id": str(cfg["telegram"]["chat_id"]).strip(), "text": text}, TIMEOUT)
    else:
        post(read_secret(cfg["webhook_url_file"]), {"text": text}, TIMEOUT)


def describe_failure(error):
    # Never include the exception message: it may contain the secret URL.
    code = getattr(error, "code", None)
    return type(error).__name__ + (" HTTP %d" % code if isinstance(code, int) else "")


def write_json(path, value, mode):
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    handle, temporary = tempfile.mkstemp(dir=directory, prefix=".watchdog-")
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as output:
            json.dump(value, output, sort_keys=True)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    except BaseException:
        if os.path.exists(temporary):
            os.unlink(temporary)
        raise


def read_state(path):
    try:
        with open(path, encoding="utf-8") as handle:
            value = json.load(handle)
    except (OSError, ValueError):
        return {"targets": {}, "outbox": {}, "last_run": None}
    if not isinstance(value, dict):
        return {"targets": {}, "outbox": {}, "last_run": None}
    targets = value.get("targets") if isinstance(value.get("targets"), dict) else {}
    outbox = value.get("outbox") if isinstance(value.get("outbox"), dict) else {}
    outbox = {k: [m for m in v if isinstance(m, str)] for k, v in outbox.items() if isinstance(v, list)}
    last_run = value.get("last_run") if isinstance(value.get("last_run"), (int, float)) and not isinstance(value.get("last_run"), bool) else None
    return {"targets": {k: _clean_target_state(v) for k, v in targets.items()}, "outbox": outbox, "last_run": last_run}


def run_once(cfg, now, fetch=http_fetch, post=http_post):
    previous = read_state(cfg["state_file"])
    gap = max(2 * cfg["alert_after_seconds"], 600)
    unobserved = previous["last_run"] is not None and (now - previous["last_run"] > gap or now < previous["last_run"])
    targets, status, messages = {}, [], []
    configured = {target["name"] for target in cfg["targets"]}
    for target in cfg["targets"]:
        name = target["name"]
        before = previous["targets"].get(name, {"bad_since": None, "alerted_at": None})
        if unobserved and before["bad_since"] is not None and before["alerted_at"] is None:
            # The watchdog itself was not running: do not alert on time it never saw.
            before = {"bad_since": now, "alerted_at": None}
        try:
            observation = probe(target, fetch)
        except Exception:  # noqa: BLE001 - one bad target must not stop the others
            observation = {"ready": False, "lag": None, "problem": "unreachable"}
        targets[name], events = evaluate(before, name, observation, now, cfg)
        messages.extend(format_message(event) for event in events)
        down = observation["problem"] is not None
        status.append({
            "name": name,
            "state": "down" if down and not observation["ready"] else ("degraded" if down else "ok"),
            "ready": observation["ready"],
            "lag_blocks": observation["lag"],
            "problem": observation["problem"],
            "bad_since": targets[name]["bad_since"],
        })
    for name, before in previous["targets"].items():
        if name not in configured and before.get("alerted_at") is not None:
            messages.append(format_message({"kind": "removed", "target": name}))
    outbox = {}
    for channel in channels(cfg):
        queue = (previous["outbox"].get(channel, []) + messages)[-OUTBOX_LIMIT:]
        while queue:
            try:
                deliver(cfg, channel, queue[0], post)
            except Exception as error:  # noqa: BLE001 - keep the message for the next run
                print("watchdog: %s delivery failed (%s); %d message(s) pending" % (channel, describe_failure(error), len(queue)), file=sys.stderr)
                break
            queue.pop(0)
        outbox[channel] = queue
    write_json(cfg["state_file"], {"targets": targets, "outbox": outbox, "last_run": now}, 0o600)
    write_json(cfg["status_file"], {"generated_at": int(now), "targets": status}, 0o644)
    bad = sum(1 for item in status if item["state"] != "ok")
    print("watchdog: %d target(s), %d bad, %d message(s) pending" % (len(status), bad, sum(len(q) for q in outbox.values())))
    return status


def safe_tick(cfg, now, fetch=http_fetch, post=http_post):
    try:
        run_once(cfg, now, fetch, post)
        return True
    except Exception as error:  # noqa: BLE001 - the loop must survive any single tick
        print("watchdog: tick failed (%s)" % type(error).__name__, file=sys.stderr)
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--config", required=True, help="JSON configuration file")
    parser.add_argument("--interval", type=int, default=0, help="loop every N seconds instead of running once")
    args = parser.parse_args()
    try:
        with open(args.config, encoding="utf-8") as handle:
            cfg = load_config_dict(json.load(handle), check_secrets=True)
        state_dir = os.path.dirname(cfg["state_file"]) or "."
        os.makedirs(state_dir, exist_ok=True)
        if not os.access(state_dir, os.W_OK):
            raise ValueError("state directory %s is not writable by UID %d" % (state_dir, os.getuid()))
    except (OSError, ValueError) as error:
        print("watchdog: invalid configuration: %s" % error, file=sys.stderr)
        return 2
    while True:
        ok = safe_tick(cfg, time.time())
        if args.interval <= 0:
            return 0 if ok else 1
        time.sleep(args.interval)


if __name__ == "__main__":
    sys.exit(main())
