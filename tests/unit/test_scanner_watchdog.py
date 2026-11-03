import http.client
import http.server
import importlib.util
import json
import threading
import urllib.error
from pathlib import Path

import pytest

SPEC = importlib.util.spec_from_file_location("scanner_watchdog", Path(__file__).resolve().parents[2] / "scripts/scanner_watchdog.py")
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)

METRICS = '# HELP merchant_scanner_head_lag_blocks x\nmerchant_scanner_head_lag_blocks{service="scanner"} 373925\nmerchant_provider_circuits_open{service="scanner"} 0\n'
TOKEN = "123456:never-print-this-telegram-token"


def fake_fetch(responses):
    def fetch(url, timeout):
        assert timeout <= 10
        value = responses[url]
        if isinstance(value, Exception):
            raise value
        return value
    return fetch


def config(tmp_path, **overrides):
    (tmp_path / "token").write_text(TOKEN + "\n")
    cfg = {
        "targets": [{"name": "bsc", "url": "http://scanner-bsc:9091", "max_lag_blocks": 1000}],
        "alert_after_seconds": 300,
        "repeat_after_seconds": 3600,
        "state_file": str(tmp_path / "state.json"),
        "status_file": str(tmp_path / "status.json"),
        "telegram": {"bot_token_file": str(tmp_path / "token"), "chat_id": "-100200300"},
    }
    cfg.update(overrides)
    return module.load_config_dict(cfg)


def test_parse_lag_reads_only_the_scanner_lag_gauge():
    assert module.parse_lag(METRICS) == 373925
    assert module.parse_lag("merchant_provider_circuits_open 0\n") is None
    assert module.parse_lag("merchant_scanner_head_lag_blocks{service=\"scanner\"} not-a-number\n") is None


def test_probe_classifies_ready_not_ready_and_unreachable():
    target = {"name": "bsc", "url": "http://scanner-bsc:9091", "max_lag_blocks": 1000}
    ready = module.probe(target, fake_fetch({"http://scanner-bsc:9091/readyz": (200, "ok"), "http://scanner-bsc:9091/metrics": (200, METRICS.replace("373925", "12"))}))
    assert ready == {"ready": True, "lag": 12, "problem": None}
    down = module.probe(target, fake_fetch({"http://scanner-bsc:9091/readyz": (503, "not ready"), "http://scanner-bsc:9091/metrics": (200, METRICS)}))
    assert down["ready"] is False and down["lag"] == 373925 and "not ready" in down["problem"]
    lagging = module.probe(target, fake_fetch({"http://scanner-bsc:9091/readyz": (200, "ok"), "http://scanner-bsc:9091/metrics": (200, METRICS)}))
    assert lagging["ready"] is True and "373925 blocks behind" in lagging["problem"]
    gone = module.probe(target, fake_fetch({"http://scanner-bsc:9091/readyz": OSError("refused"), "http://scanner-bsc:9091/metrics": OSError("refused")}))
    assert gone == {"ready": False, "lag": None, "problem": "unreachable"}


def bad():
    return {"ready": False, "lag": 373925, "problem": "not ready"}


def good():
    return {"ready": True, "lag": 3, "problem": None}


def test_alert_after_threshold_once_then_repeat_then_recover():
    cfg = {"alert_after_seconds": 300, "repeat_after_seconds": 3600}
    state, events = module.evaluate({}, "bsc", bad(), 1000, cfg)
    assert events == [] and state["bad_since"] == 1000
    state, events = module.evaluate(state, "bsc", bad(), 1299, cfg)
    assert events == []
    state, events = module.evaluate(state, "bsc", bad(), 1300, cfg)
    assert [e["kind"] for e in events] == ["alert"] and state["alerted_at"] == 1300
    state, events = module.evaluate(state, "bsc", bad(), 1360, cfg)
    assert events == []
    state, events = module.evaluate(state, "bsc", bad(), 1300 + 3600, cfg)
    assert [e["kind"] for e in events] == ["repeat"]
    state, events = module.evaluate(state, "bsc", good(), 9000, cfg)
    assert [e["kind"] for e in events] == ["recovered"] and state == {"bad_since": None, "alerted_at": None}


def test_short_blip_never_alerts_or_recovers():
    cfg = {"alert_after_seconds": 300, "repeat_after_seconds": 3600}
    state, events = module.evaluate({}, "bsc", bad(), 1000, cfg)
    state, events = module.evaluate(state, "bsc", good(), 1100, cfg)
    assert events == [] and state == {"bad_since": None, "alerted_at": None}


