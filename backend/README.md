# Merchant Platform backend

This directory contains the chain-neutral payment core. It intentionally has no
store, subscription, entitlement, or product-delivery logic. Integrators fulfill
their own orders after consuming an authenticated `payment.settled` event.

## Implemented foundation

- exact 256-bit atomic and fiat-minor amounts represented as JSON strings;
- payment-intent, route, transfer, unmatched-payment, and reorg state models;
- tenant-scoped idempotent payment-intent, route, read, and cancellation use cases;
- exact-amount reservation collision protection;
- transfer identity that includes chain, transaction, event index, asset, and recipient;
- per-asset double-entry ledger validation;
- transactional domain event envelopes and outbox records;
- HMAC request authentication with body digest, timestamp, and single-use nonce;
- a pgx v5.10 PostgreSQL store with serializable tenant transactions and durable nonces;
- exact rational rate quotes, immutable quote provenance, and leased address-pool allocation;
- canonical transfer ingestion and exact finalized settlement in one ledger/callback/outbox transaction;
- signed webhook contract, SSRF-safe HTTPS/IP pinning, durable leases, retry, and dead-letter behavior;
- chain adapter contract for replay-safe scanners and independent verification;
- PostgreSQL 18 schema with RLS, deferred ledger checks, leaseable jobs, inbox/outbox,
  immutable event identity, and overlapping-reservation exclusion;
- strict `net/http` API handlers and an OpenAPI 3.1 contract.
- deterministic unmatched candidates, four-eyes manual resolutions, and
  independent provider-quorum verification before a manual financial mutation;
- optional advisory-only OpenAI-compatible candidate ranking with an exact
  hostname allowlist, DNS/IP pinning, and immutable suggestion audit rows;
- runnable API, settlement/callback/outbox/resolution/proof workers, and scanner
  processes with dependency-aware readiness probes.

The in-memory adapter is a deterministic local/test implementation. Production
uses the pinned [pgx v5](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)
adapter. The database must be migrated and provisioned with tenants, merchants,
revocable scoped API clients, enabled chains/assets, rate ticks, wallet/address
pools, and webhook endpoints before traffic is enabled.

## Run locally

Go 1.26 or newer is required.

```sh
go test ./...
BOOTSTRAP_API_KEY_ID=mk_test_local \
BOOTSTRAP_API_KEY_SECRET=local-secret-at-least-32-characters \
go run ./cmd/api
```

The development server listens on `:8080` by default. Health endpoints are
`GET /healthz` and `GET /readyz`. Override settings with:

- `APP_ENV=development|test|production`
- `HTTP_ADDRESS=:8080`
- `PUBLIC_BASE_URL=https://api.example.com`
- `DATABASE_URL=<secret reference or PostgreSQL URL>`
- `API_CREDENTIAL_ENVELOPE_KEY=<base64 16/24/32-byte AES key>` in production
- `SHUTDOWN_TIMEOUT=15s`
- `READ_HEADER_TIMEOUT=5s`
- `REQUEST_BODY_LIMIT=1048576`
- `TRON_DEPOSIT_ADDRESS` and `ETHEREUM_DEPOSIT_ADDRESS` for the local planner

Production startup rejects plaintext public URLs, a missing database URL, and
missing credential-envelope configuration. Production authentication performs a
PostgreSQL lookup on every request and enforces tenant, merchant, credential
version, scopes, validity window, and revocation. `BOOTSTRAP_*` credentials and
the static 1:1 planner exist only in development/test; production route creation
fails closed unless persisted rate ticks and address assignments are available.
Real secrets belong in Vault/KMS and must be injected through workload identity
rather than committed environment files.

## Request authentication

Every `/v1` request sends:

```text
Merchant-Key-Id: mk_live_...
Merchant-Timestamp: <Unix seconds>
Merchant-Nonce: <single-use 16..128 character value>
Content-Digest: sha-256=:<standard base64 SHA-256 of exact body>:
Merchant-Signature: <base64url HMAC-SHA256 without padding>
```

The signing input is:

```text
METHOD + "\n" +
CANONICAL_ESCAPED_PATH_AND_SORTED_QUERY + "\n" +
TIMESTAMP + "\n" +
NONCE + "\n" +
LOWERCASE_HEX_SHA256(EXACT_BODY)
```

