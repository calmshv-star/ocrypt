# ton-zero-value-transfer specification

## Requested outcome

TON scanner skips successful zero-value transfers instead of failing the range

## Interview and scope

Production `ocrypt-scanner-ton` (shard `ton-live-batch-canary`, image
`20260913-b91cdef`) has been unhealthy since 2026-10-21 19:24 UTC with cursor
stuck at MC 100504303. Read-only replay of the next range against toncenter v3
found one successful `ton_transfer` to the single watched address at MC
100504329 with `details.value == "0"` and a comment (a notification). The
normalizer treated amount `"0"` as `malformed_response`, so every retry of the
range failed closed.

Scope: zero-value native or Jetton transfer actions that are otherwise well
formed are skipped because they cannot fund any route. Malformed amounts
(empty, negative, fractional, non-numeric, leading zeros) still fail closed.
No change to quorum, finality, cursors, providers, intervals, or assets.
Non-goals: deployment, cursor edits, BSC provider changes.

## Acceptance matrix

| ID | Behavior/input/fault | Expected result | Test or manual check | Evidence |
| --- | --- | --- | --- | --- |
| A1 | Successful zero-value `ton_transfer` with comment to a watched address inside the range | Range succeeds; no event for it; funded transfers still emitted | `TestTONWatchedRangeSkipsZeroValueTransfers/ton_transfer` | failed before fix with `ton transfer action ... invalid amount`; passes after |
| A2 | Successful zero-amount `jetton_transfer` to a watched address | Same as A1 | `TestTONWatchedRangeSkipsZeroValueTransfers/jetton_transfer` | as A1 |
| A3 | Amount empty, `-1`, `1.5`, `abc`, `00x` | Range fails closed with `invalid amount` | `TestTONWatchedRangeStillRejectsMalformedAmounts` | passes before and after |
| A4 | Existing boundary/normalization behavior | Unchanged | `providers` package, `check backend`, `check financial` | passed |

For financial changes include relevant exact-money, negative, duplicate/replay,
concurrency, tenant, finality, and external-delivery cases.

## Runtime prerequisites

Identify required local services and external evidence separately. Record which
checks are offline/static and which require a live test environment.
