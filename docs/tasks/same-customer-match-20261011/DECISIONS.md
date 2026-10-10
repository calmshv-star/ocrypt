# same-customer-match-20261011 decisions

Goal: Settle small overpayments against equivalent repeated same-customer orders exactly once, fulfill the approved Showy payment, and publish the verified repair

2026-10-11: user explicitly approved fulfillment and requested the permanent fix
and GitHub publication. No additional pricing/tolerance/interval changes authorized.

The narrow eligibility choice is an implementation inference from the verified
incident and the outstanding same-customer PR audit: authenticated identical
customer/economics/context plus strictly closest small overpayment, with locked
revalidation. Preserve all current-main ownership protections; do not merge the
older stacked PR wholesale. Shared deposit/sender addresses are not identity.

One production operator (the orchestrator) handles the approved resolution and
rollout. Delegated agents may run offline/disposable tests only; never production
operations or shared plan edits. Model/role settings follow project profiles.

Use a bounded 101st-route sentinel rather than unbounded shared-address locking.
Retain the original first-100 candidate behavior; persist truncation evidence so
contextual ties fail closed at both boundaries. Canonical JSON uses exact numbers;
different numeric spellings conservatively remain distinct.

Only the newly resolved contextual path skips the misleading pre-settlement
needs-review callback. Existing unique selection behavior remains unchanged.
The PostgreSQL fixture uses actual separate settlement/matching roles and an
injected deterministic clock, preserving assertions and payment windows.

Independent review exposed the cancelled-contender bypass. Preserve explicit
rejected ownership separately from absent ownership, then veto before reduction.
Database regression proved RED before repair and GREEN after it.

Deploy the shared code to settlement/matching and all eleven existing proof
workers; do not deploy unrelated worker roles. Retain stopped originals with
restart disabled, record their original policies in the protected journal and
restore policies on rollback. Only image/release metadata changes on active
containers; no migrations or product-configuration edits are required.
