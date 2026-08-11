# Financial operations

This subsystem provides tenant-isolated treasury sweeps, verified refunds, and deterministic reconciliation. It is an internal operator API, not a merchant payment endpoint. All amounts are canonical uint256 atomic-unit strings; floating point is rejected.

## Safety model

- Every write runs in a PostgreSQL `SERIALIZABLE` transaction with forced tenant RLS.
- Idempotency keys are locked and bound to a SHA-256 request fingerprint. Reuse with different content is a conflict.
- Aggregate state, reservations, balanced double-entry ledger commands, hash-chained audit entry, and outbox event commit atomically.
- Source/nonce, daily-limit, and refundable-settlement reservations are locked before acceptance.
- Refunds require independent wallet/custodian/merchant evidence. An observed sender alone is never verification; origin-only is the safe default.
- Approval requires step-up authentication and an actor different from the creator.
- The operator API can request, read, approve, cancel, and execute evidence-only reconciliation. It has no build, sign, or broadcast route.

Finalized transfers are only refund evidence, never ownership verification. Refund capability remains fail-closed until a separately admitted wallet-signature/custodian verifier writes a non-revoked `financial_verified_refund_destinations` record; this repository intentionally contains no endpoint that accepts arbitrary merchant evidence. Origin-only still requires that independent verification and never promotes a CEX, smart-contract, GasFree, or hot-wallet sender automatically.

## Execution isolation

`financial-worker` advances one fenced stage at a time. Builder, signer, broadcaster, outgoing-finality verifier, and event sink must use five different HTTPS origins and five different credentials. Redirects and environment proxies are disabled. Requests carry a stable stage idempotency key and aggregate binding. The signer receives only approved digests and opaque references; the platform stores no chain private key.

Outgoing finality is obtained from the separately admitted verifier, not the signer or broadcaster. It can produce confirmed, finalized, failed, or reorged evidence. Refund finality/reorg transitions post immutable balancing/reversal entries. Treasury consolidation retains status and fee accounting without pretending an internal sweep changes merchant ownership.

## API and authentication

See `contracts/financial-openapi.yaml`. The IAM proxy signs tenant, actor, sorted permissions, bounded step-up expiry, request timestamp, nonce, path/query, and body digest. Nonces use `financial_proxy_nonces`, which is independent of merchant API clients. Reads require `financial:read`; writes require the domain permissions documented by OpenAPI. Unknown JSON fields, trailing JSON values, unknown query parameters, malformed UUIDs, and non-string amounts are rejected.

## Runtime configuration

`financial-api` requires `FINANCIAL_DATABASE_URL`, TLS certificate/key files, and `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`. The API process uses disabled custody ports by construction.

`financial-worker` requires database URL, worker ID, explicit tenant UUID list, and separate `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}` pairs. Health defaults to `127.0.0.1:9093`; `:9093` is supported for Kubernetes pod probes and must not be exposed by a public Service.

## Outbox and audit

The financial outbox uses `SKIP LOCKED`, monotonic lease tokens, bounded retry, dead-letter after 20 attempts, stable event IDs, and an acknowledgement that must echo the event ID. The sink receives the same ID as `Idempotency-Key`.

Audit records are append-only and chained per tenant with SHA-256 over the previous hash and canonical entry fields. Appends are serialized by an advisory lock and performed only through `append_financial_audit`. Production admission must grant its execution to the financial application role while denying direct audit DML. Export/anchor the latest tenant hash outside PostgreSQL so a database-owner attack is detectable.

## Deployment admission

Before enabling money movement: apply 000001, optional independent 000002, then 000003 up/down/up on a disposable PostgreSQL database; grant least-privilege table/function rights; verify RLS with two tenants; configure real KMS/HSM/MPC signer, independent builder/broadcaster, quorum finality provider, and idempotent event sink; exercise lost-response replay, stale-fence, reorg, dead-letter, backup/restore, and audit-chain verification. No software implementation can admit a signer/provider without operational key ceremony, allowlists, rate limits, monitoring, and recovery drills.

Local tests do not require sockets. Live PostgreSQL contract tests are skipped unless an explicitly isolated test database is supplied; static migration contracts still guard cross-tenant/cross-asset FKs, balanced ledger legs, nonce independence, RLS, and hash-chain structure.

## Admin financial cabinet

The browser talks only to the same-origin Admin BFF. The BFF derives a closed tenant-wide permission set from current database role bindings; merchant-scoped bindings and browser-supplied permissions are rejected. Treasury operators may request or cancel sweeps and refunds and request reconciliation. Senior approvers may independently approve sweeps or refunds and execute reconciliation. Support and payment operators receive no financial permission.

Every mutation requires CSRF, exact Origin, current version, reason and `Idempotency-Key`. Decision replay is stored atomically with the aggregate, audit and outbox transition; reusing a key with another method, path or body conflicts. Approval also requires recent MFA and a different actor. Atomic amounts remain strings throughout the UI and API.

The Admin BFF reaches the private financial API only over TLS 1.3 with a pinned CA, explicit server name and client certificate. The financial API requires and verifies that client certificate. Health monitoring uses a separate least-privilege client certificate; health endpoints are not downgraded to plaintext TLS. Redirects and environment proxies are disabled. The browser never receives the internal origin, assertion secret or custody metadata, and the BFF does not expose build, sign, broadcast or money-execution routes.

Live custody remains disabled and fail-closed. A rendered cabinet and a healthy financial API do not claim that any signer, broadcaster or custody provider has passed production key ceremony and admission.