def test_messages_name_target_reason_and_duration():
    text = module.format_message({"kind": "alert", "target": "bsc", "problem": "not ready", "lag": 373925, "bad_for": 720})
    assert "bsc" in text and "not ready" in text and "12 min" in text and "373925" in text
    assert "recovered" in module.format_message({"kind": "recovered", "target": "ton", "problem": None, "lag": 2, "bad_for": 4000})


def test_run_once_alerts_through_telegram_and_writes_status(tmp_path, capsys):
    cfg = config(tmp_path)
    fetch = fake_fetch({"http://scanner-bsc:9091/readyz": (503, "not ready"), "http://scanner-bsc:9091/metrics": (200, METRICS)})
    sent = []

    def post(url, payload, timeout):
        sent.append((url, payload))
        return 200

    module.run_once(cfg, now=1000, fetch=fetch, post=post)
    module.run_once(cfg, now=1300, fetch=fetch, post=post)
    assert len(sent) == 1
    url, payload = sent[0]
    assert url == "https://api.telegram.org/bot" + TOKEN + "/sendMessage"
    assert payload["chat_id"] == "-100200300" and "bsc" in payload["text"]
    status = json.loads((tmp_path / "status.json").read_text())
    assert status["targets"] == [{"name": "bsc", "state": "down", "ready": False, "lag_blocks": 373925, "problem": "not ready", "bad_since": 1000}]
    output = capsys.readouterr()
    for blob in (output.out, output.err, (tmp_path / "status.json").read_text(), (tmp_path / "state.json").read_text()):
        assert TOKEN not in blob


def test_failed_notification_is_retried_on_the_next_run(tmp_path):
    cfg = config(tmp_path)
    fetch = fake_fetch({"http://scanner-bsc:9091/readyz": (503, "x"), "http://scanner-bsc:9091/metrics": (200, METRICS)})
    attempts = []

    def failing(url, payload, timeout):
        attempts.append(payload)
        raise OSError("telegram unreachable")

    module.run_once(cfg, now=1000, fetch=fetch, post=failing)
    module.run_once(cfg, now=1300, fetch=fetch, post=failing)
    module.run_once(cfg, now=1360, fetch=fetch, post=failing)
    assert len(attempts) == 2


def test_webhook_receives_plain_text(tmp_path):
    (tmp_path / "hook").write_text("https://hooks.example.invalid/ocrypt\n")
    cfg = config(tmp_path, telegram=None, webhook_url_file=str(tmp_path / "hook"))
    sent = []
    module.run_once(cfg, now=0, fetch=fake_fetch({"http://scanner-bsc:9091/readyz": OSError("x"), "http://scanner-bsc:9091/metrics": OSError("x")}), post=lambda u, p, t: sent.append((u, p)) or 200)
    module.run_once(cfg, now=301, fetch=fake_fetch({"http://scanner-bsc:9091/readyz": OSError("x"), "http://scanner-bsc:9091/metrics": OSError("x")}), post=lambda u, p, t: sent.append((u, p)) or 200)
    assert sent == [("https://hooks.example.invalid/ocrypt", {"text": sent[0][1]["text"]})] and "unreachable" in sent[0][1]["text"]


@pytest.mark.parametrize("cfg", [
    {"targets": [], "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "file:///etc/passwd"}], "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}], "telegram": None},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}, {"name": "bsc", "url": "http://other:9091"}], "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9O91"}], "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091/readyz"}], "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}], "telegram": {"bot_token_file": "/x", "chat_id": "REPLACE_WITH_CHAT_ID"}},
    {"targets": "bsc", "webhook_url_file": "/x"},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}], "webhook_url_file": "/x", "alert_after_seconds": None},
    [],
])
def test_invalid_configuration_is_rejected(cfg):
    with pytest.raises(ValueError):
        module.load_config_dict(cfg)


