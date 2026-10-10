# same-customer-match-20261011 plan

Goal: Settle small overpayments against equivalent repeated same-customer orders exactly once, fulfill the approved Showy payment, and publish the verified repair

Initial source revision: `92a2caf37a8291611996ab1eecb5ba002251ff87`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [x] Dependencies, writer ownership, and common base assigned.
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |
| tests-first | test author | `work/.ocrypt-agent-worktrees/same-customer-match-20261011/tests-first`; `agent/same-customer-match-20261011/tests-first`; `d48e7d6` | focused application and PostgreSQL tests, harness registration | M1–M4 | accepted specification |
| implementation | worker | `/Users/deniss/Documents/Codex/2026-08-12/new-chat/work/ocrypt/work/.ocrypt-agent-worktrees/same-customer-match-20261011/implementation`; `agent/same-customer-match-20261011/implementation`; `046d0ac` | contextual selector and locked context; ingestion/reconciliation; approved fixture role/clock and saturation/P1 regressions | M1–M4 | tests-first completed and integrated |
| independent-review | reviewer, read-only | integrated `12baccadd08ba0471c9237cec29754ae58bf6b54` | integrated diff and private operator rollout artifacts; no edits | M1–M4, D1 safety | implementation and persisted evidence |

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

Implementation commits `7570a6a` and `12bacca` are integrated. The reviewer found
a cancelled/changed-contender fallback bypass in the first revision. Actual
persisted RED reproduced one match instead of zero. The follow-up retains an
explicit contextual rejection and enters fail-closed review before the reducer;
fresh focused PostgreSQL GREEN passes all five M3/M4 cases (70.289 seconds).

Final clean integrated `12bacca` passes financial tests/vet/race and both fuzz
targets: `.agent-evidence/20261010T204757Z-financial-952b01ec/result.json`.
Clean worker backend profile passes tests/build/vet/all race:
`implementation/.agent-evidence/20261010T204755Z-backend-e74c399e/result.json`.
Independent final review at that exact revision has no outstanding findings and
reran credential-filtered focused application/PostgreSQL packages successfully.

The entire real PostgreSQL fault harness was compiled from clean `12bacca` for
Linux and run against a fresh marked PostgreSQL 18 database over server localhost.
All 56 migrations, production role grants, all eleven persisted subtests and
database-name guards pass (3.25 seconds). This also resolves the earlier baseline
scanner/reorg failures: they were fixture-clock skew caused by WAN round trips,
not product defects; no baseline assertions were weakened.

The bounded journaled operator rollout covers exactly settlement, matching and
the eleven existing proof workers because proofs share the ingestion boundary.
No scanner/rate/callback/resolution service or business setting changes. An
independent reviewer checked the operator script and twelve fake-daemon fault
tests; all pass, including lost mutation responses, unknown identity rejection,
configuration drift, retained originals and restart suppression/restoration.
Read-only live grant checks confirm required route/intent locking permissions.

Publication and runtime are still separate gates at this source snapshot. The
immutable final release record will report current-head GitHub checks, merged
source revision, image digest and live readiness at:
https://github.com/calmshv-star/ocrypt/releases/tag/v2026.10.11-samecustomer
Do not infer live activation from the source commit or local tests alone.

## Resume / next action

Publish the focused reviewed branch, await all current-head release checks, merge
without bypass, then perform the independently reviewed pinned-image rollout.
Verify all affected workers and the individual payment remains single-credit;
publish the version with the actual final activation evidence and retained rollback.
