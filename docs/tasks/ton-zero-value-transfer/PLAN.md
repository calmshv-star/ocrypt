# ton-zero-value-transfer plan

Goal: TON scanner skips successful zero-value transfers instead of failing the range

Initial source revision: `1fbfbf111d22ab6c12eb3235d951b8195b5080e1`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [x] Dependencies, writer ownership, and common base assigned (serial, orchestrator only).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

- `go test ./internal/providers/ -run TestTONWatchedRangeSkipsZeroValueTransfers` before fix: FAIL `ton transfer action failed (malformed_response): invalid amount` (matches production log).
- After fix: `python3 scripts/agent_workflow.py check backend` passed; `check financial` passed (base 1fbfbf1 + working tree).
- Skipped: `pnpm verify` live gate, web checks (no web change), deployment.

## Resume / next action

Fix and tests done. Next: independent review, publish, build image, and roll
out `ocrypt-scanner-ton` only after explicit deployment approval; then confirm
the canary cursor moves past MC 100504329.
