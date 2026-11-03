# go-live-request decisions

Goal: Let a self-service merchant owner prove its receiving addresses, choose an acceptance preset and ask to go live, and let platform operators admit it without weakening any four-eyes control

## 2026-11-03 — agent decisions (owner: "do the whole specification, I am not here")

1. **No control is weakened.** Wallets become usable only through the
   existing four-eyes `wallet_pool` platform change, and matching policies
   keep `approved_by <> requested_by`. The owner never approves its own
   preset: the go-live reviewer is the second person. Nothing writes an
   approval on someone's behalf.
2. **Strict needs no approval** because it is the behaviour without a
   policy: only exact payments settle automatically.
3. **Preset values** (matching-policy fields only; invoice lifetime stays per
   link or API call): standard 0.5 % tolerance with partials from the same
   sender and late payments in grace; fast 2 % and any sender. Overpayments
   credit the expected amount and hold the excess in both.
4. **Ownership proof** reuses the wallet-import verifiers (EVM, Solana) and
   adds TRON signMessageV2 — the most used USDT network. TON is left out until
   a ton_proof verifier exists. The message binds domain, merchant, network,
   address, nonce and a five-minute window; the token is sealed with the
   state box; each nonce is unique in the database (no replay).
5. **Approval needs admitted addresses**, checked in the same transaction
   (`go_live_address_admitted`), so the merchant cannot be live without a
   usable wallet, and the reviewer cannot skip the platform tools.
6. **A held registrant owns nothing**, so it cannot file a request (OCP01),
   which is stricter than a separate "screening" refusal.
7. **Errors** use OCP01 (not the owner / not a global reviewer / a member
   deciding), OCP02 (invalid input) and 55000 (not possible now: already
   live, open request, wallet not admitted, decided).
8. **Reasons are at least eight characters**, the matching-policy approval
   minimum, because the reason becomes the policy approval reason.
9. **Decision audit is global** (tenant NULL) like REG-3, so the owner's
   audit view never shows the reviewer's note.

## 2026-11-03 — independent review of df86c23..64aa2e4 (go-live)

No way was found for one person to activate a policy or go live alone.
Fixed:

- **H1** The preset's policy change was filed at submit, so anyone with
  `matching_policy:approve` could approve and activate it outside the review,
  after which the request could never be decided or re-filed. The change is
  now filed only by approval, already activated (owner requester, reviewer
  approver and activator); a rejection files nothing.
- **M1** The reviewer's reason reached `approval_reason`,
  `activation_reason` and `approval_reference`, which `merchant_owner` can
  read through `matching_policy:read`. Those now carry a neutral
  "Platform go-live review <request id>"; the reason stays in the request and
  the global audit log.
- **M2** Approval did not enable payments: route allocation needs the
  `merchant_environment` and `new_routes` heads, which self-service signup
  never creates, and operators had no tool to create wallet records.
  Approval now also requires `platform_route_runtime_admission` for each
  chain; reviewers get a "Create wallet records" action (inert records from
  the proven, canonical addresses); the queue shows tenant, canonical address
  and the wallet id (the `wallet_pool` key); the README lists the exact
  platform changes.
- **M3** Browser signing now connects the wallet (`eth_requestAccounts`,
  `tron_requestAccounts`, Phantom `connect`), checks the account against the
  address, and reports "no wallet", "another account" and "did not sign"
  separately from an invalid address.
- **Lows:** the matching-policy version lock (L1); global
  `matching_policy:approve`/`activate` required to file a policy (L2); a
  neutral management audit entry (L3); self-service merchants only (L4); the
  badge for every member with a dashboard, nothing while loading (L5); 409
  only for an open request or a live merchant, expired proofs are 400 (L6);
  readiness covers all go-live functions (L7); live copy no longer points the
  owner to a page they cannot edit (L8); strict replaces an existing policy
  (L9); a warning that test keys and the sandbox stop (L10); the rollback can
  run twice and documents what stays (L11); comment placement (L12).
