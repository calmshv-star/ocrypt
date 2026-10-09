# solana-recovery-20261009 plan

Goal: Restore Solana scanner progress without losing verified transfers, fulfill the confirmed Showy order once, and publish/deploy the verified fix

Initial source revision: `3fbe419cf0ebb82b90c5068ae06acba4d8222b47`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [ ] Tests authored; expected failures inspected (or artifact validation defined).
- [ ] Dependencies, writer ownership, and common base assigned.
- [ ] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

## Resume / next action

Allocate a test-author worktree. Independently reproduce native-only unrelated
token failures and establish current-source versus deployed-binary behavior.
The orchestrator in parallel submits the verified payment through the canonical
protocol, inspects the production scanner configuration, and prepares rollback.
