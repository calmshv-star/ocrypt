# go-live-request specification

## Requested outcome

Let a self-service merchant owner prove its receiving addresses, choose an acceptance preset and ask to go live, and let platform operators admit it without weakening any four-eyes control

## Interview and scope

Source: TZ REG-4 ("live mode only after 2FA and a confirmed wallet") and the
onboarding wizard steps "Кошелёк" (address + signed message, no seed field)
and "Правила приёма" (presets Быстро / Стандарт / Надёжно). The owner asked
for no questions; agent decisions are in DECISIONS.md.

Facts found in the code that shape the design:

- A self-service merchant starts in `environment='test'`; the test
  environment is the separate sandbox. Real payments need `live`.
- Receiving wallets are platform objects: a watch-only wallet must exist and
  be admitted through a `wallet_pool` platform-configuration snapshot, which
  is a four-eyes change. A self-service owner has no wallet and cannot create
  one.
- Matching-policy versions require `approved_by <> requested_by` and
  `activated_by <> requested_by` at the database level. Without a policy, an
  exact payment still settles; partial, under- and overpayments and late
  payments go to review. That is the strict behaviour.

Flow:

1. The owner proves each receiving address with a signed challenge
   (EVM `personal_sign`, Solana `signMessage`; the existing proof code). The
   message names the merchant, chain and address, a nonce and a 5-minute
   window.
2. The owner picks a preset and submits a go-live request (one open request
   per merchant). Preconditions: merchant active and in test mode, screening
   `clear` or `approved`, at least one proven address. Choosing **Быстро** or
   **Стандарт** also files a matching-policy change request
   (`pending_approval`, requested by the owner). **Надёжно** files nothing.
3. A platform operator with a global `go_live_reviews:decide` binding and an
   MFA session sees the queue (merchant, screening state, preset, proven
   addresses). The operator creates and admits the wallets with the existing
   four-eyes platform tools, then approves. Approval requires every requested
   address to be the current deposit address of an admitted watch-only wallet
   of that merchant on that chain. In one transaction it then:
   - approves and activates the preset's policy change, with the operator as
     approver and activator, so the owner and the operator are two people;
   - switches the merchant to `live`;
   - writes an audit entry.

   Rejection needs a reason. The registrant can never decide, and a decision
   is final.
4. The cabinet always shows Test/Live for the current merchant; onboarding's
   last step shows the request state.

Presets (matching-policy fields only; invoice lifetime is set per payment
link or API call and stays there):

| Preset | accumulate_partials | underpayment_tolerance_bps | overpayment_mode | accept_late_within_grace | require_same_sender |
| --- | --- | --- | --- | --- | --- |
| Надёжно (strict) | no policy: exact amount only, everything else to review | | | | |
| Стандарт (standard) | true | 50 (0.5%) | credit_expected_hold_excess | true | true |
| Быстро (fast) | true | 200 (2%) | credit_expected_hold_excess | true | false |

Confirmations are platform finality per chain and are not merchant-tunable.

Non-goals: automatic wallet admission (it would bypass the platform
four-eyes control), TRON and TON ownership proofs (operators verify those
addresses out of band; the request marks them unproven), changing presets
after going live (the existing matching-policy flow applies).

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| L1 | Address proof: valid EVM and Solana signatures; wrong signer, wrong merchant, expired, replayed challenge | Accepted; refused | Go unit tests | |
| L2 | Submit: not owner, live already, screening held/rejected, no proven address, second open request | Refused | live `S4_go_live_request` | |
| L3 | Submit with fast/standard | Pending policy change requested by the owner with the preset values; strict files none | live `S4` | |
| L4 | Decide: tenant-scoped or non-global operator, registrant, short reason, twice | Refused; one decision | live `S4`, BFF tests | |
| L5 | Approve before wallets are admitted; after | Refused with conflict; merchant live, policy active with approver ≠ requester, audit | live `S4` | |
| L6 | Reject | Request rejected with reason, merchant stays test, policy change rejected | live `S4` | |
| L7 | Cabinet | Test/Live badge; wizard preset choice and request state; operator queue | vitest | |

## Runtime prerequisites

Live cases need a disposable PostgreSQL 18 database. Real wallet admission
in production stays an operator four-eyes step.