Mutations also require `Idempotency-Key`. The stored fingerprint binds the HTTP
method, canonical escaped path and sorted query, and SHA-256 of the exact body.
Repeating the same operation, path, and key returns the original resource;
changing any bound request component returns `idempotency_conflict`.

## Runtime processes

`go run ./cmd/api` serves authenticated merchant, read, operator, and optional
sandbox/AI endpoints. Its `/readyz` checks PostgreSQL in production. Optional AI
ranking is enabled only when all of `AI_ENDPOINT`, `AI_MODEL`, `AI_API_KEY`, and
comma-separated exact `AI_ALLOWED_HOSTS` are set. Model output is restricted to
the stored candidate set, always has `review_required=true`, and cannot approve
or settle.

`go run ./cmd/management-api` is the only public owner of payment-link and
checkout capability routes, including `/v1/public/payment-links/*` and
`/v1/checkout-sessions/*`. It also hosts authenticated management and matching
policy APIs. It requires `DATABASE_URL`, `MANAGEMENT_PUBLIC_BASE_URL`,
`MANAGEMENT_TLS_CERT_FILE`, `MANAGEMENT_TLS_KEY_FILE`, and separate 32-byte key
files in `WEBHOOK_ENVELOPE_KEY_FILE`, `API_CREDENTIAL_ENVELOPE_KEY_FILE`,
`MANAGEMENT_RESPONSE_KEY_FILE`, and `MANAGEMENT_ADMIN_ASSERTION_KEY_FILE`.
TLS 1.3 is mandatory. The in-process public capability limiter is only a
per-instance safety bound; production ingress must enforce a cluster-wide rate
limit without trusting caller-supplied forwarding headers.

`go run ./cmd/worker` requires `DATABASE_URL`, `WORKER_ID`, and comma-separated
`WORKER_ROLES` chosen from `settlements`, `callbacks`, `outbox`, `resolutions`,
`proofs`, and `plans`. The `plans` role releases expired, unbound quote/address
leases left by a process crash. Relevant role configuration is:

- callbacks: `WEBHOOK_ENVELOPE_KEY`;
- outbox: explicit `OUTBOX_PUBLISHER=https|jetstream` and
  `OUTBOX_MAX_RETRY_DELAY`; HTTPS requires `OUTBOX_PUBLISH_URL` plus
  `OUTBOX_PUBLISH_TOKEN_FILE`, while JetStream uses only the file/TLS/stream
  settings enumerated in `deploy/runtime-contracts.json`;
- resolutions: `VERIFIER_CHAIN_ID`, `VERIFIER_PROVIDER_URLS`,
  `VERIFIER_QUORUM`, and optional `VERIFIER_PROVIDER_TOKEN`;
- proofs: `PROOF_VERIFIER_CHAIN_ID`, `PROOF_VERIFIER_PROVIDER_URLS`,
  `PROOF_VERIFIER_QUORUM`, and optional `PROOF_VERIFIER_PROVIDER_TOKEN`.

The outbox uses `event_id` as the stable downstream message/idempotency key and
emits one canonical JSON envelope. JetStream fixes the stream/subject and sets
`Nats-Msg-Id=event_id`; its ack must name that stream and a nonzero sequence.
A successful external acknowledgement is followed by a fenced database commit
that advances immutable event history; a crash between them safely redelivers.
PostgreSQL and merchant `/v1/events` remain the recovery source of truth.
The normalized verification gateways must expose `GET /v1/transfer` for manual
resolution and `GET /v1/transaction` for proof lookup.

