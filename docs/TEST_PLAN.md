# Independent verification plan

Status: executable QA scaffold for the greenfield merchant platform. It is
deliberately black-box where possible and does not import private backend
packages.

## 1. Quality invariants

The release gate must demonstrate all of the following:

1. An on-chain event can contribute to settlement at most once.
2. Retrying an identical API request returns the original resource.
3. Reusing an idempotency key with different immutable input fails closed.
4. Money is represented as strings or integer minor/raw units, never JSON
   floating-point numbers.
5. A repeated webhook has one business effect; the same event ID with a
   different body is a security conflict.
6. `observed` and `confirmed` do not grant the merchant's product;
   `payment.settled` is the normal fulfillment event.
7. A reorg creates a compensating state/event and never silently rewrites a
   settled record back to pending.
8. Unmatched, wrong-asset, late, and underpaid events remain reviewable and
   auditable. AI cannot settle them.
9. Tenant/environment identity, key version, timestamp, nonce, content digest,
   and signature are verified before parsing a webhook as trusted input.
10. Hosted checkout is keyboard-operable, localized in all six supported
    locales, and does not rely on color alone.

## 2. Test layers

### Pure contract and security tests

Run without a server:

```bash
python -m pip install -r tests/requirements.txt
pytest -q tests/unit tests/security tests/i18n
node --test tests/node/*.test.mjs
```

They cover canonical signatures, tampering, timestamp windows, exact amount
fixtures, allowed state transitions, localized documentation completeness, and
the reference webhook inbox's duplicate/conflict behavior.

### Backend black-box contract tests

Start the API, then run:

```bash
export MERCHANT_BASE_URL=http://127.0.0.1:8080
export MERCHANT_KEY_ID=test_key
export MERCHANT_SECRET=test_secret
pytest -q tests/contract
```

The suite validates health, create/get, exact amount representation, repeat
idempotency, idempotency conflict, route creation, cancellation, body and
digest tampering, nonce replay, stale timestamps, and duplicate JSON keys.
When the explicitly configured PostgreSQL sandbox API is enabled, executable
templates cover exact, partial, under-, over-, late, wrong-asset, duplicate and
out-of-order callbacks, timeout, dead-letter, reorg, and full reorg recovery.
Progressive actions separately prove observation, confirmations, finality, and
recovery; sandbox identifiers cannot cross into live payment endpoints. Real
provider and chain fixtures listed below remain independent release gates.

The production/canary and deterministic sandbox contracts always use distinct
targets. Set `SANDBOX_BASE_URL`, `SANDBOX_KEY_ID`, and `SANDBOX_SECRET` for the
sandbox suite; never reuse `MERCHANT_BASE_URL`, because production must return
404 for every sandbox route.

### Hosted-checkout accessibility and E2E

```bash
pnpm install
pnpm --filter @merchant/qa exec playwright install chromium
LANDING_E2E_URL=http://127.0.0.1:4173 \
ADMIN_E2E_URL=http://127.0.0.1:4174 \
UNMATCHED_E2E_URL=http://127.0.0.1:4174/#/unmatched \
CHECKOUT_E2E_URL=http://127.0.0.1:4175/checkout/fixture \
REQUIRE_E2E_TARGETS=1 \
pnpm --filter @merchant/qa test:e2e
```

The Playwright suite checks all locales, document language, accessible names,
keyboard focus, live payment status, exact copy values, copy controls,
responsive overflow, 200% zoom, reduced-motion emulation, automated axe
findings, and the single-operator manual-resolution path with independent
on-chain verification for cross-asset cases.

## 3. Required fixtures

Sandbox fixtures must be deterministic and return stable IDs for:

- exact native transfer;
- exact token transfer with event index;
- duplicate observation from two providers;
- two installments that reach the required total;
- underpayment below and above policy tolerance;
- overpayment;
- late payment;
- wrong asset on the correct network;
- internal EVM native transfer;
- TRON fee-deducted/GasFree transfer;
- Solana SPL and Token-2022 transfer;
- TON native and Jetton transfer;
- reorg before finality;
- deep reorg after settlement;
- RPC outage, cursor restart, and provider disagreement.

Every fixture includes raw amount, decimals, canonical event tuple, block time,
provider evidence digest, finality state, and expected domain events.

## 4. Concurrency and failure tests

Run against PostgreSQL, Redis/NATS, and at least two API/worker replicas:

- 50 concurrent identical creates produce one intent;
- conflicting bodies under one idempotency key produce one winner and conflicts;
- two scanners insert the same event concurrently;
- two matchers race for one route/event;
- a worker dies after ledger commit but before outbox publish;
- a delivery worker dies while holding a lease;
- callback returns timeout after committing its inbox;
- callback deliveries arrive duplicated and out of order;
- RPC switches providers mid-range and resumes from durable cursor;
- database failover happens during settlement.
- a JetStream publish commits but its acknowledgement is lost, then retry with
  the same `Nats-Msg-Id` is deduplicated before the fenced PostgreSQL mark;
- NATS outage/backpressure keeps PostgreSQL outbox rows pending without silent
  HTTPS fallback, and a wrong-stream or zero-sequence ack is rejected;
- PostgreSQL fails after a valid publish ack, then redelivery produces one
  broker event identity and no double business credit;
- the reference pull consumer loses its ack after atomic inbox/business commit,
  then redelivery is accepted as a duplicate and acknowledged.

Assertions use database uniqueness, ledger totals, domain event count, outbox
count, and consumer inbox count—not only HTTP responses.

