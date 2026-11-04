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
2. **A separate lane, after the first review.** The first version filtered inside
   the ordinary scan. The review showed that one cheap crafted transaction could
   stop a chain for good (a response over the limit is not retryable), that an
   overlap rescan repeated the cost every five seconds, and that a replayed
   event could dead-letter. All of that comes from one cause: a best-effort,
   third-party dependency sitting in the path of every payment. Tracing now has
   its own cursor. A failure delays internal transfers only, each block is read
   once, and there is no overlap to replay.
3. **Bounded retries.** A failing range retries for 30 minutes, then it is recorded
   and the lane moves on. The timer is in memory, so a restart restarts it; the
   bound holds per process. A transaction that makes providers time out can delay
   internal transfers by that long, and nothing else.
4. **Findings are rows.** What the lane cannot credit goes to
   `scanner_unattributed_inflows`, once per finding. If both trace providers ever
   omit a real transfer, this row is the only signal, so it must be durable.
5. **A sweep instead of a bisection.** The old filter split a range to single blocks
   with balance reads, which multiplied with the number of addresses. When a
   range needs a trace, the lane now sweeps it with `trace_filter`, whose cost
   does not depend on the number of addresses. Above 8 wallets balance reads are
   skipped altogether. The price: an unexplained increase is not reported in a
   range where the same wallet also sent a transaction, nor above 8 wallets.
6. **"By default" means the shipped configuration, not the binary.** Third-party
   endpoints are not hard-coded: they receive the receiving addresses, and the
   handover says not to change providers on the owner's behalf. The catalog names
   the verified pairs, the runbook gives the steps, and a scanner without
   detection warns and sets `merchant_scanner_internal_detection_missing`. The
   live server needs the owner's step; it is in the TZ list.
7. **Production mode goes through admission.** `trace_providers` names RPC
   snapshots with the `traces` capability, so the four-eyes process stays the only
   way to add a provider. They load apart from the scan's atomic state, because a
   missing trace provider must not stop ordinary payments. Lane commits carry no
   runtime evidence; each event's own evidence names the trace frame.
8. **The tree check stands on its own.** The tree builder comes from the unmerged
   PR #101. Depth is limited to the EVM's 1024 instead of 128, and its 4096 frame
   limit is gone; a tree over the trace response limit on both providers is
   recorded, not retried.
9. **Evidence holds chain facts only.** Provider names in the hashed evidence made
   an identical event look like a conflict when the answering provider changed.
10. **Rewind is the recovery path.** Rewinding the lane replays internal transfers
    without touching the ordinary cursor, and it handles amounts that differ from
    the route: they go to review. The exact recovery command stays for one payment
    that matches, with the same proof.
11. **Customer receipts are not covered.** The proof worker needs the watched
    addresses, which a route-watching deployment does not have in its environment.
    That is its own task; it is recorded in the TZ.
12. **Fee note.** The checkout already mentioned fees in general; it now says plainly
    that exchanges often deduct theirs. "May not complete" because an
    underpayment tolerance can still settle a shortfall.

## 2026-11-04 — after the second review

13. **A reorg is the lane's business too.** Both reorg paths were open: a commit
    could land after the rewind, and a queued transfer settled from a replaced
    block with nothing to compensate it. `RewindReorg` now lowers the lane cursor
    and marks queued rows by block hash; `CommitInternal` requires the ordinary
    cursor to have reached the range and refuses replaced blocks; first insert at
    settlement refuses a replaced block. The last two also close a path that
    ordinary queued transfers had before this task.
14. **Finality is checked before the block is passed.** The lane reads once, so a
    transfer below the route's `required_finality` would stay unsettled with no
    record. A range with transfers waits until the slower trace provider is past
    it; after 30 minutes it is recorded like any other failure.
15. **Own clients, in the same process.** The lane shares no client with the ordinary
    scan, and `waitForRateLimit` no longer sleeps holding its mutex. It stays in
    the scanner process instead of a new service: an 8 MiB trace limit, one tree
    in memory at a time and a recover in the step bound what it can do there, and
    a separate workload would add one deployment per chain.
16. **Bounds turn attacks into rows.** Pages shrink and spans narrow to one block
    before anything is dropped. Receipts are read once per transaction. A provider
    that alone cannot read something is retried, because a crafted transaction
    fails on both.
17. **A failed platform read holds the lane.** Its last plan may be stale.

## 2026-11-04 — after the third review

18. **A version fences the lane's commit.** Height alone cannot: a lane below the
    fork keeps its height through a reorg, so a read of the old fork passed. Every
    write now raises `version`, a commit carries the version it read, and a rewind
    by hand must raise it too.
19. **A lease, not one lane per replica.** The chart runs two scanner replicas, and
    each would spend the same providers' quota. Double credit was never possible;
    the version and the event identity already prevent it.
20. **Failure costs one range, one provider.** A provider that failed sits out the
    rest of the range, so a hung one cannot use up a step at one timeout per
    wallet. Pages grow back after a shrink and a block has a request budget, so
    frames over the limit cannot pace the lane at one frame per request. A range
    that runs out of time halves, so one heavy block does not cost 32.
21. **Balance reads stop at 8 wallets**, not 16: at 16 a range costs over 200
    requests and a fast chain outruns it.
22. **Alerts must reach someone.** No shipped configuration scraped the scanner.
    The local Prometheus now does, the standalone watchdog reads the open-rows
    gauge, and the alert is on that gauge, because a counter's increase clears
    while the row stays open. A Kubernetes install has to add the scrape.

Known limits, recorded for the owner: a route's `required_finality` above the
chain's current policy can leave a lane transfer unsettled until a rewind; whether
tracers mark a call that fails on insufficient balance with `error` was not
verified on real providers, which matters for wallets outside the balance check;
the lane's trace and balance calls do not pass through the provider health policy.

