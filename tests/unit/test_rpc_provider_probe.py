import http.server
import importlib.util
import json
import threading
import time
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
    raw = {"chains": [dict({"name": "bsc", "chain_id": 56, "filter": {"address": USDT, "topics": [TRANSFER]}, "recent_offset": 200,
                            "archive_age_blocks": 400000, "span": 10, "min_agreeing": 2, "endpoints": endpoints}, **overrides)]}
    return module.load_config_dict(raw)


def fake_call(behaviour):
    """behaviour: endpoint url -> {"head": int, "recent": logs|error, "archive": logs|error}."""
    calls = []

    def call(url, payload, timeout):
        assert timeout <= 20
        calls.append((url, payload["method"]))
        spec = behaviour[url]
        if payload["method"] == "eth_chainId":
            return {"jsonrpc": "2.0", "id": payload["id"], "result": hex(spec.get("chain", 56))}
        if payload["method"] == "eth_blockNumber":
            if isinstance(spec["head"], Exception):
                raise spec["head"]
            head = spec["head"] if isinstance(spec["head"], str) else hex(spec["head"])
            return {"jsonrpc": "2.0", "id": payload["id"], "result": head}
        params = payload["params"][0]
        head = spec["head"] if isinstance(spec["head"], int) else 100000000
        key = "archive" if int(params["fromBlock"], 16) < head - 1000 else "recent"
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


def base_chain(**overrides):
    chain = {"name": "bsc", "chain_id": 56, "filter": {"address": USDT, "topics": [TRANSFER]}, "endpoints": [{"id": "a", "url": "https://a.invalid"}, {"id": "b", "url": "https://b.invalid"}]}
    chain.update(overrides)
    return {"chains": [chain]}


@pytest.mark.parametrize("raw", [
    base_chain(recent_offset=5, span=10),
    base_chain(recent_offset=200, span=10, archive_age_blocks=100),
    base_chain(min_agreeing=1),
    base_chain(endpoints=5),
    base_chain(endpoints=[{"id": 1, "url": "https://a.invalid"}, {"id": "b", "url": "https://b.invalid"}]),
    base_chain(endpoints=[{"id": "a", "url": "https://user:key@a.invalid"}, {"id": "b", "url": "https://b.invalid"}]),
    base_chain(chain_id=None),
    base_chain(name=7),
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


def three(tmp_path):
    return chain_config(tmp_path, endpoints(tmp_path))["chains"][0]


def test_agreement_on_empty_answers_is_not_evidence(tmp_path):
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": []},
        "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": []},
        SECRET: {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
    })
    result = module.probe_chain(three(tmp_path), call)
    assert result["verdict"] == "insufficient" and result["majority"]["archive"] is None
    assert result["endpoints"]["publicnode"]["archive"]["status"] == "empty"


def test_endpoint_on_another_chain_is_excluded(tmp_path):
    call = fake_call({
        "https://a.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE, "chain": 97},
        "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
    })
    result = module.probe_chain(three(tmp_path), call)
    assert result["agreeing"] == ["club48", "paid"] and result["endpoints"]["publicnode"]["chain"] == "wrong chain"


