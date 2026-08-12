#!/usr/bin/env python3
"""Seed new EVM scanners at a two-provider finalized block.

This prevents a newly enabled chain from replaying its entire history. Existing
cursors are never changed. Run after enable-public-assets.sh and before starting
the new scanner containers.
"""

import json
import os
import pathlib
import re
import subprocess
import sys
import time
import urllib.request


HASH = re.compile(r"^0x[0-9a-f]{64}$")
CHAIN = re.compile(r"^eip155:[1-9][0-9]*$")


def rpc(endpoint, method, params):
    payload = json.dumps(
        {"jsonrpc": "2.0", "id": 1, "method": method, "params": params},
        separators=(",", ":"),
    ).encode()
    request = urllib.request.Request(
        endpoint,
        data=payload,
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=15) as response:
        if response.status != 200:
            raise RuntimeError(f"RPC returned HTTP {response.status}")
        envelope = json.load(response)
    if envelope.get("error") is not None or envelope.get("result") is None:
        raise RuntimeError(f"RPC {method} did not return a result")
    time.sleep(0.3)
    return envelope["result"]


def psql(database_url, sql):
    result = subprocess.run(
        ["psql", database_url, "-X", "-v", "ON_ERROR_STOP=1", "-Atc", sql],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def finalized_anchor(item):
    chain_id = item["chain_id"]
    expected_chain = int(chain_id.split(":", 1)[1])
    expected_genesis = item["genesis_hash"].lower()
    endpoints = item["endpoints"]
    if not CHAIN.fullmatch(chain_id) or not HASH.fullmatch(expected_genesis) or len(endpoints) != 2:
        raise RuntimeError(f"invalid catalog identity for {item.get('name', chain_id)}")

    finalized_heights = []
    for endpoint in endpoints:
        reported_chain = int(rpc(endpoint, "eth_chainId", []), 16)
        genesis = rpc(endpoint, "eth_getBlockByNumber", ["0x0", False])
        finalized = rpc(endpoint, "eth_getBlockByNumber", ["finalized", False])
        if reported_chain != expected_chain or genesis["hash"].lower() != expected_genesis:
            raise RuntimeError(f"provider identity mismatch for {chain_id}")
        finalized_heights.append(int(finalized["number"], 16))

    height = min(finalized_heights)
    tag = hex(height)
    anchors = [rpc(endpoint, "eth_getBlockByNumber", [tag, False]) for endpoint in endpoints]
    hashes = {anchor["hash"].lower() for anchor in anchors}
    if len(hashes) != 1 or any(int(anchor["number"], 16) != height for anchor in anchors):
        raise RuntimeError(f"providers disagree on finalized anchor for {chain_id}")
    block_hash = hashes.pop()
    if not HASH.fullmatch(block_hash):
        raise RuntimeError(f"invalid finalized hash for {chain_id}")
    return height, block_hash


def main():
    database_url = os.environ.get("MIGRATION_DATABASE_URL", "")
    if not database_url:
        raise RuntimeError("MIGRATION_DATABASE_URL is required")
    catalog_path = pathlib.Path(__file__).with_name("public-evm-networks.json")
    catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
    for item in catalog:
        chain_id = item["chain_id"]
        height, block_hash = finalized_anchor(item)
        existing = psql(
            database_url,
            "SELECT cursor_height::text||E'\\t'||COALESCE(cursor_hash,'') "
            "FROM scanner_cursors "
            f"WHERE chain_id='{chain_id}' AND scanner_shard='default' "
            "AND capability='normalized_transfers_v1'",
        )
        if existing:
            fields = existing.split("\t", 1)
            if len(fields) != 2 or int(fields[0]) <= 0 or not HASH.fullmatch(fields[1]):
                raise RuntimeError(f"existing cursor for {chain_id} is not safely initialized")
            print(f"{chain_id}: existing cursor left unchanged at {fields[0]}")
            continue
        psql(
            database_url,
            "INSERT INTO scanner_cursors("
            "chain_id,scanner_shard,capability,cursor_height,cursor_hash,version,updated_at) "
            f"VALUES('{chain_id}','default','normalized_transfers_v1',{height},'{block_hash}',1,clock_timestamp())",
        )
        stored = psql(
            database_url,
            "SELECT cursor_height::text||E'\\t'||COALESCE(cursor_hash,'') "
            "FROM scanner_cursors "
            f"WHERE chain_id='{chain_id}' AND scanner_shard='default' "
            "AND capability='normalized_transfers_v1'",
        )
        if stored != f"{height}\t{block_hash}":
            raise RuntimeError(f"cursor verification failed for {chain_id}")
        print(f"{chain_id}: seeded at finalized block {height}")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"cursor seeding failed: {error}", file=sys.stderr)
        sys.exit(1)
