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
- [x] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

- `go test ./internal/providers/ -run TestTONWatchedRangeSkipsZeroValueTransfers` before fix: FAIL `ton transfer action failed (malformed_response): invalid amount` (matches production log).
- After fix: `python3 scripts/agent_workflow.py check backend` passed; `check financial` passed (base 1fbfbf1 + working tree).
- Skipped: `pnpm verify` live gate, web checks (no web change), deployment.

## Resume / next action

2026-10-22 (user-approved rollout of TON scanner only): image
`ocrypt-scanner:20261022-ton-zero-da44472` built on the host from
`da444723e2f4117bdd3d7d04e8cdffaf80a4a4ad` with `deploy/docker/Dockerfile.backend`.
`ocrypt-scanner-ton` recreated with the previous container's configuration
(new `WORKER_ID` only); previous container kept stopped as
`ocrypt-scanner-ton-before-da44472` for rollback. `healthz=200`, restarts 0.
Canary cursor advanced from 100504303 to 100505071 within two minutes
(past MC 100504329); about 148k MC blocks remained to catch up at that time,
so readiness stays 503 until the backlog is committed. One transient
SQLSTATE 40001 retry observed. Independent review of the diff not performed.

Rollback: `docker rm -f ocrypt-scanner-ton && docker rename
ocrypt-scanner-ton-before-da44472 ocrypt-scanner-ton && docker start ocrypt-scanner-ton`.
