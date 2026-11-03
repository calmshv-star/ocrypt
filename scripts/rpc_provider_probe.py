#!/usr/bin/env python3
"""Prove which EVM RPC providers agree on recent and archival eth_getLogs ranges.

For every configured chain the probe checks each endpoint's eth_chainId, takes
the lower median of the valid heads as the reference, runs one address/topic
filter on a recent range (below the finality depth) and on an archival range,
and lists the endpoints whose non-empty answers match a strict majority on
both. A chain passes when at least `min_agreeing` endpoints agree. Empty
answers are never evidence: pruned or wrong-chain nodes often answer `[]`.
It only reads public chain data. Endpoint URLs may hold API keys, so they are
read from files and never printed; errors are reduced to fixed categories.
"""
import argparse
import hashlib
import json
import os
import re
import statistics
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from urllib.parse import urlparse

SLUG = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
ADDRESS = re.compile(r"^0x[0-9a-fA-F]{40}$")
TOPIC = re.compile(r"^0x[0-9a-fA-F]{64}$")
QUANTITY = re.compile(r"^0x[0-9a-fA-F]{1,16}$")
HASH = re.compile(r"^0x[0-9a-fA-F]{1,64}$")
TIMEOUT = 15
DEADLINE = 30
MAX_RESPONSE = 16 << 20
RANGES = ("recent", "archive")


class ResponseTooLarge(ValueError):
    pass


class RPCError(Exception):
    def __init__(self, code):
        super().__init__("rpc error")
        self.code = code


def _integer(raw, key, default, minimum, maximum):
    value = raw.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise ValueError("%s must be an integer between %d and %d" % (key, minimum, maximum))
    return value


def _endpoint_url(endpoint):
    """Read and validate one endpoint URL without ever echoing it."""
    source = endpoint.get("url_file")
    if source is not None:
        if not isinstance(source, str) or not source:
            raise ValueError("endpoint %s url_file must be a path" % endpoint["id"])
        try:
            with open(source, encoding="utf-8") as handle:
                url = handle.read().strip()
        except OSError as error:
            raise ValueError("endpoint %s url_file is unreadable (%s)" % (endpoint["id"], type(error).__name__)) from None
    else:
        url = endpoint.get("url")
    message = "endpoint %s needs one http(s) URL without credentials" % endpoint["id"]
    if not isinstance(url, str) or any(c.isspace() for c in url):
        raise ValueError(message)
    try:
        parsed = urlparse(url)
        valid = parsed.scheme in ("http", "https") and bool(parsed.hostname) and parsed.username is None and parsed.password is None
        parsed.port
    except ValueError:
        valid = False
    if not valid:
        raise ValueError(message)
    return url


def load_config_dict(raw):
    if not isinstance(raw, dict) or not isinstance(raw.get("chains"), list) or not raw["chains"]:
        raise ValueError("configuration needs a non-empty chains list")
    chains, names = [], set()
    for chain in raw["chains"]:
        if not isinstance(chain, dict) or not isinstance(chain.get("name"), str) or not SLUG.match(chain["name"]) or chain["name"] in names:
            raise ValueError("chain names must be unique lowercase slugs")
        name = chain["name"]
        names.add(name)
        chain_id = chain.get("chain_id")
        if isinstance(chain_id, bool) or not isinstance(chain_id, int) or chain_id < 1:
            raise ValueError("chain %s needs its numeric chain_id" % name)
        log_filter = chain.get("filter")
        if not isinstance(log_filter, dict) or not isinstance(log_filter.get("address"), str) or not ADDRESS.match(log_filter["address"]):
            raise ValueError("chain %s needs filter.address (0x + 40 hex)" % name)
        topics = log_filter.get("topics", [])
        if not isinstance(topics, list) or not all(isinstance(t, str) and TOPIC.match(t) for t in topics):
            raise ValueError("chain %s filter.topics must be 0x + 64 hex values" % name)
        raw_endpoints = chain.get("endpoints")
        if not isinstance(raw_endpoints, list) or not raw_endpoints:
            raise ValueError("chain %s needs a list of endpoints" % name)
        endpoints, ids = [], set()
        for endpoint in raw_endpoints:
            if not isinstance(endpoint, dict) or not isinstance(endpoint.get("id"), str) or not SLUG.match(endpoint["id"]) or endpoint["id"] in ids:
                raise ValueError("endpoint ids in %s must be unique lowercase slugs" % name)
            if not endpoint.get("url") and not endpoint.get("url_file"):
                raise ValueError("endpoint %s needs url or url_file" % endpoint["id"])
            ids.add(endpoint["id"])
            endpoints.append({"id": endpoint["id"], "url": _endpoint_url(endpoint)})
        cfg = {
            "name": name,
            "chain_id": chain_id,
            "filter": {"address": log_filter["address"], "topics": topics},
            "finality_blocks": _integer(chain, "finality_blocks", 15, 0, 10000),
            "recent_offset": _integer(chain, "recent_offset", 200, 1, 100000),
            "archive_age_blocks": _integer(chain, "archive_age_blocks", 400000, 1, 100000000),
            "span": _integer(chain, "span", 10, 1, 1000),
            "min_agreeing": _integer(chain, "min_agreeing", 2, 2, 50),
            "endpoints": endpoints,
        }
        if cfg["recent_offset"] < cfg["span"] + cfg["finality_blocks"]:
            raise ValueError("chain %s recent_offset must be at least span + finality_blocks" % name)
        if cfg["archive_age_blocks"] < cfg["recent_offset"] + cfg["span"]:
            raise ValueError("chain %s archive_age_blocks must reach beyond the recent range" % name)
        chains.append(cfg)
    return {"chains": chains}


