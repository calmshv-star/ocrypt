# stage2-completion specification

## Requested outcome

Finish the stage-2 cabinet and checkout items that need no owner input: a plain sign-in failure page, network fee hints and a locked-amount timer on checkout, and daily totals for accounting with XLSX export

## Interview and scope

Source: product specification (TZ) stage 2 — "понятная страница при сбое
OIDC-callback", CHK-1 (network choice with a fee hint), CHK-3 (rate lock
timer; status refreshed within 5 s — polling every 5 s with ETag already
exists), and Reports ("итоги по дням для бухгалтерии (фиатная оценка на момент
оплаты), экспорт CSV/XLSX"). The owner asked for no questions; choices are in
DECISIONS.md.

- Sign-in failure: a failed OIDC callback redirects (303) to
  `/#/sign-in-failed?reason=` one of `cancelled`, `provider`, `expired`,
  `not_allowed`, `mfa`, `failed`, sets no session and clears the correlation
  cookie. The page explains what to do and offers to sign in again; an
  unknown reason reads as `failed`.
- CHK-1: each on-chain route in the selector carries a qualitative fee tier
  (low: BSC, Polygon, OP, Arbitrum, Base, Avalanche, Plasma, Solana, TON,
  Aptos; medium: TRON; high: Ethereum), and the selected route explains it.
- CHK-3: on-chain checkout routes carry `expires_at`, the last moment they
  accept a transfer for their fixed amount (grace end when the frozen policy
  accepts late payments). The checkout counts down to the earlier of that and
  the session expiry, reads "Amount locked for MM:SS" only when the route
  deadline is known (otherwise "Time remaining to pay"), and hides the timer
  once the payment is final.
- Reports: `GET /admin/v1/reports/daily-totals?from&to&time_zone` (payments:
  read) sums settled and overpaid intents per payment day — when the last
  finalized transfer reached the chain, else `settled_at` for orders settled
  without a transfer — in the IANA zone, and currency at the exact invoice
  amount, using the partial index `payment_intents_paid_day_idx` (000060), for at most 366 days,
  in the authorized tenant or merchant scope. The Reports page shows days and
  exact per-currency totals and exports CSV and XLSX; the payments export
  gains XLSX. The XLSX writer is dependency-free; text is never a formula and
  amounts beyond 15 significant digits stay exact as text.

Non-goals: fee amounts in money (they change with the market), a merchant
profile time zone (the report takes the zone as a parameter), multi-sheet
workbooks.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| C1 | Callback with provider error, access_denied, missing or foreign correlation | 303 to the failure page with the generic reason, no session cookie, no provider detail | `TestCallbackFailuresRedirectToAPlainPage` | |
| C2 | Error mapping | step-up → mfa, forbidden → not_allowed, unauthenticated → expired, other → failed | `TestCallbackFailureReasonsStayGeneric` | |
| C3 | Failure page | Reason text, sign-in link, back link only with a session, unknown reason generic | `SignInFailed.test.tsx` | |
| C4 | Route selector | Fee tier per network in each option and a hint for the selected one | checkout `App.test.tsx` CHK-1 case | |
| C5 | Daily totals SQL | UTC and Europe/Berlin day boundaries, Berlin's 25-hour day, transfer day rather than processing day, reorg_review with a stale settled_at excluded, overpaid counted at invoice amount, pending excluded, merchant scope with a merchant-only principal, other tenant never summed, 30-digit exact sums, unknown zone invalid | live `S3_daily_totals_report` | passes |
| C6 | Report route | Default UTC, reversed/loose/over-a-year/injected values refused before the repository, other tenant and missing permission forbidden | `TestDailyTotalsReportValidatesItsRangeAndScope` | |
| C7 | XLSX | Valid stored ZIP (CRC checked by Node), five parts, numbers vs exact text, escaping, no formulas, sheet-name rules | `xlsx.test.ts`, Python `zipfile.testzip` and XML parse of a sample | |
| C8 | Reports page | Exact per-day and total amounts (BigInt), XLSX download, reversed period refused locally | `Reports.test.tsx` | |

## Runtime prerequisites

C5 needs a disposable PostgreSQL 18 database; everything else is offline.