def test_real_http_probe_against_a_stalled_scanner():
    class Stalled(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = b"not ready" if self.path == "/readyz" else METRICS.encode()
            self.send_response(503 if self.path == "/readyz" else 200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = http.server.HTTPServer(("127.0.0.1", 0), Stalled)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        target = {"name": "bsc", "url": "http://127.0.0.1:%d" % server.server_port, "max_lag_blocks": 1000}
        result = module.probe(target, module.http_fetch)
        assert result["ready"] is False and result["lag"] == 373925
    finally:
        server.shutdown()


def responses(ready, metrics=(200, METRICS)):
    return fake_fetch({"http://scanner-bsc:9091/readyz": ready, "http://scanner-bsc:9091/metrics": metrics})


def both_channels(tmp_path):
    (tmp_path / "hook").write_text("https://hooks.example.invalid/ocrypt\n")
    return config(tmp_path, webhook_url_file=str(tmp_path / "hook"))


@pytest.mark.parametrize("error", [http.client.BadStatusLine("garbage"), http.client.IncompleteRead(b"x"), http.client.InvalidURL("bad"), RuntimeError("unexpected")])
def test_probe_treats_any_transport_error_as_unreachable(error):
    target = {"name": "bsc", "url": "http://scanner-bsc:9091", "max_lag_blocks": None}
    assert module.probe(target, responses(error, error))["problem"] == "unreachable"


def test_parse_lag_rejects_non_finite_values():
    assert module.parse_lag('merchant_scanner_head_lag_blocks{service="scanner"} +Inf\n') is None
    assert module.parse_lag('merchant_scanner_head_lag_blocks{service="scanner"} 1e400\n') is None


def test_one_broken_target_does_not_stop_the_others(tmp_path):
    cfg = config(tmp_path, targets=[{"name": "aaa", "url": "http://junk:9091"}, {"name": "bsc", "url": "http://scanner-bsc:9091"}], alert_after_seconds=0)
    fetch = fake_fetch({"http://junk:9091/readyz": http.client.BadStatusLine("x"), "http://junk:9091/metrics": http.client.BadStatusLine("x"),
                        "http://scanner-bsc:9091/readyz": (503, "x"), "http://scanner-bsc:9091/metrics": (200, METRICS)})
    sent = []
    module.run_once(cfg, now=0, fetch=fetch, post=lambda u, p, t: sent.append(p["text"]) or 200)
    assert len(sent) == 2 and any("aaa" in text for text in sent) and any("bsc" in text for text in sent)


def test_tick_never_raises(tmp_path):
    (tmp_path / "file").write_text("not a directory")
    cfg = config(tmp_path, status_file=str(tmp_path / "file" / "status.json"))
    assert module.safe_tick(cfg, 0, responses((200, "ok")), lambda u, p, t: 200) is False
    assert module.safe_tick(config(tmp_path), 60, responses((200, "ok")), lambda u, p, t: 200) is True


def test_failing_webhook_neither_spams_nor_silences_telegram(tmp_path):
    cfg = both_channels(tmp_path)
    telegram, webhook_attempts, webhook_ok = [], [], [False]

    def post(url, payload, timeout):
        if "api.telegram.org" in url:
            telegram.append(payload["text"])
            return 200
        webhook_attempts.append(payload["text"])
        if not webhook_ok[0]:
            raise OSError("webhook down")
        return 200

    for now in range(0, 900, 60):
        module.run_once(cfg, now=now, fetch=responses((503, "x")), post=post)
    assert len(telegram) == 1 and "ALERT" in telegram[0]
    webhook_ok[0] = True
    module.run_once(cfg, now=900, fetch=responses((200, "ok"), (200, METRICS.replace("373925", "1"))), post=post)
    assert len(telegram) == 2 and "recovered" in telegram[1]
    delivered = webhook_attempts[-2:]
    assert "ALERT" in delivered[0] and "recovered" in delivered[1]


def test_failing_telegram_does_not_block_the_webhook(tmp_path):
    cfg = both_channels(tmp_path)
    webhook = []

    def post(url, payload, timeout):
        if "api.telegram.org" in url:
            raise urllib.error.HTTPError(url, 401, "Unauthorized", {}, None)
        webhook.append(payload)
        return 200

    module.run_once(cfg, now=0, fetch=responses((503, "x")), post=post)
    module.run_once(cfg, now=300, fetch=responses((503, "x")), post=post)
    assert len(webhook) == 1 and webhook[0] == {"text": webhook[0]["text"]}


def test_secrets_never_leak_on_failure_paths(tmp_path, capsys):
    cfg = both_channels(tmp_path)

    def post(url, payload, timeout):
        raise http.client.InvalidURL("URL can't contain control characters. %r" % url)

    module.run_once(cfg, now=0, fetch=responses((503, "x")), post=post)
    module.run_once(cfg, now=300, fetch=responses((503, "x")), post=post)
    output = capsys.readouterr()
    for blob in (output.out, output.err, (tmp_path / "state.json").read_text(), (tmp_path / "status.json").read_text()):
        assert TOKEN not in blob and "hooks.example.invalid" not in blob


@pytest.mark.parametrize("token,hook", [("123456:has space", "https://hooks.example.invalid/x"), ("123456:abc\nsecond", "https://hooks.example.invalid/x"),
                                        (TOKEN, "http://hooks.example.invalid/plain"), (TOKEN, "https://hooks.example.invalid/x\nsecond")])
def test_malformed_secret_files_are_rejected_without_echoing_them(tmp_path, token, hook):
    (tmp_path / "token").write_text(token)
    (tmp_path / "hook").write_text(hook)
    raw = {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}], "telegram": {"bot_token_file": str(tmp_path / "token"), "chat_id": "-100"},
           "webhook_url_file": str(tmp_path / "hook"), "state_file": str(tmp_path / "s"), "status_file": str(tmp_path / "t")}
    with pytest.raises(ValueError) as error:
        module.load_config_dict(raw, check_secrets=True)
    assert token not in str(error.value) and hook not in str(error.value)