def http_call(url, payload, timeout):
    """POST one JSON-RPC request with a per-socket timeout and a total deadline."""
    body = json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json", "User-Agent": "ocrypt-rpc-provider-probe"})
    started = time.monotonic()
    with urllib.request.urlopen(request, timeout=min(timeout, DEADLINE)) as response:
        chunks, size = [], 0
        while True:
            if time.monotonic() - started > DEADLINE:
                raise TimeoutError("deadline exceeded")
            chunk = response.read1(65536)
            if not chunk:
                break
            size += len(chunk)
            if size > MAX_RESPONSE:
                raise ResponseTooLarge("response too large")
            chunks.append(chunk)
    return json.loads(b"".join(chunks))


def rpc(call, url, method, params):
    response = call(url, {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, TIMEOUT)
    if not isinstance(response, dict):
        raise ValueError("not a JSON-RPC object")
    if response.get("error") is not None:
        error = response["error"]
        raise RPCError(error.get("code") if isinstance(error, dict) else None)
    if "result" not in response:
        raise ValueError("missing result")
    return response["result"]


def categorize(error):
    """Fixed categories only: provider messages and exception text may carry URLs or keys."""
    if isinstance(error, RPCError):
        return "rpc error %s" % (error.code if isinstance(error.code, int) and not isinstance(error.code, bool) else "unknown")
    if isinstance(error, urllib.error.HTTPError):
        return "http %d" % error.code
    if isinstance(error, ResponseTooLarge):
        return "response too large"
    if isinstance(error, TimeoutError) or isinstance(getattr(error, "reason", None), TimeoutError):
        return "timeout"
    if isinstance(error, (ValueError, KeyError, TypeError, AttributeError)):
        return "invalid response"
    return "unreachable"


def _log_key(entry):
    if not isinstance(entry, dict):
        raise ValueError("log is not an object")
    block, transaction, index = entry.get("blockHash"), entry.get("transactionHash"), entry.get("logIndex")
    if not isinstance(block, str) or not HASH.match(block) or not isinstance(transaction, str) or not HASH.match(transaction):
        raise ValueError("log hashes are malformed")
    if isinstance(index, bool):
        raise ValueError("logIndex is malformed")
    if isinstance(index, str) and QUANTITY.match(index):
        index = int(index, 16)
    if not isinstance(index, int) or index < 0:
        raise ValueError("logIndex is malformed")
    return "%s|%s|%d" % (block.lower(), transaction.lower(), index)


def log_digest(logs):
    keys = sorted(_log_key(entry) for entry in logs)
    return hashlib.sha256("\n".join(keys).encode()).hexdigest()[:16]


def _quantity(value):
    if not isinstance(value, str) or not QUANTITY.match(value):
        raise ValueError("not a quantity")
    return int(value, 16)


def _check_endpoint(call, endpoint, chain_id):
    try:
        if _quantity(rpc(call, endpoint["url"], "eth_chainId", [])) != chain_id:
            return "wrong chain", None
    except Exception as error:  # noqa: BLE001 - report the category, keep probing the rest
        return categorize(error), None
    try:
        return "ok", _quantity(rpc(call, endpoint["url"], "eth_blockNumber", []))
    except Exception:  # noqa: BLE001 - an endpoint without a valid head is not admissible
        return "ok", None


def _logs(call, url, log_filter, first, last):
    params = [{"fromBlock": hex(first), "toBlock": hex(last), "address": log_filter["address"], "topics": log_filter["topics"]}]
    try:
        logs = rpc(call, url, "eth_getLogs", params)
        if not isinstance(logs, list):
            raise ValueError("logs are not a list")
        if not logs:
            return {"status": "empty", "logs": 0}
        return {"status": "ok", "logs": len(logs), "digest": log_digest(logs)}
    except Exception as error:  # noqa: BLE001 - report the category, keep probing the rest
        return {"status": "error", "error": categorize(error)}


def _majority(answers):
    """The digest held by more than half of the answering endpoints; empty answers count as votes for nothing."""
    answered = [answer for answer in answers if answer["status"] in ("ok", "empty")]
    counts = Counter(answer["digest"] for answer in answered if answer["status"] == "ok")
    if not counts:
        return None
    digest, count = counts.most_common(1)[0]
    return digest if count * 2 > len(answered) else None


def probe_chain(chain, call):
    result = {"chain": chain["name"], "min_agreeing": chain["min_agreeing"], "head": None, "ranges": {}, "endpoints": {}, "majority": {"recent": None, "archive": None}, "agreeing": [], "verdict": "insufficient"}
    eligible = []
    for endpoint in chain["endpoints"]:
        status, head = _check_endpoint(call, endpoint, chain["chain_id"])
        result["endpoints"][endpoint["id"]] = {"chain": status, "head": head}
        if status == "ok" and head is not None:
            eligible.append(endpoint)
    heads = sorted(result["endpoints"][endpoint["id"]]["head"] for endpoint in eligible)
    if not heads:
        return result
    head = int(statistics.median_low(heads))
    result["head"] = head
    recent_last = head - chain["recent_offset"] + chain["span"] - 1
    archive_first = max(0, head - chain["archive_age_blocks"])
    result["ranges"] = {
        "recent": {"from": max(0, head - chain["recent_offset"]), "to": max(0, recent_last)},
        "archive": {"from": archive_first, "to": archive_first + chain["span"] - 1},
    }
    for endpoint in eligible:
        for name in RANGES:
            window = result["ranges"][name]
            result["endpoints"][endpoint["id"]][name] = _logs(call, endpoint["url"], chain["filter"], window["from"], window["to"])
    for name in RANGES:
        answers = [result["endpoints"][endpoint["id"]][name] for endpoint in eligible]
        result["majority"][name] = _majority(answers)
        for answer in answers:
            answer["agrees"] = answer["status"] == "ok" and result["majority"][name] is not None and answer["digest"] == result["majority"][name]
    result["agreeing"] = sorted(endpoint["id"] for endpoint in eligible if all(result["endpoints"][endpoint["id"]][name]["agrees"] for name in RANGES))
    result["verdict"] = "ok" if len(result["agreeing"]) >= chain["min_agreeing"] else "insufficient"
    return result


def run(cfg, call=http_call):
    return {"chains": [probe_chain(chain, call) for chain in cfg["chains"]]}


def exit_code(report):
    return 0 if all(chain["verdict"] == "ok" for chain in report["chains"]) else 1


def format_report(report):
    lines = []
    for chain in report["chains"]:
        lines.append("%s: %s (%d of %d required agree; reference head %s)" % (chain["chain"], chain["verdict"].upper(), len(chain["agreeing"]), chain["min_agreeing"], chain["head"]))
        for endpoint_id, data in sorted(chain["endpoints"].items()):
            if data["chain"] != "ok":
                lines.append("  %-20s %s" % (endpoint_id, data["chain"]))
                continue
            if data["head"] is None:
                lines.append("  %-20s no valid head" % endpoint_id)
                continue
            cells = []
            for name in RANGES:
                answer = data.get(name)
                if answer is None:
                    cells.append("%s: -" % name)
                elif answer["status"] == "ok":
                    cells.append("%s: %d logs %s" % (name, answer["logs"], "agrees" if answer["agrees"] else "DISAGREES"))
                elif answer["status"] == "empty":
                    cells.append("%s: empty (not evidence)" % name)
                else:
                    cells.append("%s: %s" % (name, answer["error"]))
            lines.append("  %-20s head %-12s %s" % (endpoint_id, data["head"], "; ".join(cells)))
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--config", required=True, help="JSON configuration file")
    parser.add_argument("--json", metavar="PATH", help="also write the JSON report to PATH")
    args = parser.parse_args()
    try:
        with open(args.config, encoding="utf-8") as handle:
            cfg = load_config_dict(json.load(handle))
    except (OSError, ValueError) as error:
        print("rpc-provider-probe: invalid configuration: %s" % error, file=sys.stderr)
        return 2
    report = run(cfg)
    print(format_report(report))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as output:
            json.dump(report, output, indent=2, sort_keys=True)
        os.chmod(args.json, 0o644)
    return exit_code(report)


if __name__ == "__main__":
    sys.exit(main())
