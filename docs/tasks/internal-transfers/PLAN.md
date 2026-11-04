# internal-transfers plan

Goal: Credit native transfers that exchanges send through contracts by default, safely, and warn payers about exchange fees

Initial source revision: `06fe91209767185bf427efb765fee5c8b161813a`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (the filter credited a reverted transfer and a delegatecall).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-internal-transfers` on main).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

## Resume / next action

Record completed work, unresolved failures/questions, and the next concrete step.
