# internal-transfers plan

Goal: Credit native transfers that exchanges send through contracts by default, safely, and warn payers about exchange fees

Initial source revision: `06fe91209767185bf427efb765fee5c8b161813a`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (the filter credited a reverted transfer and a delegatecall).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-internal-transfers` on main).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

- 2026-11-04 expected failures: before the fix, `TestEVMInternalFilterCreditsOnlySuccessfulPlainCalls` credited a transfer under a reverted parent, under a reverted root, and delegatecall, callcode and staticcall frames.
- 2026-11-04 live, read-only, `TestEVMInternalFilterLiveContractWithdrawal` from the local checkout:
  - Ethereum, `docs-demo.quiknode.pro` + `eth.drpc.org`: passed on a real exchange withdrawal (`trace:11`, 0.025067 ETH). `eth.merkle.io` hit 429 on balance reads.
  - Base, `docs-demo.base-mainnet.quiknode.pro`: `trace_filter` worked. `base.drpc.org` answered 408; `base.merkle.io` answered `trace_filter` and then 429 on `trace_transaction`. No pair was completed, so Base is not in the catalog.
  - Optimism, `docs-demo.optimism.quiknode.pro` + `optimism.drpc.org`: both answered `trace_filter` on a current block and returned the same 16-frame `trace_transaction`. No real payment was checked.
  - The Ethereum check was repeated on the lane version and passed again.

## Resume / next action

The first independent review found stop-the-chain paths, so detection moved to its
own lane. The second found false credits across a reorg, a stuck give-up, a
shared limiter and memory exhaustion, all fixed; see DECISIONS.md 13-17. The third
found a silent skip after a reorg, a lane with no lease, slow-path stalls and
alerts nothing could fire, all fixed; see DECISIONS.md 18-22. Mutation runs over
the new guards found the tests that proved too little, and tests were added.
Remaining limits: customer receipts for internal transfers; the
limits listed at the end of DECISIONS.md; no public pair for Base, Arbitrum,
Polygon, Avalanche, BSC or Plasma. Nothing here is deployed.
