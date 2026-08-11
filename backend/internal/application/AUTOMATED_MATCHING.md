# Deterministic automated matching

Automated matching is opt-in per merchant and fail closed. A merchant operator
creates an immutable row in `automated_matching_policies`; the route creation
transaction binds the newest already-effective policy to that route. A policy
created later never changes an address or amount already shown to a payer.
Routes without a binding continue through the unmatched-payment workflow.

The `matching` worker aggregates only canonical, finalized integer transfer
facts for the route's exact chain, asset, address and time window. It never
performs cross-asset conversion and never consumes an AI suggestion. The first
transfer binds the sender when `require_same_sender` is enabled. Concurrent
workers are fenced by a database lease token, PostgreSQL serializable
transactions, an active aggregate uniqueness index and the existing unique
active-match-per-event index.

Policy fields cover partial accumulation, a basis-point underpayment tolerance,
late payments within the route's pre-existing grace window, and one of three
overpayment actions: manual review, credit all, or credit the expected amount
while holding the excess in a separate liability account. Defaults are
non-permissive.

TRON GasFree is accepted only when the normalized payment and fee events have
the same transaction, sender, asset, block, timestamp, parser version, raw
evidence digest and paired transfer index. The fee recipient must be in the
policy snapshot's trusted collector list. The sibling is verification evidence
only: it does not increase the invoice's received or credited amount and does
not create a merchant ledger leg. The ledger debits only the actual transfer to
the merchant, and the fee sibling is explicitly excluded from the refund bridge.

Every evaluation writes canonical decision evidence and its SHA-256 digest.
Settlement matches, balanced ledger entries, intent/route state, callbacks,
deliveries and the outbox event commit atomically. A canonical reorg appends an
opposite ledger transaction, reverses the aggregate and all allocations, moves
the intent to review, emits `payment.reorged`, and schedules the remaining
canonical contributions for a new aggregate. Historical ledger and decision
rows are never deleted or rewritten.

Run the generic worker with `matching` in `WORKER_ROLES`; it is normally paired
with `settlements`, because the settlement role ingests and enqueues finalized
non-exact transfers. Provisioning must apply migration
`000006_automated_matching` before enabling the role.

Recommended deployment value is
`WORKER_ROLES=settlements,matching,callbacks,outbox`; the matching role adds no
secret or network environment variables. Its database identity must be the
same narrowly granted cross-tenant financial worker identity used by
settlements (including the deliberate RLS bypass), never the API/BFF role.
