# solana-recovery-20261009 plan

Goal: Restore Solana scanner progress without losing verified transfers, fulfill the confirmed Showy order once, and publish/deploy the verified fix

Initial source revision: `3fbe419cf0ebb82b90c5068ae06acba4d8222b47`. Pin the final accepted specification/test
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
| tests-first | test-author | `.ocrypt-agent-worktrees/solana-recovery-20261009/tests-first`; `agent/solana-recovery-20261009/tests-first`; base `11bd80a` | `backend/internal/providers/solana_recovery_test.go` | S1, S2, S3 | Integrated signed commit `824a732`; agent finished and worktree clean. |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

- Tests-only revision `824a732`: focused Solana recovery checks fail for unrelated
  malformed token balances, explicit unsupported SPL/Token-2022 transfers, and
  a synthetic temporary wSOL account lifecycle. Failures are behavioral, not
  missing dependencies; existing positive controls and fail-closed negatives pass.
- Production preserves enabled USDC and USDT scanning. The blocking transaction
  creates, initializes, transfers from, and closes an unsupported wSOL account
  within one transaction; that temporary source has no pre/post balance entry.
  Missing source metadata is incorrectly rejected before unsupported-mint filtering.
- F1: verified native SOL proof was accepted through the merchant API; one exact
  finalized match settled and the ordinary Showy callback activated one subscription.
  No manual ledger mutation or forced grant was used. Private identifiers are kept
  outside the repository.
- Production scanner remains at its original cursor with readiness 503; no
  configuration or cursor was changed. Preserve the original container for rollback.

## Resume / next action

Pin the accepted tests and allocate the implementation writer. Repair normalization
without ignoring ambiguous supported-token evidence. The orchestrator prepares a
scanner-only image rollout preserving every runtime setting, cursor, and history.
