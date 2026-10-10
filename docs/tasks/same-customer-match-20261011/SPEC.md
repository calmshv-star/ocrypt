# same-customer-match-20261011 specification

## Requested outcome

Settle small overpayments against equivalent repeated same-customer orders exactly once, fulfill the approved Showy payment, and publish the verified repair

## Interview and scope

The user explicitly approved manual fulfillment of the independently observed
GRAM payment and requested a permanent repair published as a new GitHub version.
The production failure is a finalized, in-window transfer slightly above two
overlapping invoices created by the same authenticated merchant for the same
customer and fiat amount. Both candidates score 100; the current generic selector
rejects the tie before deterministic matching can run.

Scope: add a narrowly bounded same-customer tie resolution for small in-window
overpayments. Every tied eligible candidate must have the same non-empty
merchant-authenticated customer reference, tenant, merchant, fiat amount,
currency/scale and canonical merchant-provided metadata. The selected route must
be strictly closer in exact atomic amount than every other tied candidate. Load
and validate authoritative context inside the transaction; verify ownership again
at automated settlement time. Preserve event-owner isolation, primary-window
checks, policy snapshots, finality, exact-money ledger and one-transfer-one-credit.

No new product questions are required for this observed case. Preserve inclusive
score 75, existing five-percent bounds, enabled assets, rate/scanner intervals,
quorum, fee accounting, prices, subscription durations and service configuration.
Do not match by sender/recipient address as customer identity, break equal-distance
ties by recency, auto-resolve unrelated users or import old stacked PRs wholesale.
The authorized individual payment must use the ordinary manual-resolution verifier
and signed callback/Showy activation path, not a fabricated proof or direct grant.
Public task documents use synthetic fixtures, never customer records or credentials.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| M1 | Two equivalent authenticated same-customer in-window small-overpayment candidates tie at score 100 | Strictly closest route selected deterministically; only it receives the event | New application selection regression, red before code | PASS; `7570a6a`, `12bacca`, PLAN evidence |
| M2 | Different/empty customer, tenant, merchant, fiat terms, metadata, exact-distance tie, large excess, late, low score or unsupported classification | Remains fail-closed; unique-score behavior and inclusive 75 unchanged | Table-driven negative and existing candidate tests | PASS; exact JSON/money negatives and saturation veto |
| M3 | Owner selection used during ingestion and later automated reconciliation, including more than two tied routes | Authoritative context is locked/revalidated; neighbouring route cannot aggregate the event | PostgreSQL owner/reconciliation integration regression | PASS; five persisted cases including cancelled/changed contender |
| M4 | Finalized transfer replay, competing reconciliations and settlement failure | Exactly one persisted match, balanced ledger transaction, intent transition and callback/outbox; rollback is atomic | Disposable PostgreSQL regression and race/financial checks | PASS; full eleven-case persisted harness, financial/race/fuzz |
| F1 | User-approved existing payment | One core verified settlement and one activated Showy month; replay/duplicate checks and active expiry verified | Guarded manual-resolution request and read-only postconditions | PASS; independent verifier and signed ordinary callback, private evidence |
| P1 | GitHub publication | Signed focused commits, current-head required CI, published/merged fix without bypassing gates | GitHub PR/check/merge evidence | Pending |
| D1 | Runtime activation of the permanent repair | Affected service revision/digest verified through established bounded rollout; no configuration drift | Deployment/readiness evidence, or explicit unverified limit | Pending |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Go version from backend/go.mod; Python 3.11+ for workflow checks; a disposable
PostgreSQL environment for persisted financial tests. Production is only for the
explicitly approved individual settlement and bounded real-service rollout, never
for synthetic tests. Required release checks are separate from local unit evidence.
