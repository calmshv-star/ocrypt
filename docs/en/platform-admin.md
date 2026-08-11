# Platform configuration administration

The Platform Admin API is an internal configuration control plane. It manages tenant and project environments, chains, asset contracts, watch-only wallet/address pools, RPC capabilities, rate sources and policies, finality and matching policies, quotas, notification channel references, feature flags, and maintenance windows. It never stores private keys, mnemonics, API secrets, signing keys, or provider credentials.

## Trust boundary

The API listens on internal TLS only. A browser must never call it and must never receive its assertion key. The existing same-origin admin BFF is the trusted issuer. It authenticates the server-side admin session, checks Origin and CSRF for mutations, evaluates the database-backed role binding for the requested tenant, and then mints a single-use assertion bound to HTTP method, escaped path, canonical query, exact body hash, issuer, audience, expiry, and nonce. Duplicate Authorization headers, duplicate JSON keys, non-canonical queries, replays, and unbound requests are rejected.

The intended BFF surface is `/admin/v1/platform/changes`, `/admin/v1/platform/changes/{id}/{action}`, `/admin/v1/platform/snapshots`, and `/admin/v1/platform/emergency-pauses`. It proxies to `/internal/platform-admin/v1/*`. This repository provides `platformadmin.AssertionIssuer`; the BFF routes are an explicit integration step and are not silently added here. React continues to use the existing session and CSRF cookie only.

Permissions come only from active `admin_role_bindings`: `platform_config:read`, `write`, `request`, `approve`, `schedule`, `activate`, `rollback`, and `emergency`. An auditor is read-only. A security administrator drafts, requests, and performs emergency operations. A senior approver independently approves, schedules, activates, and drafts rollbacks. A tenant-scoped binding cannot authorize another tenant; a global binding is explicit.

## Immutable workflow

1. A writer creates a new draft with `based_on_version`, a reason, and an idempotency key.
2. The requester submits the draft for approval using the current `row_version`.
3. A different MFA-stepped-up actor approves or rejects it. Self-approval is forbidden.
4. An approved version is scheduled for a future instant.
5. The admitted scheduler workload leases due rows with `SKIP LOCKED`, a bounded lease, and a monotonically increasing claim token. Manual activation uses the same row/head fences.
6. Activation appends an immutable snapshot and activation record, then atomically advances a head pointer and fence token. The prior version is derived as superseded; its payload is unchanged.
7. Rollback copies a historic snapshot into a new draft/version and repeats approval. It never rewrites the old snapshot.

Emergency pause/resume is a separate append-only operational event stream with MFA, reason, idempotency, and audit/outbox entries. It does not pretend to be a configuration edit.

Every mutation binds its idempotency fingerprint to method, path, canonical query, and exact body. Advisory transaction locks serialize first use. Stale row versions, head fences, leases, and claim tokens fail with conflict. Tenant RLS is enabled and forced. Composite foreign keys bind snapshots and heads to the same scope, kind, and logical key. The audit log is a single tamper-evident hash chain and restores the caller's RLS settings after its narrowly owned definer function returns.

## Validation

Payloads are JSON objects up to 64 KiB. Money-like and quota values use unsigned base-10 strings, never JSON numbers. Asset contract syntax is checked per family (EVM, Tron/Solana, TON, Aptos, or `native`). Rate policies require at least two sources and a valid quorum. RPC/rate endpoints require HTTPS without inline credentials or query secrets. Wallet pools accept only a watch-only or external-custodian reference. Maintenance windows are bounded and ordered. Go validation and database constraints both reject inline secrets and inconsistent payload hashes.

## Runtime consumption and deployment status

`ActiveSnapshotReader` remains the point-read contract used by the rate worker. Scanner uses `RuntimeStateReader.ActiveRuntimeState`: all requested heads and their latest emergency-pause states are read in one serializable transaction. Every item carries snapshot ID, immutable payload hash, version, and fence token. Missing, duplicate, malformed, unfenced, paused, or cross-referenced resources fail the cycle closed.

