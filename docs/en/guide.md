# Universal Crypto Merchant Platform

This guide is the concise product, developer, and operations view of a
standalone, general-purpose cryptocurrency payment platform.

## Product guide

### What the platform does

The platform is a multi-tenant payment infrastructure product for websites,
apps, bots, SaaS products, and internal systems. A merchant creates a payment
intent; the platform fixes a quote, issues one or more network routes, observes
the chain, records immutable transfer facts, matches them, commits a settlement
ledger entry, and delivers a signed event.

The merchant remains responsible for its order, customer, inventory, access,
subscription, or balance. The platform never grants the merchant's product.

### Complete scope

- Server API, hosted checkout, embeddable checkout, and payment links.
- Native and token transfers across EVM, TRON, Solana, TON, and approved Move
  adapters.
- Exact, partial, under-, over-, late, fee-deducted, wrong-asset, and internal
  smart-contract transfers.
- Durable cursors, multiple RPC providers, finality policies, and reorg handling.
- An accounting ledger, callback outbox, event history, and reconciliation.
- Merchant cabinet, operator unmatched queue, platform administration, and a
  deterministic sandbox.
- Optional custody workflows through an isolated treasury signer. A watch-only
  deployment honestly reports that refunds and sweeps are unavailable.

### Non-negotiable behavior

- One canonical chain event is never credited twice.
- Money never enters financial logic as a binary float.
- Block time, not scanner discovery time, determines whether a payment is late.
- AI may rank deterministic candidates; it cannot invent a customer or settle.
- Manual decisions record actor, reason, object version, evidence, and separate
  risk approvals.
- A reorg creates a compensating workflow; settled history is not edited away.
- Wallet private keys never live in API, checkout, scanners, or admin services.

### User journey

1. The merchant backend creates an intent with its order reference and exact
   minor-unit amount.
2. A route fixes asset, network, raw amount, address/memo, quote provenance, and
   expiry.
3. Checkout clearly shows the network and contract/mint, exact net amount, QR,
   countdown, copy actions, and wrong-network warning.
4. The platform observes and confirms the transfer. Checkout may show progress,
   but the merchant does not fulfill yet.
5. Finality and the ledger commit produce `payment.settled`.
6. The merchant's transactional webhook inbox applies the event once and emits
   its own fulfillment outbox.

Late, wrong-asset, ambiguous, and policy-breaking transfers enter review rather
than disappearing. Cancellation stops normal settlement but does not stop chain
observation; a later transfer still becomes a review case.

## Developer guide

### Core objects

- **Payment intent:** immutable commercial requirement and merchant references.
- **Route:** versioned quote and chain destination for one asset/network.
- **Transfer event:** normalized chain fact identified by network, transaction,
  event index, asset, and recipient.
- **Match/contribution:** auditable allocation of a transfer to a route.
- **Settlement:** immutable credited result in the double-entry ledger.
- **Domain event:** versioned merchant-visible fact with a tenant sequence.
- **Delivery:** retryable transport attempt for one canonical event body.
- **Unmatched case:** review workflow; it is not an editable transfer record.

### Payment state machine

```text
created → awaiting_route_selection → pending → observed → confirmed → settled
                                      │          └→ partially_paid
                                      ├→ expired → needs_review
                                      └→ cancelled
observed/partially_paid → needs_review → settled | reversed
settled → overpaid
confirmed/settled/overpaid → reorg_review → settled | reversed
```

The system never moves `settled` back to `pending`. Transfer finality follows
`observed → confirmed → finalized`, with explicit `reorged` and `invalidated`
paths.

An unmatched case follows `new → candidates_ready → bound →
verification_requested → verified → resolved`, with explicit
`approval_required`, `verification_retry`, `conflict`, and `reorged` branches.
Shortfall and cross-asset overrides require a second operator. Verification
reloads stored evidence and the chain; an operator cannot enter a credited
amount manually.

### Transfer binding and top-ups

Every route belongs to exactly one payment intent. The scanner binds a transfer
from the route's immutable facts rather than trusting a customer-supplied
transaction hash. Multiple transfers inside the admitted collection window are
added atomically only within that route; replaying the same event cannot increase
the total twice.

Public checkout returns `amount`, `received_amount`, `remaining_amount`,
`payment_count`, and `top_up_allowed`. In `partially_paid`, the payer sees the
exact net remainder and sends it to the same address on the same network.
Withdrawal fees must be added on top so the address receives exactly
`remaining_amount`. Once the window closes, `top_up_allowed=false`, payment QR
and copy controls disappear, and the case moves to review. The merchant does not
create a second invoice for the same transfer; retrying the API call with the
same idempotency key returns the original intent/session.

