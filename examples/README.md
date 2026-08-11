# Integration examples

These examples are deliberately small, dependency-free reference integrations.
They show the security and money invariants that an SDK or framework middleware
must preserve; they are not a substitute for order-specific business rules.

## API request examples

- [`python/create_intent.py`](python/create_intent.py) signs and creates a
  payment intent with Python's standard library.
- [`node/create-intent.mjs`](node/create-intent.mjs) implements the same request
  with Node.js 22.
- [`python/recover_events.py`](python/recover_events.py) and
  [`node/recover-events.mjs`](node/recover-events.mjs) pull durable events using
  `after_sequence` and preserve the returned `next_sequence` as a decimal string.

Both clients sign the exact bytes sent on the wire. The API authentication
headers are `Merchant-Key-Id`, `Merchant-Timestamp`, `Merchant-Nonce`,
`Content-Digest`, and `Merchant-Signature`. The signature input is:

```text
METHOD\nCANONICAL_PATH_AND_QUERY\nTIMESTAMP\nNONCE\nSHA256_HEX(RAW_BODY)
```

Run either client only with sandbox credentials:

```bash
export MERCHANT_BASE_URL=http://127.0.0.1:8080
export MERCHANT_KEY_ID=mk_sandbox_example
export MERCHANT_SECRET='replace-with-a-sandbox-secret'
python examples/python/create_intent.py

# or
node examples/node/create-intent.mjs
```

The example order ID can be overridden with `MERCHANT_ORDER_ID`. Every retry of
the same logical create must keep the same `Idempotency-Key` and body. A new
body requires a new key.

## Webhook consumers

- [`python/webhook_consumer.py`](python/webhook_consumer.py) is an executable
  SQLite-backed inbox/outbox consumer. It verifies the raw body, rejects stale
  or modified signatures, detects an event-ID/body conflict, and commits the
  local order update and fulfillment outbox in one transaction.
- [`node/webhook-consumer.mjs`](node/webhook-consumer.mjs) demonstrates the raw
  body and signature boundary for Node.js HTTP frameworks. It intentionally
  delegates the durable inbox transaction to a repository function so an
  application can implement it in PostgreSQL, MySQL, or another transactional
  store.

Webhook deliveries use:

```text
Merchant-Webhook-Signature: t=<unix>,key=<key-id>,event=<event-id>,v1=<base64url HMAC>
Merchant-Delivery-Id: <unique attempt id>
Content-Digest: sha-256=:<base64 SHA-256 of raw body>:
```

The webhook signature input is `<event-id>.<unix-timestamp>.<raw-body-bytes>`.
Retries keep the event ID and body unchanged while the delivery ID, timestamp,
and signature change.

To run the Python receiver:

```bash
export WEBHOOK_SECRET='replace-with-a-webhook-secret'
export WEBHOOK_KEY_ID=whsec_sandbox_example
export WEBHOOK_DB=./webhook-example.sqlite3
export WEBHOOK_DEMO_ORDER=order-2026-00042
export WEBHOOK_DEMO_AMOUNT_MINOR=49900
export WEBHOOK_DEMO_CURRENCY=RUB
python examples/python/webhook_consumer.py
```

The receiver listens on `127.0.0.1:8090` unless `WEBHOOK_HOST` or
`WEBHOOK_PORT` is set. Do not expose the demonstration server directly to the
Internet.

## Rules to copy into a real integration

1. Read a size-limited raw body before JSON decoding.
2. Verify timestamp, key version, content digest, event ID, and HMAC with a
   constant-time comparison.
3. Keep sandbox/live keys and expected merchant/environment separate.
4. Store `(event_id, body_sha256)` durably. An identical repeat is success; the
   same ID with a different digest is a security incident.
5. Fulfill only `payment.settled`. `observed`, `confirmed`, and
   `partially_paid` are informational.
6. Compare the order reference, fiat amount, currency, merchant, and
   environment with local truth even after the signature passes.
7. Update the local order and enqueue fulfillment in the same database
   transaction. Never send the product first and write the event ID later.
8. Treat money and crypto raw amounts as strings or arbitrary-size integers,
   never binary floating point.
9. Handle `payment.reorged` as an explicit recovery/risk workflow; do not erase
   the original payment silently.
10. Reject invalid UTF-8, duplicate JSON keys, unsupported schema versions, and
    non-positive sequence numbers. Delivery order is not a state transition.

For payment links, checkout capabilities, transfer/quote lookup, and signed
reconciliation reports, use the language SDK rather than copying additional
signing code. The complete surface and least-privilege scopes are documented in
the [SDK index](../sdk/README.md) and [API integration guide](../docs/en/api-integration.md).

Production-oriented framework transaction skeletons are indexed in
[`frameworks/README.md`](frameworks/README.md). They cover FastAPI/Django,
Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, a Telegram backend, and a
generic e-commerce order without product-specific business logic.
