# Financial settings mutation safety

## Current boundary

`GET /admin/v1/financial-settings` is deliberately read-only. It reports the
merchant settlement currency, admitted legacy routes, asset and chain status,
and aggregate wallet capacity. It never returns receiving addresses, secret
references, private keys, or signing material.

Migration `000041` defines usable capacity consistently with route allocation:
an active watch-only address remains usable after its first assignment because
concurrent payments are separated by exact-amount reservations. The admin UI
therefore reports `usable_address_count`, not a misleading physical
`available_address_count`.

No financial setting is changed by this read model or by its UI.

## Why existing generic configuration workflow is insufficient

The platform configuration service already provides valuable workflow
primitives: immutable snapshots, request and distinct-person approval, recent
step-up authentication, scheduled activation with fence tokens, audit/outbox,
and rollback-as-a-new-version. Those primitives can be reused only after a
typed merchant financial domain is added. Using them directly today would show
controls which do not safely change runtime behaviour.

The missing contracts are:

1. **Merchant ownership.** Generic platform scope is tenant-wide. It does not
   bind a logical key to an existing merchant or prove that a wallet belongs to
   the selected merchant.
2. **Typed semantic validation.** Generic JSON validation does not verify that
   the merchant, chain, asset, wallet, and address exist, belong together, are
   active, and are scanner-ready.
3. **One runtime policy.** Legacy gateway admission is driven by immutable
   `legacy_compat_configs`, while the core order path resolves global assets.
   There is no single merchant route/currency policy consumed fail-closed by
   both entry points.
4. **Fiat allow-list enforcement.** The core order API validates a three-letter
   currency and rate availability, not a merchant-configured fiat allow-list.
5. **Wallet/address actuator.** A `wallet_pool` snapshot is only a reference.
   Activation does not create a watch-only `wallets`/`addresses` row, verify
   address ownership, or wait for scanner readiness.
6. **Shared canonicalisation.** EVM, TRON, TON and Solana address checks live in
   provider-specific internals. There is no exported, chain-aware canonical
   address service for an administrative mutation.
7. **Scanner readiness.** Scanner watches are derived from active or
   grace-period payment routes. Adding an address row alone does not establish
   durable watch coverage or prove a backfill/catch-up boundary.
8. **Safe disable semantics.** Disabling a route must reject new orders but
   keep observation and settlement for open and grace-period routes. A rollback
   must not delete evidence or stop a watcher prematurely.
9. **Legacy mutation parity.** Legacy admission can add immutable configuration
   through separate database requester/approver roles, but has no browser/admin
   service for disable and rollback. The migration-only watch-address import is
   fenced to importing runs and is not a merchant settings API.

## Smallest safe implementation increment

The first mutation release should be backend-only until the following contract
is complete and tested:

- Add merchant-scoped permissions:
  `financial_settings:read`, `:write`, `:request`, `:approve`, `:activate`, and
  `:rollback`. Require recent step-up for every state transition and a distinct
  approver for approval.
- Add typed change requests for `fiat_currency`, `payment_route`, and
  `watch_only_wallet`. Store exact tenant and merchant IDs, resource IDs,
  expected head version/fence, payload hash, idempotency key, reason, requester,
  approver, activation result, and immutable audit evidence.
- Reuse the platform snapshot/approval engine only after it accepts an enforced
  merchant scope and invokes kind-specific validators and actuators in the same
  transaction as head activation.
- Add one merchant financial policy read by both legacy and core order creation
  and planning. Currency and route checks must fail closed when no active policy
  exists.
- For a watch-only wallet, accept only a public receiving address and an
  explicit chain. Reject all private keys, seed phrases, signer credentials,
  secret references, and unknown payload fields. Canonicalise by chain, enforce
  uniqueness, prove operator ownership/control through a chain-appropriate
  challenge, create no signer binding, establish watcher/backfill coverage, and
  activate only after readiness succeeds.
- Disable in two phases: immediately stop new route allocation, then retain the
  watcher until every existing route has settled or passed its configured grace
  boundary. Rollback restores allocation without rewriting payment history.
- Expose UI controls only after the API returns readiness, approval state,
  activation fence/version, and rollback eligibility. The UI must not claim a
  change is active before the actuator confirms it.

Until this contract exists, the safe product is the permission-aware read-only
inventory now implemented. No production database, wallet, route, currency, or
Showy configuration was mutated while producing this assessment.
