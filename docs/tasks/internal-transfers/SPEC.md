# internal-transfers specification

## Requested outcome

Credit native transfers that exchanges send through contracts by default, safely, and warn payers about exchange fees

## Interview and scope

Source: TZ remaining item after the 2026-11-03 complaint: Coinbase paid
0.025067 ETH to a route through a contract (an internal transaction, block
26336112). Two independent public trace providers return that transfer; the
ordinary block scan cannot see it. The owner asked for no questions; agent
decisions are in DECISIONS.md.

Facts found in the code:

- `EVMInternalFilter` exists but is opt-in (`SCANNER_EVM_INTERNAL_TRACE_URLS`,
  exactly two independent `trace_filter` endpoints).
- It credits any `trace_filter` frame without an own error. A frame under a
  reverted ancestor carries no error of its own, and the balance cross-check
  is skipped for contracts and for wallets whose nonce changed, so a crafted
  contract could produce a credit without money arriving.
  `docs/tasks/scanner-recovery-20260930/PR_AUDIT.md` recorded this gap.
- It ignores `action.callType`, so a `delegatecall` or `callcode` frame counts
  as a value transfer, and `evmTraceMovesValue` lists CALLCODE too.
- After an agreed trace, a balance delta that traces do not explain stops the
  scanner. Forced Ether (selfdestruct, a beacon withdrawal, a block reward)
  needs no trace frame the filter credits, so one such transfer would stop
  every payment on that chain for good.
- A `null` `trace_filter` page reads as an empty result.

## Behaviour

1. An internal transfer is credited only when both trace providers agree and
   the complete `trace_transaction` call tree shows that the frame and all its
   ancestors succeeded, the frame is a plain `CALL` to a watched address, and
   its from, to and value match the discovery result.
2. A frame under a reverted ancestor, or of type delegatecall, callcode or
   staticcall, is not credited and does not stop the scanner.
3. An incomplete tree, a frame the tree does not contain, a provider error, a
   disagreement or a `null` page leaves the cursor unchanged.
4. A balance increase that agreed traces do not explain no longer stops the
   scanner. It is counted and logged for operators. More explained inflow
   than the balance shows, on an EOA with an unchanged nonce, still stops it.
5. Every EVM scanner reports whether internal detection is on, and warns when
   it is off.
6. A production scanner gets two trace providers from admitted platform
   configuration (`trace_providers`); a static scanner keeps its two URLs.
7. The shipped network catalog carries two trace endpoints per network where
   two public ones work.
8. An operator can recover one missed internal transfer that matches its route
   exactly, with the same call tree proof.
9. The checkout says that exchanges often deduct their fee from the amount.

## Non-goals

- Crediting selfdestruct or other forced Ether.
- Hard-coding third-party endpoints in the binary, or changing the live
  server's configuration.
- Replaying old blocks.
- Verifying a customer's receipt for an internal transfer (the proof worker
  still reads only top-level transfers and logs); see DECISIONS.md.

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Frame under a reverted ancestor not credited, wallet with changed nonce | `evm_internal_filter_test.go` |
| delegatecall, callcode and staticcall frames not credited | `evm_internal_filter_test.go` |
| Incomplete tree, unknown frame, field mismatch keep the cursor | `evm_internal_filter_test.go` |
| Providers disagree on an ancestor keeps the cursor | `evm_internal_filter_test.go` |
| Unexplained inflow reported, scan continues | `evm_internal_filter_test.go` |
| Over-explained EOA balance keeps the cursor | `evm_internal_filter_test.go` |
| `null` trace page keeps the cursor | `evm_internal_filter_test.go` |
| Detection state reported and warned | `backend/cmd/scanner` tests |
| Catalog endpoints: two, HTTPS, distinct hosts | `backend/cmd/scanner` tests |
| Production loader wraps admitted trace providers, refuses unfit ones | `platformruntime/scanner_test.go` |
| Recovery accepts an internal transfer only with two trace endpoints | `evm-payment-recovery/main_test.go`, `exact_recovery_test.go` |
| Real exchange withdrawal on two providers | opt-in `TestEVMInternalFilterLiveContractWithdrawal` |
| Checkout shows the exchange fee note | `apps/checkout/src/App.test.tsx` |