`go run ./cmd/admin-api` is the separate operator BFF. It requires
`DATABASE_URL`, `ADMIN_PUBLIC_ORIGIN`, `ADMIN_OIDC_ISSUER`,
`ADMIN_OIDC_CLIENT_ID`, `ADMIN_OIDC_REDIRECT_URI`, `ADMIN_REQUIRED_ACR`,
`ADMIN_ACCEPTED_AMR`, and a 32-byte base64 `ADMIN_STATE_ENCRYPTION_KEY`;
confidential OIDC clients also set `ADMIN_OIDC_CLIENT_SECRET`. Session timing,
body limit, and listen address use the `ADMIN_*_TTL`,
`ADMIN_ROTATION_INTERVAL`, `ADMIN_BODY_LIMIT_BYTES`, and
`ADMIN_HTTP_ADDRESS` settings defined by `cmd/admin-api/config.go`. The BFF
also requires `MANAGEMENT_INTERNAL_URL`, `MANAGEMENT_INTERNAL_CA_FILE`,
`MANAGEMENT_ADMIN_ASSERTION_KEY_FILE`, `PLATFORM_ADMIN_INTERNAL_URL`,
`PLATFORM_ADMIN_CA_FILE`, `PLATFORM_ADMIN_ASSERTION_KEY_FILE`,
`PLATFORM_ADMIN_ASSERTION_ISSUER`, and `PLATFORM_ADMIN_ASSERTION_AUDIENCE`.
Browser requests never receive or supply internal assertions, actor IDs, grants,
MFA timestamps, or approval actors.

`go run ./cmd/financial-api` is an isolated TLS 1.3 treasury/refund/
reconciliation service. It requires `FINANCIAL_DATABASE_URL`,
`FINANCIAL_TLS_CERT_FILE`, `FINANCIAL_TLS_KEY_FILE`, and
`FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`; it binds to
`FINANCIAL_LISTEN_ADDR` (loopback by default). `go run ./cmd/financial-worker`
requires distinct HTTPS builder, signer, broadcaster, finality, and event-sink
services configured by `FINANCIAL_BUILDER_*`, `FINANCIAL_SIGNER_*`,
`FINANCIAL_BROADCASTER_*`, `FINANCIAL_FINALITY_*`, and
`FINANCIAL_EVENT_SINK_*` URL/token-file pairs. It also requires
`FINANCIAL_WORKER_ID` and an explicit comma-separated allowlist in
`FINANCIAL_WORKER_TENANT_IDS`; its health listener is restricted to loopback by
`FINANCIAL_WORKER_HEALTH_ADDR`. Refund destinations remain fail-closed until an
independent verifier admits them.

`go run ./cmd/scanner` requires `DATABASE_URL`, `WORKER_ID`,
`SCANNER_CHAIN_ID`, `SCANNER_GENESIS_HASH`, and a provider quorum in
`SCANNER_PROVIDER_URLS`. `SCANNER_PROVIDER_KIND=normalized-gateway` consumes the
normalized `/v1/head`, `/v1/range`, `/v1/transfer`, and `/v1/transaction`
contract. Direct read-only kinds are `evm-jsonrpc`, `tron-fullnode`,
`solana-jsonrpc`, `toncenter-v3`, and `aptos-fullnode`; configure their asset
allowlist with `SCANNER_ASSETS_JSON`, native asset fields, and chain-specific
GasFree/internal-transfer flags. One stable ID per URL can be supplied in
`SCANNER_PROVIDER_IDS`. `SCANNER_PROVIDER_HEADERS_JSON` accepts either one
common string-valued header object or an array containing one object per URL;
`SCANNER_PROVIDER_TOKEN` remains a convenience Bearer token when Authorization
is not supplied explicitly. Scanner health defaults to `:9091`; worker health
defaults to `:9090`. Both readiness probes require a recent successful work
cycle; tune the bound with `SCANNER_MAX_READY_AGE` or `WORKER_MAX_READY_AGE`.

Direct adapters are fail-closed and fixture-tested, but deployment admission
still requires live provider conformance. In particular, the current contiguous
cursor cannot represent skipped Solana slots, TON coverage depends on the tested
Toncenter v3 action contract, and Aptos contract-internal balance changes require
an independently verified indexer/resolver. These limitations must not be hidden
behind configuration claims.

## Merchant runtime completion and reconciliation exports

The Merchant-HMAC API supports explicit intent expiry, allowlisted
non-financial metadata replacement with an expected version, exact canonical
event retrieval, transaction-wide normalized transfer retrieval, and immutable
quote provenance. Event pull recovery uses ascending `after_sequence`, never a
descending UUID cursor. Callback producers allocate the merchant-local sequence
through one transactional tenant/merchant row, so concurrent API, settlement,
matching, resolution, and reorg writers cannot publish duplicates and rollback
does not burn a sequence.

