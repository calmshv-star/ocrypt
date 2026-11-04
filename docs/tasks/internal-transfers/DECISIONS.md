# internal-transfers decisions

Goal: Credit native transfers that exchanges send through contracts by default, safely, and warn payers about exchange fees

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-11-04 — agent decisions (owner: "do the whole specification, I am not here")

1. **Safety first, then default.** The filter could credit money that never
   arrived (a transfer under a reverted ancestor, on a wallet whose nonce
   changed; a `delegatecall` frame). The tests reproduced both before the fix.
   Turning detection on without the call tree check would have traded a missed
   payment for a false credit.
2. **Forced Ether no longer stops the chain.** After agreed traces, a balance
   increase they do not explain is reported and the scan continues. The old
   behaviour let one selfdestruct to a merchant wallet stop every payment on
   the chain for good. The price: if both trace providers ever omit a real
   transfer, the cursor moves on, and the report is the only signal. Provider
   errors, disagreement, incomplete trees and over-explained balances still
   stop the scan.
3. **"By default" means the shipped configuration, not the binary.** Third-party
   endpoints are not hard-coded, because detection adds RPC load and a new
   dependency that can stop a chain, and the handover says not to change
   providers on the owner's behalf. The catalog names the pairs, the runbook
   gives the two steps, and a scanner without detection warns and sets
   `merchant_scanner_internal_detection_missing`. The live server needs the
   owner's step; it is in the TZ list.
4. **Production mode goes through admission.** `trace_providers` names admitted
   RPC snapshots with the `traces` capability, so the four-eyes process stays
   the only way to add a provider. A trace provider that loses admission stops
   the scan: moving on would skip transfers only it can see.
5. **The tree check stands on its own.** Frames are read with
   `trace_transaction`; the tree builder comes from the unmerged PR #101. Depth
   is limited to the EVM's 1024 instead of 128, and the 4096 frame limit is
   gone: both would have let a crafted transaction stop the scanner. The 64 MiB
   response limit bounds the size.
6. **Exact recovery only.** The recovery command accepts an internal transfer
   with two trace endpoints and the block. It refuses an amount that differs
   from the route, so the 3 November complaint (0.000021 ETH short, if the
   invoice was 0.025088) still needs a merchant decision.
7. **Customer receipts are not covered.** The proof worker needs the watched
   addresses, which a route-watching deployment does not have in its
   environment. That is its own task; it is recorded in the TZ.
8. **Fee note.** The checkout already mentioned fees in general; it now says
   plainly that exchanges often deduct theirs from the amount entered.