def test_even_number_of_heads_uses_the_lower_median(tmp_path):
    call = fake_call({
        "https://a.invalid": {"head": 65000000, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 75000000, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": OSError("down")},
    })
    assert module.probe_chain(three(tmp_path), call)["head"] == 65000000


@pytest.mark.parametrize("head", ["-0x5", "10", "0x_1_0", "0x" + "f" * 40, None])
def test_malformed_heads_are_ignored_and_not_admissible(tmp_path, head):
    call = fake_call({
        "https://a.invalid": {"head": head, "recent": RECENT, "archive": ARCHIVE},
        "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
        SECRET: {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
    })
    result = module.probe_chain(three(tmp_path), call)
    assert result["head"] == 100000000 and "publicnode" not in result["agreeing"]


def test_plurality_is_not_majority(tmp_path):
    ids = ["a", "b", "c", "d", "e"]
    cfg = chain_config(tmp_path, [{"id": i, "url": "https://%s.invalid" % i} for i in ids])
    variants = {"a": RECENT, "b": RECENT, "c": [log(5, 5, 0)], "d": [log(6, 6, 0)], "e": [log(7, 7, 0)]}
    call = fake_call({"https://%s.invalid" % i: {"head": 100000000, "recent": variants[i], "archive": ARCHIVE} for i in ids})
    result = module.probe_chain(cfg["chains"][0], call)
    assert result["majority"]["recent"] is None and result["verdict"] == "insufficient"


def test_log_index_formats_are_normalized():
    a = [{"blockHash": "0xAB", "transactionHash": "0xCD", "logIndex": 3}]
    b = [{"blockHash": "0xab", "transactionHash": "0xcd", "logIndex": "0x03"}]
    assert module.log_digest(a) == module.log_digest(b)


def test_null_fields_are_invalid_responses(tmp_path):
    bad = [{"blockHash": None, "transactionHash": "0x1", "logIndex": "0x0"}]
    call = fake_call({"https://a.invalid": {"head": 100000000, "recent": bad, "archive": ARCHIVE},
                      "https://b.invalid": {"head": 100000000, "recent": RECENT, "archive": ARCHIVE},
                      SECRET: {"head": 100000000, "recent": RECENT, "archive": ARCHIVE}})
    assert module.probe_chain(three(tmp_path), call)["endpoints"]["publicnode"]["recent"]["error"] == "invalid response"


def test_unusual_url_errors_never_echo_the_secret(tmp_path):
    secret = "https://apikey:SUPERSECRET@bsc.example\uff20node.invalid"
    (tmp_path / "odd").write_text(secret)
    raw = base_chain(endpoints=[{"id": "a", "url_file": str(tmp_path / "odd")}, {"id": "b", "url": "https://b.invalid"}])
    with pytest.raises(ValueError) as error:
        module.load_config_dict(raw)
    assert "SUPERSECRET" not in str(error.value)


def serve(handler_cls):
    server = http.server.HTTPServer(("127.0.0.1", 0), handler_cls)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def test_slow_drip_hits_the_total_deadline(monkeypatch):
    class Drip(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            self.rfile.read(int(self.headers["Content-Length"]))
            self.send_response(200)
            self.send_header("Content-Length", "40")
            self.end_headers()
            for _ in range(40):
                self.wfile.write(b" ")
                self.wfile.flush()
                time.sleep(0.2)

        def log_message(self, *args):
            pass

    server = serve(Drip)
    monkeypatch.setattr(module, "DEADLINE", 1)
    started = time.monotonic()
    try:
        with pytest.raises(Exception) as error:
            module.http_call("http://127.0.0.1:%d" % server.server_port, {"jsonrpc": "2.0", "id": 1, "method": "eth_chainId", "params": []}, 5)
        assert module.categorize(error.value) == "timeout" and time.monotonic() - started < 4
    finally:
        server.shutdown()


def test_end_to_end_over_http_with_a_403_provider(tmp_path, capsys):
    class Node(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if self.path == "/blocked":
                self.send_response(403)
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            if request["method"] == "eth_chainId":
                result = "0x38"
            elif request["method"] == "eth_blockNumber":
                result = hex(100000000)
            else:
                result = RECENT if int(request["params"][0]["fromBlock"], 16) > 99900000 else ARCHIVE
            body = json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = serve(Node)
    base = "http://127.0.0.1:%d" % server.server_port
    config = base_chain(endpoints=[{"id": "a", "url": base + "/a"}, {"id": "b", "url": base + "/b"}, {"id": "blocked", "url": base + "/blocked"}])
    (tmp_path / "cfg.json").write_text(json.dumps(config))
    try:
        import sys
        argv = sys.argv
        sys.argv = ["rpc_provider_probe.py", "--config", str(tmp_path / "cfg.json"), "--json", str(tmp_path / "report.json")]
        try:
            code = module.main()
        finally:
            sys.argv = argv
    finally:
        server.shutdown()
    output = capsys.readouterr().out
    report = json.loads((tmp_path / "report.json").read_text())
    assert code == 0 and "bsc: OK" in output and "http 403" in output
    assert report["chains"][0]["endpoints"]["blocked"]["chain"] == "http 403"
