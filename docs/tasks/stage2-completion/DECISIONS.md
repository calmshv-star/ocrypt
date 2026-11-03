# stage2-completion decisions

Goal: Finish the stage-2 cabinet and checkout items that need no owner input: a plain sign-in failure page, network fee hints and a locked-amount timer on checkout, and daily totals for accounting with XLSX export

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — agent decisions

- **Sign-in failure reasons are six fixed words.** Nothing from the provider
  or the error reaches the page; only known OAuth/OIDC error codes are logged.
  The correlation cookie is cleared only by a callback that matched it, so a
  stale callback from another tab cannot end a sign-in in progress. A database
  outage while resolving the identity reads as `failed`, not `not_allowed`.
- **Fee tiers are qualitative** (low/medium/high per network, native TRX low),
  because fee amounts move with the market and the payer only needs the
  cheaper choice.
- **The payment day is the transfer day.** Accounting counts a payment when
  the money arrived: the last finalized transfer's on-chain time. Orders
  settled without a transfer (migrated legacy orders, some manual
  resolutions) fall back to `settled_at`. The page says past days can change
  after a reorganization or a manual review, that tolerated underpayments and
  overpayments count at the invoice amount, and that refunds are not netted.
- **XLSX without a dependency** (no new supply-chain surface), with one cell
  style per count of decimal places so 57.00 stays 57.00, and text kept
  literal (`_xHHHH_` escaped, never a formula).

## 2026-11-03 — independent review of 9d696ab..7e35206

No critical or high finding. Fixed:

- **M1** "Amount locked" was not true when a route expires before the intent
  (the payment-intents API lets them differ). Routes now carry `expires_at`;
  the timer counts to the earlier deadline, says "locked" only with a route
  deadline and hides when final.
- **M2** The report bucketed by processing time (`settled_at`), which put
  migrated orders on the migration day and moved reorged payments. It now
  uses the last finalized transfer's on-chain time; the fallback and the
  caveats are on the page.
- **M3** No index matched the range: `payment_intents_paid_day_idx
  (tenant_id, settled_at) INCLUDE (…) WHERE status IN ('settled','overpaid')`
  (migration 000060); the bounds are inlined so `settled_at` is an index
  condition (checked with EXPLAIN).
- **Lows:** cookie clearing (L1); provider error logging (L2); years before
  2000 refused (L3); report period checks, today per zone, an invalid browser
  zone, and zone/scope/generation time in exports (L4); significant digits
  without trailing zeros (L5); `_xHHHH_`, CR, apostrophe and "History" sheet
  names (L6); `styles.xml` with number formats (L7); native TRX tier (L8);
  "cancelled" copy and outage mapping (L9); report caveats (L10); live test
  for a DST day, a stale `settled_at` on `reorg_review` and a merchant-only
  principal, and these records (L11).
- **Not changed:** retrying the original flow (step-up, invitation) from the
  failure page — the page offers a fresh sign-in and, with a session, the way
  back; the per-request `pg_timezone_names` lookup (15–40 ms) is kept because
  it turns an unknown zone into a 400 instead of a 500.
- **Not verified here:** opening the XLSX in Excel or LibreOffice (neither is
  installed); Python `zipfile` and an XML parse of every part pass.
