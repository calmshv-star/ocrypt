import http.server
import importlib.util
import json
import threading
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
    {"targets": []},
    {"targets": [{"name": "bsc", "url": "file:///etc/passwd"}]},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}], "telegram": None},
    {"targets": [{"name": "bsc", "url": "http://scanner-bsc:9091"}, {"name": "bsc", "url": "http://other:9091"}], "webhook_url_file": "/x"},
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
