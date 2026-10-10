# same-customer-match-20261011 plan

Goal: Settle small overpayments against equivalent repeated same-customer orders exactly once, fulfill the approved Showy payment, and publish the verified repair

Initial source revision: `92a2caf37a8291611996ab1eecb5ba002251ff87`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [ ] Dependencies, writer ownership, and common base assigned.
- [ ] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |
| tests-first | test author | `work/.ocrypt-agent-worktrees/same-customer-match-20261011/tests-first`; `agent/same-customer-match-20261011/tests-first`; `d48e7d6` | focused application and PostgreSQL tests, harness registration | M1–M4 | accepted specification |
| implementation | worker | helper-allocated isolated worktree, pinned accepted test snapshot | contextual candidate selector; authoritative database context; ingestion and reconciliation integration; fixture role correction only | M1–M4 | tests-first completed and integrated |

## Evidence

Base is current origin/main `92a2caf37a8291611996ab1eecb5ba002251ff87`; fetched and
verified clean before starting. The previous Solana task/branches are preserved.
Read-only production evidence confirms a score-100/100 overpayment tie with empty,
equal metadata and the same authenticated customer/economics. No match or manual
resolution existed at the initial check. Preserve private identifiers outside Git.

F1 completed: the owner-approved guarded request entered the existing manual
verification queue with no shortfall/late/cross-asset acceptance. Independent
verification succeeded on the first attempt; the core recorded one finalized match
and a paid-overpayment callback. Showy processed the callback, activated exactly
one ordinary one-month subscription for 599 RUB, and reports active access. All
individual customer/transaction identifiers remain in private operational artifacts.

Workflow profile passed 13 tests on specification revision d48e7d6; evidence
`.agent-evidence/20261010T202345Z-workflow-8aa8d803/result.json`.

Signed test commits `3ac1456` and `badb755` are integrated. Pure selector tests
are compilation RED against the intentionally absent contextual API. Actual
persisted behavior RED is established on a fresh explicitly marked disposable
PostgreSQL 18 database: all 56 migrations applied, the same-customer tie ingests,
but its strictly closest route receives zero automated matching jobs instead of
one. Focused M3/M4 run exited 1 after 65.22 seconds. Rollback/context cases stop at
that same missing-job prerequisite, not falsely claimed as independent red proof.
The insufficient-finality fixture revealed an existing test-author role mistake:
ingestion must use merchant_settlement_worker and reconciliation must use
merchant_matching_worker. Correct that fixture without changing assertions.

The broader baseline disposable run also failed pre-existing scanner/reorg lease
cases; distinguish those from this feature and investigate before release.
Test writer finished clean; no production access or product implementation.

## Resume / next action

Pin the integrated accepted-test snapshot, assign the isolated implementation,
verify focused persisted GREEN and broader profiles, then obtain independent
review. Publish only after current-head required GitHub checks; bounded runtime
replacement of the affected settlement/matching workers preserves configuration.
