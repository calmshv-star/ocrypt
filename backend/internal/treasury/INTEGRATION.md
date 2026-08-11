# Treasury, refunds, and reconciliation integration checklist

The packages in `internal/treasury`, `internal/refunds`, and
`internal/reconciliation` are domain/application code. They compile and are
tested, but they are not wired to HTTP, PostgreSQL, chain adapters, or a signer
runtime yet. Custodial capabilities must remain disabled until every mandatory
item below is complete.

## Database migrations

- [ ] Add versioned, tenant-scoped `sweep_policies` and `refund_policies`.
  Store every amount as `NUMERIC(78,0)` with non-negative checks; never use a
  floating type. Store policy snapshots on each request.
- [ ] Add `sweep_requests`, `sweep_items`, `sweep_approvals`,
  `refund_requests`, and `refund_approvals`, including explicit status checks,
  version columns, opaque unsigned/signed references, digests, transaction
  hashes, and evidence digests. Do not store private keys or seed phrases.
- [ ] Add tenant-qualified unique indexes for `(tenant_id, idempotency_key)`
  and retain `request_hash`; a different hash for the same key is a conflict.
- [ ] Add destination-verification evidence with expiry/revocation and a
  binding to tenant, settlement, asset, chain, and evidence digest. An observed
  chain sender is not a verification method.
- [ ] Add transactional daily-usage buckets and settlement-refundable locks.
  `Repository.Create` must lock these rows before checking limits so concurrent
  idempotency keys cannot bypass a daily limit or double-refund a settlement.
- [ ] Add a tenant/chain/source/nonce reservation constraint for sweep items.
  Release only requests cancelled before signing; retain consumed nonce records
  for signed or broadcast requests and reconcile ambiguous outcomes.
- [ ] Implement repository mutations as one PostgreSQL transaction containing
  aggregate state, immutable double-entry ledger commands, append-only audit,
  and outbox events. Reject cross-tenant or cross-asset account references.
- [ ] Add `reconciliation_runs`, `reconciliation_items`, and
  `reconciliation_integrity_items`, including cutoff block/time, evidence
  digest, deterministic report digest, severity, owner, and resolution fields.
- [ ] Apply RLS and tenant-qualified foreign keys to every table. Add immutable
  triggers/permissions for approvals, ledger postings, audit, and outbox rows.
- [ ] Add migration up/down/up tests and concurrent serializable-transaction
  tests against the supported PostgreSQL version.

## Adapter implementation

- [ ] Implement all repository interfaces without weakening their atomicity
  contract. Map version conflicts and idempotency hash conflicts to the package
  sentinel errors.
- [ ] Implement the versioned policy repositories and emergency-pause path.
- [ ] Implement a watch-only snapshot provider that reads on-chain balances at
  an evidenced cutoff, ledger balances, pending sweeps/refunds/inbound funds,
  callback deadlines, matches, reversals, and provider coverage gaps.
- [ ] Implement deterministic chain-specific unsigned builders. Revalidate
  chain, asset, destination, amount, nonce, and fee immediately before signing.
- [ ] Connect `Signer` only to an isolated HSM/KMS/MPC or external custodian.
  Exchange only typed requests, digests, and opaque transaction references.
  The API/worker environment must have no signing secret.
- [ ] Make broadcaster calls idempotent by signed digest, handle ambiguous
  timeout outcomes by querying the chain, and independently bind returned
  transaction hashes to the signed digest.

## API and event contracts

- [ ] Add management-only sweep request/get/approve/cancel endpoints with
  treasury RBAC and short-lived step-up MFA. Never expose them to runtime API
  keys.
- [ ] Add the custodial-only refund endpoints from the product contract:
  create, get, approve, and cancel. Return `refunds=false` when no approved
  signer deployment exists; do not simulate support in watch-only mode.
- [ ] Generate OpenAPI schemas from the exported JSON command/result models.
  Exact amounts remain decimal strings; actor/workload authentication contexts
  are injected by middleware and are never accepted from request JSON.
- [ ] Map optimistic version conflicts to `409`, policy/destination failures to
  stable typed errors, and idempotent replay to the original result.
- [ ] Publish canonical versioned events for requested, approval-required,
  approved, signed, broadcast, confirmed, finalized, failed, cancelled, and
  reorged states. Sweep events require a management/treasury subscription.
- [ ] Expose reconciliation report create/get/download endpoints. Sign exports
  and use immutable object storage with a short-lived download URL.

## Runtime wiring and operations

- [ ] Run treasury coordination, signer, broadcaster, finality observation, and
  reconciliation as separately permissioned workloads. Scanner/API remain
  watch-only.
- [ ] Enforce one active executor with fenced leases. Replays use aggregate
  version plus signed digest; no job may be acknowledged before state/outbox
  commit.
- [ ] Configure signer destination allowlists, asset/chain/key-purpose limits,
  nonce controls, fee caps, per-request and rolling limits, emergency pause,
  and separate sweep/refund keys.
- [ ] Alert on approval age, signing age, broadcast/finality age, failed or
  reorged transfers, reconciliation shortfall/surplus, missing ledger legs,
  duplicate matches, stale callbacks, and provider gaps.
- [ ] Add backup/restore, signer-unavailable, ambiguous-broadcast, reorg,
  provider-gap, and reconciliation-delta runbooks. Prove restore and custody
  pause behavior in a staging environment.
- [ ] Run integration, contract, race, fuzz, load, fault-injection, and external
  custody-security tests before enabling `sweeps` or `refunds` capabilities.

Until this checklist is complete, these packages provide tested business rules
and ports only, not end-to-end treasury or refund support.