def test_webhook_redirect_is_not_counted_as_delivery():
    class Redirect(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            self.send_response(302)
            self.send_header("Location", "/moved")
            self.send_header("Content-Length", "0")
            self.end_headers()

        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, *args):
            pass

    server = http.server.HTTPServer(("127.0.0.1", 0), Redirect)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with pytest.raises(Exception):
            module.http_post("http://127.0.0.1:%d/hook" % server.server_port, {"text": "x"}, 5)
    finally:
        server.shutdown()


def test_status_file_is_readable_by_a_status_page(tmp_path):
    module.run_once(config(tmp_path), now=0, fetch=responses((200, "ok")), post=lambda u, p, t: 200)
    assert (tmp_path / "status.json").stat().st_mode & 0o044 == 0o044


def test_clock_going_backwards_or_unobserved_gaps_do_not_fake_durations(tmp_path):
    cfg = {"alert_after_seconds": 300, "repeat_after_seconds": 3600}
    state, events = module.evaluate({"bad_since": 90000, "alerted_at": 90000}, "bsc", bad(), 1000, cfg)
    assert state["bad_since"] <= 1000 and state["alerted_at"] <= 1000
    cfg = config(tmp_path)
    sent = []
    module.run_once(cfg, now=0, fetch=responses((503, "x")), post=lambda u, p, t: sent.append(p["text"]) or 200)
    module.run_once(cfg, now=86400, fetch=responses((503, "x")), post=lambda u, p, t: sent.append(p["text"]) or 200)
    assert sent == []
    _, events = module.evaluate({"bad_since": 1000, "alerted_at": 1000}, "bsc", good(), 500, cfg)
    assert "-" not in module.format_message(events[0])


def test_removed_target_that_was_alerting_is_reported(tmp_path):
    cfg = config(tmp_path, alert_after_seconds=0)
    sent = []
    module.run_once(cfg, now=0, fetch=responses((503, "x")), post=lambda u, p, t: sent.append(p["text"]) or 200)
    cfg = config(tmp_path, alert_after_seconds=0, targets=[{"name": "ton", "url": "http://scanner-ton:9091"}])
    module.run_once(cfg, now=60, fetch=fake_fetch({"http://scanner-ton:9091/readyz": (200, "ok"), "http://scanner-ton:9091/metrics": (200, METRICS.replace("373925", "1"))}),
                    post=lambda u, p, t: sent.append(p["text"]) or 200)
    assert len(sent) == 2 and "bsc" in sent[1] and "no longer monitored" in sent[1]


def test_ready_scanner_with_unreadable_lag_is_a_problem_when_a_limit_is_set():
    target = {"name": "bsc", "url": "http://scanner-bsc:9091", "max_lag_blocks": 1000}
    result = module.probe(target, responses((200, "ok"), (404, "missing")))
    assert result["ready"] is True and result["problem"] == "lag unknown"
    target["max_lag_blocks"] = None
    assert module.probe(target, responses((200, "ok"), (404, "missing")))["problem"] is None


@pytest.mark.parametrize("content", ["[]", '{"targets": {"bsc": null}}', '{"targets": {"bsc": {"bad_since": "yesterday"}}}', "not json"])
def test_corrupt_state_files_are_ignored(tmp_path, content):
    cfg = config(tmp_path)
    (tmp_path / "state.json").write_text(content)
    module.run_once(cfg, now=0, fetch=responses((200, "ok"), (200, METRICS.replace("373925", "1"))), post=lambda u, p, t: 200)
    assert json.loads((tmp_path / "status.json").read_text())["targets"][0]["state"] == "ok"


def test_telegram_payload_is_exact(tmp_path):
    cfg = config(tmp_path, alert_after_seconds=0)
    sent = []
    module.run_once(cfg, now=0, fetch=responses((503, "x")), post=lambda u, p, t: sent.append(p) or 200)
    assert sent == [{"chat_id": "-100200300", "text": "ocrypt ALERT: scanner bsc not ready for 0 min, last lag 373925 blocks"}]
