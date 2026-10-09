# solana-recovery-20261009 plan

Goal: Restore Solana scanner progress without losing verified transfers, fulfill the confirmed Showy order once, and publish/deploy the verified fix

Initial source revision: `3fbe419cf0ebb82b90c5068ae06acba4d8222b47`. Pin the final accepted specification/test
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
| tests-first | test-author | `.ocrypt-agent-worktrees/solana-recovery-20261009/tests-first`; `agent/solana-recovery-20261009/tests-first`; base `11bd80a` | `backend/internal/providers/solana_recovery_test.go` | S1, S2, S3 | Integrated signed commit `824a732`; agent finished and worktree clean. |
| implementation | worker | `/Users/deniss/Documents/Codex/2026-08-12/new-chat/work/ocrypt/work/.ocrypt-agent-worktrees/solana-recovery-20261009/implementation`; `agent/solana-recovery-20261009/implementation`; base `a74ab17` | `solana.go`, focused provider recovery tests | S1, S2, S3 | Integrated signed commits `4721984`, `6f02c26`, `98868a2`; worker finished and worktree clean. |
| independent-review | reviewer, read-only | Coordinator checkout, integrated `98868a2`; no writer allocation | Integrated diff and private rollout artifact | S1, S2, S3, D1 preparation | Two P1 findings resolved and independently re-reviewed; no remaining actionable findings. |

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
- Integrated repair revision: `98868a2cf6cd2c1110d740f37a60130e54d00217`.
- Independent review found outer-before-inner processing could miss earlier CPI
  initialization; tests-first cross-outer regressions reproduced it. Final code
  interleaves execution while retaining canonical indices, and rejects duplicate
  or out-of-range inner groups. One synthetic fixture gained its missing opaque
  outer instruction; lifecycle and expectations did not change.
- Independent review also found a private rollout crash-window rollback issue;
  exact replacement reconciliation and atomic private journal writes resolve it.
  Three offline rollout fault tests and independent re-review pass.
- On clean integrated `98868a2`, `check financial` passed tests, focused vet/race,
  identity and aggregation fuzz; evidence `.agent-evidence/20261009T062134Z-financial-4b43a853/result.json`.
- On the same revision, `check backend` passed all Go tests/build/vet/race;
  evidence `.agent-evidence/20261009T062135Z-backend-82c7c9e0/result.json`.
- The workflow profile passed 13 tests before implementation. Opt-in disposable
  PostgreSQL/live credential/native EVM diagnostics and the full web/live release
  gate were not enabled; source checks do not establish those unrelated live gates.
- Linux amd64 scanner binary was built with Go 1.26.6 from clean `98868a2`; SHA256
  `5c8cfd766b6b173e505215bc52e1634ecbd41a910de3bf277bd6f7b8cd9eeceb` matched the server copy.
- Scanner-only image `ocrypt-scanner:20261009-solana-recovery-98868a2` is running
  with the original configuration, shard/cursor and retained stopped original.
  It crossed the blocking slot and continues committing the historical backlog.
  The only Docker representation normalization is original OomKillDisable null
  versus recreated false (same default behavior); true and other drift fail.
  A first strict attempt rolled back safely; four private rollout tests and
  independent review cover retry/rollback safety. Readiness awaits backlog catch-up.
- GitHub PR #111 publishes this source. All functional/container/browser checks
  on `bd73a16` passed; supply-chain and consequently release-gate failed because
  unchanged Java Jackson 2.18.10 has four HIGH findings. User explicitly approved
  the bounded patch upgrade and main-branch update after checks.

## Resume / next action

Allocate a separate Java dependency writer from the updated accepted snapshot.
Orchestrator continues lossless scanner catch-up/readiness and canonical replay
verification while the approved dependency patch is independently checked.
Publish final evidence, await all required CI, and merge PR #111 without bypasses.
