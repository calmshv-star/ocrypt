# cabinet-checkout-stage2 specification

## Requested outcome

Plain-language payment statuses, onboarding progress, PWA cabinet and checkout requirements CHK-2, CHK-5, CHK-6 and CHK-8

## Interview and scope

Source: product specification stage 2 ("Статусы платежа для человека",
"Экраны кабинета", "Страница оплаты" CHK-1..8, PWA requirement). The user
asked to finish the specification without further questions.

Scope: merchant-facing wording, hint and next action for every
`intent_status`; onboarding progress on the overview; an installable PWA
manifest; checkout wrong-network warning (CHK-5), merchant-as-recipient and
"software, not the recipient of funds" disclaimer (CHK-6), region refusal on
public payment routes (CHK-8) and wallet deep links (CHK-2).

Parameters: region refusal is off unless `MANAGEMENT_BLOCKED_REGIONS` and
`MANAGEMENT_REGION_HEADER` are both set; the country comes only from the
trusted edge header, a repeated header is refused, a missing header is served.
Wallet links exist only for EIP-681 (EVM native and ERC-20), Solana Pay on
mainnet and `ton://transfer` for native TON on mainnet, for the amount still
due.

Non-goals: WalletConnect/TonConnect sessions (need a registered project ID),
TRON and TON jetton links (no widely supported standard), subdivision-level
region blocking (Crimea and similar need an edge header with subdivision
codes), a live/test switch (billing stage), offline service worker.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| C1 | Every `intent_status` value | Localized label and hint in six locales; only `settled` reads as paid; unknown states read as needing review; review/reorg link to the queue, expired/underpaid to a new link | `apps/admin/src/paymentStatus.test.ts` | passes |
| C2 | Overview for a wallet holder with unfinished onboarding | Progress banner until every visible step is done | `Signup.test.tsx` | passes |
| C3 | CHK-5/CHK-6 on an open route | "Send only {asset} on {network}" warning; merchant named as recipient; disclaimer | `apps/checkout/src/App.test.tsx` | passes |
| C4 | CHK-8: blocked, lower-case, padded, repeated, missing, allowed country; bad configuration | 451 `unavailable_in_region` readable cross-origin on all five public routes; repeated refused; missing/allowed served; invalid config refused | `internal/management/region_gate_test.go` | passes |
| C5 | CHK-8 in the browser | Neutral final message, no retry, no further polling; payment-link page too | `apps/checkout/src/App.test.tsx` | passes |
| C6 | CHK-2 link building | Exact EIP-681/Solana Pay/ton links; refused for TRON, jettons, devnet, non-numeric EVM chain, malformed or injected addresses, zero/nil amount; remainder only | `internal/management/payment_uri_test.go` | passes |
| C7 | CHK-2 in the browser | Link rendered only when it matches the strict pattern; `javascript:`, https and whitespace-injected values dropped | `apps/checkout/src/App.test.tsx` | passes |
| C8 | PWA | Standalone manifest, 192/512 and maskable PNG icons with matching sizes, manifest and touch icon linked | `tests/web/test_admin_pwa.py` | passes |
| C10 | Review fixes: link only while the route accepts a transfer; malformed or joined region values refused, preflight not gated; human network and asset names; CHK-6 text only with `direct_to_merchant`; 451 on select-route | As described in DECISIONS.md | `payment_uri_test.go`, `region_gate_test.go`, `apps/checkout/src/App.test.tsx` | passes |
| C9 | Reports: payments CSV export | RFC 4180 quoting, formula cells neutralized, exact string amounts, localized headers and status, paging at 200 until the server stops, capped at 10 000 rows with a notice | `apps/admin/src/csvExport.test.ts` | passes |

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
