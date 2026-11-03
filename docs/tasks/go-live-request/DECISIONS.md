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
