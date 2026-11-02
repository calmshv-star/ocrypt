import http.server
import importlib.util
import json
import threading
from pathlib import Path

import pytest

SPEC = importlib.util.spec_from_file_location("rpc_provider_probe", Path(__file__).resolve().parents[2] / "scripts/rpc_provider_probe.py")
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)

USDT = "0x55d398326f99059fF775485246999027B3197955"
TRANSFER = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
SECRET = "https://bsc.example-node.invalid/v1/super-secret-api-key"


def log(block, tx, index):
    return {"blockHash": "0x%064x" % block, "transactionHash": "0x%064x" % tx, "logIndex": hex(index), "data": "0x"}


RECENT = [log(1, 1, 0), log(1, 2, 3)]
ARCHIVE = [log(9, 7, 1)]


def chain_config(tmp_path, endpoints, **overrides):
    (tmp_path / "key").write_text(SECRET + "\n")
    raw = {"chains": [dict({"name": "bsc", "filter": {"address": USDT, "topics": [TRANSFER]}, "recent_offset": 200,
                            "archive_age_blocks": 400000, "span": 10, "min_agreeing": 2, "endpoints": endpoints}, **overrides)]}
    return module.load_config_dict(raw)


def fake_call(behaviour):
    """behaviour: endpoint url -> {"head": int, "recent": logs|error, "archive": logs|error}."""
    calls = []

    def call(url, payload, timeout):
        assert timeout <= 20
        calls.append((url, payload["method"]))
        spec = behaviour[url]
        if payload["method"] == "eth_blockNumber":
            if isinstance(spec["head"], Exception):
                raise spec["head"]
            return {"jsonrpc": "2.0", "id": payload["id"], "result": hex(spec["head"])}
        params = payload["params"][0]
        key = "archive" if int(params["fromBlock"], 16) < spec["head"] - 1000 else "recent"
        value = spec[key]
        if isinstance(value, Exception):
            raise value
        if isinstance(value, dict):
            return {"jsonrpc": "2.0", "id": payload["id"], "error": value}
        return {"jsonrpc": "2.0", "id": payload["id"], "result": value}
    call.calls = calls
    return call


def endpoints(tmp_path):
    return [{"id": "publicnode", "url": "https://a.invalid"}, {"id": "club48", "url": "https://b.invalid"}, {"id": "paid", "url_file": str(tmp_path / "key")}]


def test_log_digest_is_order_independent_and_content_sensitive():
    assert module.log_digest(RECENT) == module.log_digest(list(reversed(RECENT)))
    assert module.log_digest(RECENT) != module.log_digest(RECENT[:1])
    assert module.log_digest([]) == module.log_digest([])


def test_providers_that_agree_on_both_ranges_are_admitted(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path))
    call = fake_call({
        "https://a.invalid": {"head": 129105453, "recent": RECENT, "archive": {"code": -32602, "message": "Archive requests require a personal token"}},
        "https://b.invalid": {"head": 129105453, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": 129105452, "recent": RECENT, "archive": ARCHIVE},
    })
    result = module.probe_chain(cfg["chains"][0], call)
    assert result["agreeing"] == ["club48", "paid"] and result["verdict"] == "ok"
    assert result["endpoints"]["publicnode"]["archive"]["status"] == "error"
    assert "-32602" in result["endpoints"]["publicnode"]["archive"]["error"]
    assert result["ranges"]["recent"] == {"from": 129105453 - 200, "to": 129105453 - 200 + 9}
    assert result["ranges"]["archive"]["from"] == 129105453 - 400000


def test_a_disagreeing_provider_is_not_counted(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path))
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 100000000, "recent": RECENT[:1], "archive": ARCHIVE},
        SECRET: {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
    })
    result = module.probe_chain(cfg["chains"][0], call)
    assert result["agreeing"] == ["paid", "publicnode"] and result["endpoints"]["club48"]["recent"]["agrees"] is False


def test_a_tie_has_no_majority_and_fails(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path)[:2])
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 100000000, "recent": RECENT[:1], "archive": ARCHIVE},
    })
    result = module.probe_chain(cfg["chains"][0], call)
    assert result["majority"]["recent"] is None and result["agreeing"] == [] and result["verdict"] == "insufficient"


def test_reference_head_is_the_median_so_one_liar_cannot_move_the_ranges(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path))
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 100000002, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": 999999999, "recent": RECENT, "archive": ARCHIVE},
    })
    assert module.probe_chain(cfg["chains"][0], call)["head"] == 100000002


def test_no_head_at_all_is_insufficient_without_crashing(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path)[:2])
    call = fake_call({"https://a.invalid": {"head": OSError("down")}, "https://b.invalid": {"head": TimeoutError("slow")}})
    result = module.probe_chain(cfg["chains"][0], call)
    assert result["verdict"] == "insufficient" and result["head"] is None


def test_secret_endpoint_urls_never_reach_the_report(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path))
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": 100000000, "recent": ValueError("bad response from " + SECRET), "archive": OSError(SECRET + " refused")},
    })
    report = module.run(cfg, call)
    text = module.format_report(report) + json.dumps(report)
    assert SECRET not in text and "super-secret-api-key" not in text
    assert report["chains"][0]["endpoints"]["paid"]["recent"]["error"] == "invalid response"


def test_exit_code_reflects_every_chain(tmp_path):
    cfg = chain_config(tmp_path, endpoints(tmp_path)[:2])
    good = fake_call({"https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE}, "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE}})
    bad = fake_call({"https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE}, "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": {"code": 429, "message": "rate limited"}}})
    assert module.exit_code(module.run(cfg, good)) == 0
    assert module.exit_code(module.run(cfg, bad)) == 1


@pytest.mark.parametrize("raw", [
    {"chains": []},
    {"chains": [{"name": "bsc", "filter": {"address": "0x123", "topics": []}, "endpoints": [{"id": "a", "url": "https://a.invalid"}]}]},
    {"chains": [{"name": "bsc", "filter": {"address": USDT, "topics": ["0xnope"]}, "endpoints": [{"id": "a", "url": "https://a.invalid"}]}]},
    {"chains": [{"name": "bsc", "filter": {"address": USDT, "topics": [TRANSFER]}, "endpoints": [{"id": "a", "url": "https://a.invalid"}, {"id": "a", "url": "https://b.invalid"}]}]},
    {"chains": [{"name": "bsc", "filter": {"address": USDT, "topics": [TRANSFER]}, "endpoints": [{"id": "a", "url": "file:///etc/passwd"}]}]},
    {"chains": [{"name": "bsc", "filter": {"address": USDT, "topics": [TRANSFER]}, "span": 5000, "endpoints": [{"id": "a", "url": "https://a.invalid"}]}]},
    {"chains": [{"name": "bsc", "filter": {"address": USDT, "topics": [TRANSFER]}, "endpoints": [{"id": "a"}]}]},
    [],
])
def test_invalid_configuration_is_rejected(raw):
    with pytest.raises(ValueError):
        module.load_config_dict(raw)


def test_real_json_rpc_round_trip():
    class Node(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            result = hex(100000000) if request["method"] == "eth_blockNumber" else RECENT
            body = json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = http.server.HTTPServer(("127.0.0.1", 0), Node)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        url = "http://127.0.0.1:%d" % server.server_port
        assert module.rpc(module.http_call, url, "eth_blockNumber", []) == hex(100000000)
        assert module.rpc(module.http_call, url, "eth_getLogs", [{}]) == RECENT
    finally:
        server.shutdown()
