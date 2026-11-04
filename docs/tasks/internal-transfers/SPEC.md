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

1. Detection runs in an internal lane with its own cursor and its own paced
   clients, behind the ordinary scan. Nothing in the lane can hold the ordinary
   cursor or wait on its limiter. A reorg rewinds both cursors together, and the
   lane cannot commit a range read before that reorg, a range the ordinary scan
   has not reached, or a transfer from a replaced block. Replicas share the lane
   through a lease.
2. An internal transfer is credited only when both trace providers agree and
   the complete `trace_transaction` call tree shows that the frame and all its
   ancestors succeeded, the frame is a plain `CALL` to a watched address, and
   its from, to and value match the discovery result.
3. A frame under a reverted ancestor, a self-transfer, or a delegatecall,
   callcode or staticcall frame is not credited and does not stop the lane.
4. An incomplete tree, a frame the tree does not contain, a provider error or a
   disagreement keeps the lane's cursor. After 30 minutes on one range the lane
   records the range and moves on.
5. What cannot be read on both providers (a trace over 8 MiB, more than 256
   transfers in a transaction, too many frames in a block) and a balance increase
   that agreed traces do not explain are recorded once, durably, and the lane
   moves on. More credited money than the balance shows, on an EOA with an
   unchanged nonce, keeps the cursor. What only one provider cannot read keeps
   the cursor too.
6. Each block is read once. Up to 8 wallets, an idle wallet costs balance reads
   only, through any two providers that agree. Above that the lane sweeps every
   range. A range waits until the trace providers pass the required confirmations.
7. Every EVM scanner reports whether internal detection is on, and warns when
   it is off. A wrong trace configuration keeps the lane off and the ordinary scan
   running, except a malformed static variable, which fails at start.
8. A production scanner gets two trace providers from admitted platform
   configuration (`trace_providers`); a static scanner keeps its two URLs.
9. The shipped network catalog carries two trace endpoints per network where
   two public ones were verified.
10. An operator can replay a range for internal transfers only, by rewinding the
    lane, and can recover one exact internal transfer with the call tree proof.
11. The checkout says that exchanges often deduct their fee from the amount.

## Non-goals

- Crediting selfdestruct or other forced Ether.
- Hard-coding third-party endpoints in the binary, or changing the live
  server's configuration.
- Replaying old blocks automatically.
- Verifying a customer's receipt for an internal transfer (the proof worker
  still reads only top-level transfers and logs); see DECISIONS.md.

## Acceptance matrix

| Case | Evidence |
| --- | --- |
| Frame under a reverted ancestor not credited, wallet with changed nonce | `evm_internal_filter_test.go` |
| delegatecall, callcode, staticcall, self-transfer, zero value not credited | `evm_internal_filter_test.go` |
| Incomplete, unbound or contradicting tree, failed receipt keep the cursor | `evm_internal_filter_test.go` |
| Providers disagree on an ancestor keeps the cursor | `evm_internal_filter_test.go` |
| Crafted transaction recorded, not credited, lane moves on | `evm_internal_filter_test.go` |
| Unexplained inflow recorded, scan continues | `evm_internal_filter_test.go` |
| Over-explained EOA balance keeps the cursor | `evm_internal_filter_test.go` |
| Any two agreeing probes; trace outage waits only when a trace is needed | `evm_internal_filter_test.go` |
| Evidence holds chain facts only | `evm_internal_filter_test.go` |
| Endpoint URLs stay out of errors | `evm_internal_filter_test.go` |
| Lane follows the ordinary cursor, gives up after its budget, idle, paused | `scanner/internal_lane_test.go` |
| Lane storage under the production role, replay, cursor race, no ordinary cursor change | live S9 |
| Reorg lowers the lane cursor, marks queued rows, refuses replaced blocks at commit and settlement | live S9 |
| A read from before a reorg cannot commit, even below the fork; lease | live S9 |
| Commit survives a timed-out step; budget belongs to one range; a slow range narrows | `scanner/internal_lane_test.go` |
| A failed probe sits out the range; pages shrink and grow back; bounds in one constructor | `evm_internal_filter_test.go` |
| Open findings reach the watchdog | `tests/unit/test_scanner_watchdog.py` |
| Limiter does not hold a short deadline; confirmations from the slower provider | `evm_internal_filter_test.go` |
| Ordinary scan never reads through trace providers | `cmd/scanner/main_test.go` |
| Detection state reported and warned | `cmd/scanner/main_test.go` |
| Catalog endpoints: two, HTTPS, distinct hosts | `cmd/scanner/main_test.go` |
| Loader plans the lane, wrong config keeps scanning, admission, watch margin, pause | `platformruntime/scanner_test.go` |
| Recovery reads an internal transfer through two trace endpoints, refuses another chain | `evm-payment-recovery/main_test.go`, `exact_recovery_test.go` |
| Real exchange withdrawal on two providers | opt-in `TestEVMInternalFilterLiveContractWithdrawal` |
| Checkout shows the exchange fee note | `apps/checkout/src/App.test.tsx` |
