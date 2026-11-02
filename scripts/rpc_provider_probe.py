#!/usr/bin/env python3
"""Prove which EVM RPC providers agree on recent and archival eth_getLogs ranges.

For every configured chain the probe asks each endpoint for its head, takes the
median as the reference, runs one address/topic filter on a recent and on an
archival range, and lists the endpoints whose answers match the unique
majority on both. A chain passes when at least `min_agreeing` endpoints agree.
It only reads public chain data. Endpoint URLs may hold API keys, so they are
read from files and never printed; errors are reduced to fixed categories.
"""
import argparse
import hashlib
import json
import re
import statistics
import sys
import urllib.error
import urllib.request
from collections import Counter
from urllib.parse import urlparse

SLUG = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
ADDRESS = re.compile(r"^0x[0-9a-fA-F]{40}$")
TOPIC = re.compile(r"^0x[0-9a-fA-F]{64}$")
TIMEOUT = 15
RANGES = ("recent", "archive")


def _integer(raw, key, default, minimum, maximum):
    value = raw.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise ValueError("%s must be an integer between %d and %d" % (key, minimum, maximum))
    return value


def _endpoint_url(endpoint):
    if endpoint.get("url_file"):
        with open(endpoint["url_file"], encoding="utf-8") as handle:
            url = handle.read().strip()
    else:
        url = endpoint.get("url", "")
    parsed = urlparse(url) if isinstance(url, str) else None
    if not parsed or parsed.scheme not in ("http", "https") or not parsed.hostname or any(c.isspace() for c in url):
        raise ValueError("endpoint %s needs one http(s) URL" % endpoint.get("id"))
    return url


def load_config_dict(raw):
    if not isinstance(raw, dict) or not isinstance(raw.get("chains"), list) or not raw["chains"]:
        raise ValueError("configuration needs a non-empty chains list")
    chains, names = [], set()
    for chain in raw["chains"]:
        if not isinstance(chain, dict) or not SLUG.match(str(chain.get("name", ""))) or chain["name"] in names:
            raise ValueError("chain names must be unique lowercase slugs")
        names.add(chain["name"])
        log_filter = chain.get("filter")
        if not isinstance(log_filter, dict) or not ADDRESS.match(str(log_filter.get("address", ""))):
            raise ValueError("chain %s needs filter.address (0x + 40 hex)" % chain["name"])
        topics = log_filter.get("topics", [])
        if not isinstance(topics, list) or not all(isinstance(t, str) and TOPIC.match(t) for t in topics):
            raise ValueError("chain %s filter.topics must be 0x + 64 hex values" % chain["name"])
        endpoints, ids = [], set()
        for endpoint in chain.get("endpoints") or []:
            if not isinstance(endpoint, dict) or not SLUG.match(str(endpoint.get("id", ""))) or endpoint["id"] in ids:
                raise ValueError("endpoint ids in %s must be unique lowercase slugs" % chain["name"])
            if not endpoint.get("url") and not endpoint.get("url_file"):
                raise ValueError("endpoint %s needs url or url_file" % endpoint["id"])
            try:
                url = _endpoint_url(endpoint)
            except OSError as error:
                raise ValueError("endpoint %s url_file is unreadable (%s)" % (endpoint["id"], type(error).__name__)) from None
            ids.add(endpoint["id"])
            endpoints.append({"id": endpoint["id"], "url": url})
        if not endpoints:
            raise ValueError("chain %s needs endpoints" % chain["name"])
        chains.append({
            "name": chain["name"],
            "filter": {"address": log_filter["address"], "topics": topics},
            "recent_offset": _integer(chain, "recent_offset", 200, 0, 100000),
            "archive_age_blocks": _integer(chain, "archive_age_blocks", 400000, 1, 100000000),
            "span": _integer(chain, "span", 10, 1, 1000),
            "min_agreeing": _integer(chain, "min_agreeing", 2, 1, 50),
            "endpoints": endpoints,
        })
    return {"chains": chains}


def http_call(url, payload, timeout):
    body = json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json", "User-Agent": "ocrypt-rpc-provider-probe"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read(16 << 20))


class RPCError(Exception):
    def __init__(self, code):
        super().__init__("rpc error %s" % code)
        self.code = code