## 5. Security tests

- Missing, unknown, expired, revoked, and wrong-environment key IDs.
- Bad signature, content-digest mismatch, path/query tampering, stale/future
  timestamp, replayed nonce, oversized body, duplicate JSON keys, invalid UTF-8.
- SSRF callback targets: loopback, link-local, RFC1918, redirect-to-private,
  DNS rebinding, non-HTTPS, disallowed port.
- Cross-tenant resource IDs and pagination cursors.
- Scope/RBAC/idempotency violations for resolution, plus four-eyes violations
  for refund, keys and asset pause.
- Log/audit redaction for secrets, auth headers, wallet material and private
  metadata.
- Webhook event ID collision with a different digest returns conflict and emits
  a security alert.

## 6. Accessibility and i18n gates

- Locale catalogs have the same key set for `en`, `zh-CN`, `es`, `fr`, `de`,
  and `ru`; no empty or source-key placeholder values.
- A source gate rejects user-facing JSX and accessibility labels that bypass
  the locale catalogs; protocol identifiers require an explicit exemption.
- Dates, decimal separators and plural forms use locale-aware formatting while
  copyable crypto amounts remain protocol-exact.
- `html[lang]`, heading hierarchy, landmarks, form labels, error association,
  focus visibility, 200% zoom, reduced motion and contrast meet WCAG 2.2 AA.
- Status is announced through an appropriate live region and is never conveyed
  only by color or animation.
- Address, amount and transaction hash remain selectable/copyable in RTL-safe
  isolated spans even though the initial six locales are LTR.

## 7. Performance, soak and retention

- API create/get p95 and p99 under the declared SLO at expected and 3x traffic.
- Scanner catches up after a 24-hour outage within the recovery objective.
- Seven-day soak with duplicate provider observations and callback retries.
- Partition creation, pruning, WORM export, restore and reconciliation reports.
- Webhook backlog and unmatched queue remain bounded under a failed endpoint.

## 8. Release evidence

CI publishes:

- Go and Python test reports;
- OpenAPI breaking-change report;
- signed webhook golden fixtures;
- Playwright traces/screenshots and axe report;
- migration up/down rehearsal result;
- dependency, secret and container scan reports;
- load/soak summary;
- backup restore and reconciliation diff;
- a matrix mapping every quality invariant to a passing automated test or an
  explicitly approved manual control.

No skipped P0 test is acceptable for production. Sandbox-only and external
chain-provider tests may be scheduled, but their most recent passing evidence
must be attached to the release.

## 9. Offline financial-invariant acceptance suite

The retained offline suite under `backend/internal/providers/testdata/acceptance`
parses synthetic responses through the real EVM, TRON, Solana, TON, and Aptos
adapters. It covers ERC-20 logs plus internal traces, TRC-20 GasFree siblings,
Solana native/SPL/Token-2022 inner instructions, TON native/Jetton pagination,
and Aptos coin/fungible-asset transfers. Fault cases include unordered logs,
pagination overlap, an interrupted response followed by an explicit retry,
provider disagreement, duplicate provider identities, and stale heads.

`TestFinancialInvariantExhaustiveBoundedFaultSchedules` loads the retained
schedule in `backend/testfixtures/financial_invariant/fault_schedules.json` and
deterministically evaluates 7,776 bounded schedules through the production
`TransferSettlementStore` application port and a small in-memory oracle. At
every step it requires at most one active match and a ledger net of either zero
or one credit. A committed settlement whose response is lost, explicit retry,
duplicate observation, outbox delivery, callback delivery, reorg reversal, and
same-identity/new-block re-inclusion must finish with exactly one active credit.
A different event index is explicitly a different transfer, not a re-inclusion.

The GasFree regression uses two-decimal atomic amounts: a merchant transfer of
639 (6.39) and a fee sibling of 150 (1.50). Merchant received, treasury received,
and credited values must remain 639, never 789; the fee allocation has zero
effective and credited amounts and therefore cannot create a merchant ledger
leg. The PostgreSQL refund bridge is separately pinned to
`allocation_role='payment'`, excluding the `gasfree_fee` evidence allocation
from refundable settlements. Bounded fuzz targets exercise address
normalization/identity stability and order-independent exact aggregation.

Static PostgreSQL contract tests connect the offline oracle to the migration and
store source by requiring the canonical-transfer, active-match, aggregate,
ledger-business-reference, callback, outbox, history, and consumer-inbox
uniqueness clauses; the reorg reversal link and inverted ledger entries; and
lease-token fencing. These are source checks, not a claim that PostgreSQL
transactions or external delivery behavior were exercised.

Run the offline slice from `backend`:

```sh
go test ./internal/providers ./internal/scanner ./internal/application ./internal/adapters/postgres
go vet ./internal/providers ./internal/scanner ./internal/application ./internal/adapters/postgres
go test -race ./internal/providers ./internal/scanner ./internal/application ./internal/adapters/postgres
go test ./internal/providers -run '^$' -fuzz '^FuzzCanonicalIdentityNormalization$' -fuzztime=5s
go test ./internal/application -run '^$' -fuzz '^FuzzEvaluateAutomatedMatchAggregation$' -fuzztime=5s
```

Production admission still requires live PostgreSQL serializable/failover
testing, independent provider replay, deployed outbox destination and callback
fault injection, and reconciliation of actual ledger rows. Offline success must
not be promoted as evidence for those live gates.
