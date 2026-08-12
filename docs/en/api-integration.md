# API integration guide

## Boundary and credentials

Create a separate HMAC client per workload and environment. A typical payment backend needs `payments:write`, `payments:read`, and `events:read`; exports need a separate `reconciliation:read` credential. Add `payment-links:read`, `payment-links:write`, or `checkout:write` only when that workload uses them. Never expose the key or secret to a browser, bot user, mobile app, checkout page, log, URL, or support ticket.

Use the API origin for merchant requests and the management/gateway origin for payment-link and checkout aliases. Public `pl_…` and `cs_…` capabilities are intentionally unauthenticated, high entropy, short-lived or use-limited bearer secrets.

## Invoice currencies and rates

Invoice currency is not hard-coded to RUB. `currency` is exactly three uppercase ASCII letters and should be an ISO 4217 code; `currency_scale` explicitly defines its minor units. `RUB`, `USD`, `EUR`, `KZT`, `INR`, and `CNY` normally use scale `2`, so `amount_minor: "3813"` means `38.13` in the selected currency. The API does not infer the scale from the code.

Accepting a currency code does not make a crypto quote available by itself. Before creating an on-chain or hosted route, production must have a fresh admitted rate for the exact `asset_id`/currency pair. Missing, stale, non-quorate, future-dated, or excessively divergent rates fail closed; configure and admit independent normalized rate sources for every currency you sell in.

## Payment lifecycle

1. Create one intent with a unique `merchant_order_id`, exact `amount_minor`, currency scale, expiry, and allowed routes.
2. Create a route when the customer chooses a network/asset. Store its exact atomic amount, address/memo, quote expiry, and `grace_ends_at`.
3. Display only route data returned by the API. A wallet receipt or submitted proof is a lookup hint, not payment acceptance.
4. Verify and durably process webhooks. Fulfill on `payment.settled`; make fulfillment idempotent by intent/order.
5. Poll `GET /v1/events?after_sequence=N` to repair gaps. Delivery can be duplicated or out of order.

Cancel and explicit expire stop normal waiting but do not erase chain history. The route remains matchable through its grace window, so late transfers can become `needs_review`, a policy outcome, or a later settlement. Never immediately recycle an address when an intent expires.

Metadata updates are non-financial, replace the allowlisted object, and require `expected_version`. A `409` means another actor changed state; fetch the intent, reconsider the desired change, and use a new idempotency key/body.

## Exact signing and retries

Sign the exact serialized request bytes and canonical path/query. Nonces are single use; timestamps must fit the configured skew. Reusing an idempotency key with a different request returns `409`. Retry transport errors, `429`, and selected `5xx` with jittered exponential backoff while retaining the same body and idempotency key. Never automatically retry a version conflict or validation failure.

## Webhooks and key rotation

Read the raw body before JSON parsing. Verify `Content-Digest`, timestamp tolerance, event ID, key ID, and the HMAC over `<event-id>.<timestamp>.<raw-body>`. Resolve secrets by `key_id`; keep old and new keys during overlap. In one database transaction, claim `(event_id, body_digest)`, update the order, enqueue fulfillment, and commit. Only then return the exact JSON acknowledgement. A repeated event with the same digest is safe; the same ID with another digest is an incident.

## Payment links and checkout

Payment links currently bind exactly one allowed route. Creation returns `public_url` only on the original response or exact idempotent replay; list/get responses do not reveal the bearer token. Redemption atomically consumes a use, creates the intent/quote/address/route, and returns a `cs_…` checkout capability. Return URLs are fixed by the merchant and cannot be overridden by the browser.

Embedded checkout tokens must be bound to one exact HTTPS origin. Public polling uses `Cache-Control: no-store`; route selection requires the capability to include `select_route` and an idempotency key. Derive explorer URLs from a trusted network allowlist rather than trusting arbitrary links.

## Reconciliation

Use `GET /v1/balances` for exact debit/credit totals and `GET /v1/reconciliation` for operational counts. For an audit export, request a period no longer than 366 days and not in the future, poll its durable state, then download when `ready`. Verify length, SHA-256, frozen key ID, and Ed25519 signature before parsing JSONL. Retain historical public keys for the full report-retention window. The header freezes the global ledger sequence and cutoff; entries are ordered by ledger/entry sequence; the footer contains exact string totals.

## Sandbox and go-live

Exercise exact, partial, under-, over-, late, wrong-asset, duplicate and out-of-order callback, timeout, dead-letter, reorg, and reorg-recovery scenarios. Test progressive confirmations/finality, webhook key overlap, event-gap recovery, and a report signed by both the current and retiring keys. Sandbox success is not production admission: production also requires real provider diversity, finality/reorg drills, PostgreSQL restore evidence, pinned images, secret rotation, and load/soak evidence.

The merchant team/settings API is a separate cabinet surface and is not called with merchant HMAC SDK methods. Its backend contract exists, while BFF/browser activation remains pre-release; see [merchant team settings](merchant-team-settings.md) after that component's handoff.

Reference transaction adapters for FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, Telegram, and generic commerce are in the [framework skeleton index](../../examples/frameworks/README.md). They are adaptation templates, not installed dependencies.