def rpc(call, url, method, params):
    response = call(url, {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, TIMEOUT)
    if not isinstance(response, dict):
        raise ValueError("not a JSON-RPC object")
    if response.get("error") is not None:
        error = response["error"]
        raise RPCError(error.get("code") if isinstance(error, dict) else "unknown")
    if "result" not in response:
        raise ValueError("missing result")
    return response["result"]


def categorize(error):
    """Fixed categories only: provider messages and exception text may carry URLs or keys."""
    if isinstance(error, RPCError):
        return "rpc error %s" % (error.code if isinstance(error.code, int) else "unknown")
    if isinstance(error, urllib.error.HTTPError):
        return "http %d" % error.code
    if isinstance(error, TimeoutError) or "timed out" in type(error).__name__.lower():
        return "timeout"
    if isinstance(error, (ValueError, KeyError, TypeError)):
        return "invalid response"
    return "unreachable"


def log_digest(logs):
    keys = sorted("%s|%s|%s" % (entry["blockHash"].lower(), entry["transactionHash"].lower(), str(entry["logIndex"]).lower()) for entry in logs)
    return hashlib.sha256("\n".join(keys).encode()).hexdigest()[:16]


def _head(call, url):
    try:
        value = rpc(call, url, "eth_blockNumber", [])
        return int(value, 16)
    except Exception:  # noqa: BLE001 - a missing head only removes this endpoint from the median
        return None


def _logs(call, url, log_filter, first, last):
    params = [{"fromBlock": hex(first), "toBlock": hex(last), "address": log_filter["address"], "topics": log_filter["topics"]}]
    try:
        logs = rpc(call, url, "eth_getLogs", params)
        if not isinstance(logs, list):
            raise ValueError("logs are not a list")
        return {"status": "ok", "logs": len(logs), "digest": log_digest(logs)}
    except Exception as error:  # noqa: BLE001 - report the category, keep probing the rest
        return {"status": "error", "error": categorize(error)}


def _majority(answers):
    counts = Counter(answer["digest"] for answer in answers if answer["status"] == "ok")
    if not counts:
        return None
    ranked = counts.most_common()
    if len(ranked) > 1 and ranked[0][1] == ranked[1][1]:
        return None
    return ranked[0][0]


def probe_chain(chain, call):
    heads = {endpoint["id"]: _head(call, endpoint["url"]) for endpoint in chain["endpoints"]}
    known = sorted(value for value in heads.values() if value is not None)
    result = {"chain": chain["name"], "min_agreeing": chain["min_agreeing"], "head": None, "ranges": {}, "endpoints": {}, "majority": {"recent": None, "archive": None}, "agreeing": [], "verdict": "insufficient"}
    for endpoint in chain["endpoints"]:
        result["endpoints"][endpoint["id"]] = {"head": heads[endpoint["id"]]}
    if not known:
        return result
    head = int(statistics.median_high(known))
    result["head"] = head
    recent_first = max(0, head - chain["recent_offset"])
    archive_first = max(0, head - chain["archive_age_blocks"])
    result["ranges"] = {
        "recent": {"from": recent_first, "to": recent_first + chain["span"] - 1},
        "archive": {"from": archive_first, "to": archive_first + chain["span"] - 1},
    }
    for endpoint in chain["endpoints"]:
        for name in RANGES:
            window = result["ranges"][name]
            result["endpoints"][endpoint["id"]][name] = _logs(call, endpoint["url"], chain["filter"], window["from"], window["to"])
    for name in RANGES:
        answers = [result["endpoints"][endpoint["id"]][name] for endpoint in chain["endpoints"]]
        result["majority"][name] = _majority(answers)
        for answer in answers:
            answer["agrees"] = answer["status"] == "ok" and result["majority"][name] is not None and answer["digest"] == result["majority"][name]
    result["agreeing"] = sorted(endpoint_id for endpoint_id, data in result["endpoints"].items() if all(data.get(name, {}).get("agrees") for name in RANGES))
    result["verdict"] = "ok" if len(result["agreeing"]) >= chain["min_agreeing"] else "insufficient"
    return result


def run(cfg, call=http_call):
    return {"chains": [probe_chain(chain, call) for chain in cfg["chains"]]}


def exit_code(report):
    return 0 if all(chain["verdict"] == "ok" for chain in report["chains"]) else 1


def format_report(report):
    lines = []
    for chain in report["chains"]:
        lines.append("%s: %s (%d of %d required agree; head %s)" % (chain["chain"], chain["verdict"].upper(), len(chain["agreeing"]), chain["min_agreeing"], chain["head"]))
        for endpoint_id, data in sorted(chain["endpoints"].items()):
            cells = []
            for name in RANGES:
                answer = data.get(name)
                if answer is None:
                    cells.append("%s: -" % name)
                elif answer["status"] == "ok":
                    cells.append("%s: %d logs %s" % (name, answer["logs"], "agrees" if answer.get("agrees") else "DISAGREES"))
                else:
                    cells.append("%s: %s" % (name, answer["error"]))
            lines.append("  %-20s head %-12s %s" % (endpoint_id, data["head"], "; ".join(cells)))
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--config", required=True, help="JSON configuration file")
    parser.add_argument("--json", action="store_true", help="print the JSON report instead of the table")
    args = parser.parse_args()
    try:
        with open(args.config, encoding="utf-8") as handle:
            cfg = load_config_dict(json.load(handle))
    except (OSError, ValueError) as error:
        print("rpc-provider-probe: invalid configuration: %s" % error, file=sys.stderr)
        return 2
    report = run(cfg)
    print(json.dumps(report, indent=2, sort_keys=True) if args.json else format_report(report))
    return exit_code(report)


if __name__ == "__main__":
    sys.exit(main())
