# cabinet-checkout-stage2 decisions

Goal: Plain-language payment statuses, onboarding progress, PWA cabinet and checkout requirements CHK-2, CHK-5, CHK-6 and CHK-8

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-03 — independent review of abf1399, 46a2f5b, 51b3438, 60b3066

No critical or high findings. Accepted and fixed:

1. **payment_uri on routes that no longer accept a transfer (medium).** The
   server now builds it only when `routeAcceptsTransfer`: route `active`,
   before its collection deadline, and either untouched while the intent is
   `pending` or open for a top-up. Test: `TestPaymentURIOnlyForRoutesThatStillAcceptATransfer`.
2. **Region gate failed open on comma-joined or malformed values (medium).**
   Any value other than one region code is refused; `T1` (Tor) and `XX`
   (unknown) are accepted codes, `T1` can be blocked. CORS preflights skip the
   gate so cross-origin pages can read the 451.
3. **CHK-5 warning showed raw identifiers (medium).** On-chain routes now
   carry `network_name` and `asset_symbol`; the page shows them in the
   warning, amounts, selector, summary and the wallet button
   ("Open in wallet · Ethereum"), with raw ids only as a fallback.
4. **CHK-6 text could be false (low/medium).** Sessions carry
   `direct_to_merchant`, true only in the noncustodial edition; the
   "goes directly to {merchant}" line and the "software, not the recipient"
   disclaimer appear only then and only for on-chain routes.
5. **Merchant wording (low).** `overpaid` reads "Paid, with overpayment" with a
   positive badge and says goods may ship; `reorg_review` reads "Rechecking
   after a reorganization"; onboarding counts only `settled`/`overpaid` as paid.
6. **Underpaid action (low).** No "new payment link" for an underpaid order
   (the payer tops up on the same page); the hint no longer promises an
   "accept as is" action. The expired-order link needs `payment_links:write`.
7. **451 on select-route and receipt (low).** Both show the final region
   message.
8. **Wallets that ignore EIP-681 `@chainId` (low).** The button names the
   network. Not changed: shared EVM deposit addresses across chains belong to
   the full edition's bootstrap data.
9. **Tests (low).** The banner test waits for both queries before asserting;
   the CHK-2 UI test covers a foreign address, a settled route and a partial
   route without top-up; the client drops a link that does not name the
   route's own address. Amount cross-checking in the client was not added:
   the server is the only source of the atomic amount.
