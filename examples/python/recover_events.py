#!/usr/bin/env python3
"""Pull one durable event page and print the next merchant-local sequence."""
import json, os, urllib.request
from create_intent import sign_headers

base_url = os.environ.get("MERCHANT_BASE_URL", "http://127.0.0.1:8080").rstrip("/")
key_id, secret = os.environ.get("MERCHANT_KEY_ID"), os.environ.get("MERCHANT_SECRET")
if not key_id or not secret: raise SystemExit("MERCHANT_KEY_ID and MERCHANT_SECRET are required")
after = os.environ.get("MERCHANT_AFTER_SEQUENCE", "0")
if not after.isdigit() or (len(after) > 1 and after[0] == "0"): raise SystemExit("MERCHANT_AFTER_SEQUENCE must be canonical")
url = f"{base_url}/v1/events?after_sequence={after}&limit=100"
request = urllib.request.Request(url, headers={"Accept": "application/json", **sign_headers("GET", url, b"", key_id=key_id, secret=secret)})
with urllib.request.urlopen(request, timeout=15) as response: envelope = json.load(response)
print(json.dumps(envelope["data"]["items"], ensure_ascii=False, indent=2))
print("Persist next contiguous cursor:", envelope["data"]["next_sequence"])
