# rpc-provider-probe plan

Goal: Prove which RPC providers agree on recent and archival eth_getLogs ranges before admitting them

Initial source revision: `d88964a959235292568c22dbf39885e2bbf6c5c4`. Pin the final accepted specification/test
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

- Before implementation (dc35a1b): collection failed, script missing.
- After: 17 probe tests pass, including a real local JSON-RPC round trip.
- Live run 2026-11-03 against five public BSC endpoints: four agree on both
  ranges, bloXroute returns http 403 (table in the deploy README).

## Resume / next action

Implementation done. Next: independent review; operators run it weekly and
before proposing a provider through the admin flow.
