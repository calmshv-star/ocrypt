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
  environment is the separate sandbox (`livemode=false`). New payment routes
  are admitted only by platform configuration: a tenant
  `merchant_environment` head for the merchant, a tenant `feature_flag`
  `new_routes` at 10000 bps, and the global chain, asset and finality heads
  (`platform_route_runtime_admission`).
- Receiving wallets are platform objects: a watch-only wallet must exist and
  be admitted through a `wallet_pool` platform change. All platform changes
  are four-eyes. A self-service owner has no wallet and cannot create one.
- Matching-policy versions require `approved_by <> requested_by` and
  `activated_by <> requested_by` at the database level. Without a policy, an
  exact payment still settles; partial, under- and overpayments and late
  payments go to review. That is the strict behaviour.

Flow:

1. The owner proves each receiving address with a signed challenge
   (EVM `personal_sign`, Solana `signMessage`, TRON `signMessageV2`). The
   message names the cabinet domain, the merchant, the network, the address,
   a nonce and a 5-minute window. The cabinet connects the browser wallet and
   checks its account before signing, or accepts a pasted signature.
2. The owner picks a preset and submits a go-live request (one open request
   per merchant, self-service merchants only). Preconditions: an MFA session,
   an active owner of an active test-mode merchant (a held signup has no
   rights), at least one proven address. The request stores the preset; **no
   matching-policy change exists before approval**, so nothing outside the
   review can approve it or strand the request.
3. A global reviewer (`go_live_reviews:decide`, MFA) sees the queue with the
   tenant, merchant, canonical addresses, wallet ids and admission states;
   creates the inert wallet and deposit address records with one action; and
   the operators admit each `wallet_pool`, the `merchant_environment` and
   `new_routes` with the usual four-eyes platform changes. Approval requires
   every address admitted and routable, and — when a policy is filed — global
   `matching_policy:approve` and `matching_policy:activate` too. In one
   transaction it:
   - files the preset's policy change already activated, requested by the
     owner and approved and activated by the reviewer, with a neutral reason
     (the merchant can read its policies), under the matching-policy version
     lock, plus a neutral entry in the merchant's management audit;
   - switches the merchant to `live`;
   - writes the decision and its reason to the global audit log only.

   Strict files nothing for a merchant without a policy and a strict policy
   for one that already has one. Rejection files nothing. The registrant or
   any member can never decide, and a decision is final.
4. Every member of the merchant sees Test or Live in the header; onboarding's
   last step links to the go-live page, which warns that test keys and the
   sandbox stop working after going live.

Presets (matching-policy fields only; invoice lifetime is set per payment
link or API call and stays there):

| Preset | accumulate_partials | underpayment_tolerance_bps | overpayment_mode | accept_late_within_grace | require_same_sender |
| --- | --- | --- | --- | --- | --- |
| Надёжно (strict) | no policy: exact amount only, everything else to review | | | | |
| Стандарт (standard) | true | 50 (0.5%) | credit_expected_hold_excess | true | true |
| Быстро (fast) | true | 200 (2%) | credit_expected_hold_excess | true | false |

Confirmations are platform finality per chain and are not merchant-tunable.

Non-goals: automatic platform admission (it would bypass the four-eyes
control; the queue only prepares inert wallet records), a TON ownership proof (TON Connect's ton_proof needs the
wallet's state init; TON addresses are not accepted in a request yet),
changing presets after going live (the existing matching-policy flow
applies). TRON is proven with TronLink signMessageV2.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| L1 | Address proof: valid EVM, Solana and TRON signatures; another domain, merchant, network or signer, expired, malformed, wrong kind | Accepted; refused | `TestGoLiveProofsBindDomainMerchantNetworkAndAddress`, `TestGoLiveRequestNeedsOwnSignedAddressesAndAnMFASession` | passes |
| L2 | Submit: not owner, live already, held signup (no rights at all), unknown preset, no or duplicate or inactive chain, second open request, proof for another merchant, tampered token, no MFA session | Refused | live `S4_go_live_request`, BFF tests | passes |
| L3 | Submit with fast/standard | Pending policy change requested by the owner with the preset values; strict files none | live `S4`, BFF tests | passes |
| L4 | Decide: tenant-scoped or non-global operator, auditor, registrant, short reason, twice | Refused; one decision | live `S4`, BFF tests | passes |
| L5 | Approve before wallets are admitted; after | Refused with conflict; merchant live, policy active with approver ≠ requester, audit | live `S4` (SQL and Go repository) | passes |
| L6 | Reject | Request rejected with reason, merchant stays test, policy change rejected | live `S4` | passes |
| L7 | Cabinet | Test/Live badge; preset choice, message, wallet or pasted signature, request state; operator queue with approve only when admitted | `GoLive.test.tsx` | passes |

## Runtime prerequisites

Live cases need a disposable PostgreSQL 18 database. Real wallet admission
in production stays an operator four-eyes step.
