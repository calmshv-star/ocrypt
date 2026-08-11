# Merchant Platform SDKs

[English](README.md) · [简体中文](docs/zh-CN.md) · [Español](docs/es.md) · [Français](docs/fr.md) · [Deutsch](docs/de.md) · [Русский](docs/ru.md)

Source SDKs for TypeScript, Python, Go, PHP, Java, and .NET implement the stable merchant surface described by `contracts/openapi.yaml` and the server-to-server aliases in `contracts/management-openapi.yaml`. They are pre-release source packages and are not published to public registries.

## Quick start

```ts
import { MerchantClient } from "@merchant-platform/sdk";

const client = new MerchantClient({
  baseUrl: process.env.MERCHANT_API_URL!,
  keyId: process.env.MERCHANT_KEY_ID!,
  secret: process.env.MERCHANT_SECRET!
});

const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42",
  amount_minor: "49900",
  currency: "USD",
  currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42:create");
```

All fiat minor amounts, atomic asset amounts, block heights, ledger sequences, sizes, and totals remain canonical decimal strings. Never pass them through a floating-point type. Mutations require an operation-scoped idempotency key; a replay must reuse the exact method, target, and body.

## Stable coverage

Every language client exposes the same operation groups, using native typed models where practical and JSON maps/nodes for forward-compatible operational views:

- payment intents: create/list/get, routes, cancel, explicit expire, and optimistic metadata replacement;
- assets and payment-proof submission/status;
- durable events by `after_sequence`, exact stored event detail, transfers by transaction, and quote provenance;
- ledger balances, operational reconciliation, and asynchronous signed JSONL reconciliation reports;
- server-to-server payment links and checkout capability issuance;
- unauthenticated public payment-link redemption, checkout polling, and route selection;
- raw-byte webhook verification, key rotation, mandatory acknowledgement, and an atomic inbox interface.

Operator exception, AI ranking, credential administration, treasury, refund, and platform-admin operations are intentionally absent from `MerchantClient`. They require human/admin authorization and are not merchant server credentials.

## Least-privilege scopes

| Workload | Scopes |
| --- | --- |
| Create/modify payments and proofs | `payments:write` |
| Read intents, routes, assets, transfers, quotes, balances, summary | `payments:read` |
| Pull events only | `events:read` (or `payments:read`) |
| Create/status/download ledger exports | `reconciliation:read` |
| Payment-link reads | `payment-links:read` |
| Payment-link writes | `payment-links:write` |
| Checkout capability issuance | `checkout:write` |

Do not put merchant HMAC credentials in a browser or mobile application. `CheckoutClient` accepts only high-entropy `pl_…`/`cs_…` capabilities and deliberately sends no `Merchant-*` headers.

When API and management have different origins, instantiate a second
`MerchantClient` with the management/gateway origin for payment-link and
checkout-issuance methods. The same HMAC profile applies, but the key still
needs the matching management scopes.

## Authentication and recovery

Each request serializes its body once and signs those exact bytes:

```text
METHOD + "\n" + CANONICAL_PATH_AND_QUERY + "\n" +
UNIX_TIMESTAMP + "\n" + SINGLE_USE_NONCE + "\n" +
LOWERCASE_SHA256_HEX(RAW_BODY)
```

Clients send `Merchant-Key-Id`, `Merchant-Timestamp`, `Merchant-Nonce`, `Content-Digest`, and unpadded base64url `Merchant-Signature`. Queries are form-encoded in sorted key order. Base URLs must be HTTPS except exact loopback development; redirects are rejected where the runtime permits.

Webhook delivery is at least once and may be out of order. Verify the untouched body, commit `(event_id, body_digest)`, the order update, and fulfillment outbox in one transaction, then return `{"acknowledged_event_id":"…"}`. Persist the last contiguous merchant `sequence`; repair gaps using `listEvents(afterSequence)`, and use `getEvent` when exact canonical bytes are needed. Only `payment.settled` normally grants a product.

`payment.observed`, `payment.confirming`, and pre-settlement `payment.reorged` carry the bounded `observation` object (canonical transfer, current/required confirmations, finality, and evidence digest). `payment.resolution.updated` carries a secret-free `resolution` object. Treat both as informational; they never authorize fulfillment.

## Domain-safe integration helpers

All six source clients ship an explicit retry wrapper. It runs only when the
application calls it, retries only errors classified as retryable, honors a
bounded `Retry-After`, applies capped jittered backoff otherwise, and rejects an
unsafe operation without an idempotency key. Pagination helpers follow opaque
UUID cursors and decimal event sequences without using floating point.

Dependency-free telemetry hooks emit only a caller-supplied low-cardinality
operation name, HTTP method, status, duration, and retryability. They never emit
URL, resource ID, query, body, headers, nonce, signature, capability, or secret.
Explicit live/sandbox endpoint configs keep environment selection visible; the
application must also separate credentials, data, webhook keys, and fulfillment.

Python provides `python -m merchant_platform.cli`; TypeScript provides
`bin/verify-webhook.mjs` after build. Both verify bounded offline fixtures, read
the secret from a file rather than argv, and print only safe event identity.

## Reconciliation reports

Create a report with `period_start < period_end <= now`; the maximum period is 366 days. Poll until `ready`, then download once and check all four identities: byte length, SHA-256, `signing_key_id`, and Ed25519 signature over:

```text
"merchant-reconciliation-jsonl-v1\0" + report_id + "\0" +
snapshot_ledger_sequence + "\0" + SHA256(downloaded_bytes)
```

Keep a public-key ring indexed by key ID until every report signed by an old key has passed its retention period. TypeScript, Python (`[reports]` extra), Go, PHP (`ext-sodium`), and Java include full verification helpers. The .NET source exposes exact digest and signature-message helpers; connect them to the deployment's audited Ed25519 provider.

Go exposes a streaming response; close it and verify the stream. Other source
clients buffer with a 256 MiB fail-closed default (TypeScript/Python allow an
explicit client limit). For larger admitted exports, use a streaming adapter or
split the reporting period instead of removing the bound. Report downloads may
need a longer timeout than ordinary API requests.

## Verification matrix

| SDK | Local evidence in this workspace |
| --- | --- |
| TypeScript | TypeScript compile + golden request/webhook/checkout tests |
| Python | import/bytecode compile + golden tests |
| Go | `gofmt` + package tests |
| PHP | Source and vectors present; PHP runtime unavailable in the current workspace |
| Java | Source and vectors present; JDK/Maven unavailable in the current workspace |
| .NET | Source and vectors present; .NET runtime unavailable in the current workspace |

Golden byte vectors are in [`fixtures/golden-vectors.json`](fixtures/golden-vectors.json). Runtime-independent surface parity is enforced by repository contract tests. Live API/provider and published-package tests remain release admission requirements.