### Minimal API flow

```http
POST /v1/payment-intents
Idempotency-Key: order-2026-00042
Content-Type: application/json

{
  "merchant_order_id": "order-2026-00042",
  "amount_minor": "49900",
  "currency": "RUB",
  "currency_scale": 2,
  "description": "Annual plan",
  "customer_reference": "customer-opaque-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

All amounts in JSON are strings plus explicit scale/decimals. Every mutation
uses an idempotency key. The same key and body returns the original result; the
same key with different immutable input returns `idempotency_conflict`.

Relevant endpoints include:

- `POST/GET /v1/payment-intents`;
- `POST /v1/payment-intents/{id}/routes`;
- `POST /v1/payment-intents/{id}/cancel`;
- `POST /v1/payment-proofs` as a lookup hint, never as a paid switch;
- `GET /v1/events?after_sequence=...` for recovery;
- `GET /v1/transfers/{network}/{tx}` and reconciliation reports;
- sandbox simulation for observed, partial, settled, wrong-asset, and reorg.

### Request authentication

The HMAC profile signs raw body bytes:

```text
HMAC-SHA256(secret,
  METHOD + "\n" +
  CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" +
  NONCE + "\n" +
  SHA256_HEX(RAW_BODY))
```

Send key ID, timestamp, 128-bit nonce, `Content-Digest`, and signature headers.
High-assurance integrations may use Ed25519 and optionally mTLS. Credentials are
separate for sandbox and live and have narrow scopes.

### Webhook consumer contract

Only `payment.settled` normally grants the product. A consumer must:

1. limit and read the raw body;
2. validate key ID, timestamp, nonce/delivery ID, digest, and signature before
   trusting parsed JSON;
3. validate merchant, environment, event type, order reference, amount, and
   currency;
4. begin a database transaction;
5. insert `(event_id, body_digest)` into a unique inbox;
6. return the previous acknowledgement for an identical duplicate;
7. return conflict and alert if the same event ID has a different digest;
8. update the local order and enqueue fulfillment in the same transaction;
9. commit and return `acknowledged_event_id`.

Delivery retries keep the same event ID and canonical body but use a new
delivery ID, timestamp, nonce, and signature. HTTP delivery order is not
guaranteed; tenant sequence and event history provide reconciliation.

Runnable clients and consumers are in [`../../examples`](../../examples/README.md).

## Operations guide

### Deployment model

- Stateless API and admin BFF replicas behind an ingress/WAF.
- PostgreSQL as financial source of truth, partitioned where appropriate.
- Transactional outbox and leased workers; NATS/Redis are delivery aids, never
  the financial truth.
- Per-network indexers with durable cursors and provider quorum/fallback.
- Separate callback delivery, rate, reconciliation, and expiry workers.
- Isolated signer with no inbound Internet path and explicit approval policies.

### Required telemetry

Every intent, route, transfer, match, settlement, event, and delivery carries a
trace/correlation ID. Monitor:

- scanner lag and provider disagreement;
- observed-to-finalized and finalized-to-settled latency;
- unmatched age and queue depth;
- callback backlog, retry age, and dead letters;
- idempotency conflicts and signature/replay failures;
- ledger/reconciliation differences;
- quote age, provider failures, and address-pool capacity;
- reorgs, manual overrides, refunds, and signer operations.

### Incident priorities

Pause only the affected asset/route when possible. Runbooks cover RPC outage,
scanner lag, reorg, callback outage, unmatched growth, rate anomalies, key
compromise, ledger mismatch, signer failure, and database recovery. Never repair
financial history with ad-hoc row edits; use versioned configuration and
compensating ledger/domain events.

### Recovery and retention

Backups require encrypted continuous WAL, tested point-in-time recovery, object
storage for reports/evidence, and periodic restoration into an isolated
environment. Audit data is partitioned, redacted, retained by policy, and
exported to WORM storage where required. Webhook bodies and private metadata are
not kept indefinitely by accident.

### Production gate

Production requires passing contract, concurrency, duplicate, exact-money,
reorg, unmatched, security, localization, accessibility, load, soak, migration,
backup-restore, and reconciliation tests. See
[`../TEST_PLAN.md`](../TEST_PLAN.md). A skipped P0 invariant is a release blocker.