The production scanner now reloads `chain`, `finality_policy`, every configured `rpc_provider`, every admitted `asset_contract`, and relevant `maintenance_window` heads before each cycle. It requires active resources, provider quorum, `overlap >= reorg_depth`, required RPC capabilities, and exact chain/asset references. Configured confirmations are subtracted from the quorum head. Each committed range appends immutable `scanner_runtime_config_evidence`; rollback is observed on the next cycle as a new snapshot/fence. Provider credentials are never in snapshots: `credential_ref` resolves a bounded JSON header file below `SCANNER_SECRET_DIR`. Production requires strict `SCANNER_PLATFORM_RUNTIME_JSON`, for example `{"chain":"eip155:1","finality":"eip155:1","rpc_providers":["rpc/one","rpc/two"],"assets":["eth-ethereum","usdt-ethereum"],"maintenance":["eip155:1"]}`; static scanner settings are available only with `ENVIRONMENT=development|test` plus `SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG=true`.

Production route allocation calls database-owned admission functions inside the same serializable transaction as rate-tick selection and address leasing. Naming is deliberate: tenant `merchant_environment` key = merchant UUID, global `chain`/`finality_policy` key = chain ID, global `asset_contract` key = asset ID, and tenant `wallet_pool` key = wallet UUID. All must be active and current. A tenant `feature_flag` head named `new_routes` is also mandatory: its payload key must match, it must be enabled, and rollout must be 10000 bps. Missing/disabled flags, global/tenant emergency pauses, or an active `read_only`/`disable_new_routes` maintenance window reject allocation. The quote records environment/chain/asset/finality snapshot IDs and fences plus required finality; the address lease records wallet-pool evidence. Operational asset decimals must equal the activated contract snapshot. Existing rows without this provenance cannot be replayed as a new plan.

Rate collection already consumes fenced `rate_policy` and `rate_source` snapshots and persists their evidence. `matching_policy` in this control plane is intentionally **not** allowed to widen settlement tolerances: automated matching continues to use its separate four-eyes policy workflow. Quota snapshots, notification-channel references, and financial sweep/refund finality transitions are not yet runtime consumers here; notifications still require an external secret owner and refunds remain fail-closed without independent destination verification. Do not treat those kinds as runtime-effective merely because they were activated.

`platform-outbox-publisher` is the concrete private publisher. Its database claimant requires an enabled `outbox_publisher` service identity and monotonic claim token. Delivery is HTTPS with TLS 1.3, mTLS, a bearer token read from a file, redirects/proxies disabled, and stable `Idempotency-Key`/`X-Event-ID`. The destination is exactly `/v1/platform-admin/events` and must acknowledge both `event_id` and `claim_token`; only then is the leased row marked published. Configure `PLATFORM_OUTBOX_DATABASE_URL`, `PLATFORM_OUTBOX_WORKER_ID`, destination URL/CA/client certificate/client key/token files, and a loopback health address. Live PostgreSQL, chain-provider, and destination interoperability remain deployment admission checks; they were not exercised by package tests.

## Endpoint summary

| Operation | Internal endpoint | Required permission |
| --- | --- | --- |
| List/get changes and snapshots | `GET .../changes`, `GET .../changes/{id}`, `GET .../snapshots` | `platform_config:read` |
| Create draft | `POST .../changes` | `platform_config:write` |
| Request approval | `POST .../changes/{id}/request-approval` | `platform_config:request` + recent MFA |
| Approve/reject | `POST .../changes/{id}/approve` or `reject` | `platform_config:approve` + recent MFA + different actor |
| Schedule | `POST .../changes/{id}/schedule` | `platform_config:schedule` + recent MFA |
| Manual due activation | `POST .../changes/{id}/activate` | `platform_config:activate` + recent MFA |
| Draft rollback | `POST .../rollbacks` | `platform_config:rollback` + recent MFA |
| Emergency pause/resume | `POST .../emergency-pauses` | `platform_config:emergency` + recent MFA |

There are no `PUT`, `PATCH`, or direct “edit current row” endpoints.