Signed ledger reports require the dedicated `reconciliation:read` API-client
scope. `POST /v1/reconciliation-reports` captures a database-clock cutoff and
an actual ledger sequence fence; a future `period_end` is rejected. The worker
emits exact-string JSONL and enforces both entry and byte quotas. Ready objects
are SHA-256 digested and Ed25519 signed. The API selects the historical public
key by the report's exact `signing_key_id`, verifies the object once into a
private bounded spool, and then streams it with a route-specific deadline.

Production requires the same private, immutable S3-compatible bucket for API
reads and worker writes. The worker performs a conditional-write admission
probe and exits unless the provider proves `If-None-Match: *` with 409/412.
Directory storage is restricted to explicit non-production single-host use.
Configure:

- both processes: `RECONCILIATION_OBJECT_STORE=s3`, `RECONCILIATION_S3_ENDPOINT`,
  `RECONCILIATION_S3_REGION`, `RECONCILIATION_S3_BUCKET`, credential file paths,
  `RECONCILIATION_MAX_OBJECT_BYTES`, and an optional private spool directory;
- API only: `RECONCILIATION_SIGNING_PUBLIC_KEYS` as comma-separated
  `key_id=/mounted/public-key-file` entries and
  `RECONCILIATION_DOWNLOAD_TIMEOUT_SECONDS` (30..3600, default 900);
- worker only: `RECONCILIATION_SIGNING_PRIVATE_KEY_FILE`,
  `RECONCILIATION_SIGNING_KEY_ID`, `RECONCILIATION_WORKER_ID`,
  `RECONCILIATION_MAX_ENTRIES`, bounded poll/lease/batch/attempt/backoff values,
  and `RECONCILIATION_HEALTH_ADDRESS` (default `:9094`).

The reconciliation worker must use the isolated
`merchant_reconciliation_worker` BYPASSRLS role. `/healthz` performs a database
ping; `/readyz` also enforces recent polling and maximum queue age. The existing
plan worker expires overdue intents automatically, keeps exact reservations and
bound addresses through late-payment grace, and retires/releases them only
after grace. Historical report verification keys must remain mounted for the
full report retention period.

## Webhook verification

The `Merchant-Webhook-Signature` header has this form:

```text
t=<unix>,key=<key-id>,event=<event-id>,v1=<base64url-hmac>
```

The HMAC input is `<event-id>.<unix>.<exact HTTP body>`. Consumers must validate
the timestamp, verify the HMAC in constant time, store the event ID in a durable
inbox, and fulfill only `payment.settled`. HTTP retries retain the same event ID
and canonical body. A 2xx response is acknowledged only when its JSON body has
the exact matching `acknowledged_event_id`; empty or mismatched responses retry.

## Database migration

Apply migrations `000001` through `000008` in numeric order with a migration
role. `000004_management` adds payment links, checkout capabilities, signing-key
history, management credentials/audit, and durable four-eyes actions;
`000005_platform_admin` adds the admitted platform configuration plane;
`000006_automated_matching` adds immutable matching-policy changes;
`000007_rate_runtime` adds admitted rate collection; and
`000008_merchant_runtime_completion` adds deterministic ledger fences, complete
payment-intent history, signed report jobs, and the transactional callback
sequence allocator. Migration `000008` takes explicit table locks while it
backfills ledger and callback sequence state; quiesce application writers or
let the deployment migration gate drain them before applying it. Merchant
API roles must not own tables or bypass row-level security. At the start of
every tenant transaction set the local tenant context:

```sql
SELECT set_config('app.tenant_id', '<tenant UUID>', true);
```

Chain ingestion and callback workers require separate, narrowly granted roles.
The chain matcher is cross-tenant by recipient and therefore needs explicit
`BYPASSRLS`; those credentials must never be available to merchant HTTP handlers.

The migration enables `btree_gist`. Run forward migrations as a separate release
step; application startup must never execute destructive migrations.

## Release checks

```sh
gofmt -w $(find . -name '*.go')
go test ./...
go test -race ./...
go vet ./...
```

Integration gates additionally run the PostgreSQL migration up/down/up sequence,
OpenAPI validation, chain fixture replay, callback fault injection, and the
end-to-end invariant that a transfer can neither be lost nor credited twice.
