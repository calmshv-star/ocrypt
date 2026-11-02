# scanner-watchdog plan

Goal: Alert the operator within minutes when a scanner stops being ready or falls behind

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

- Before implementation (3e51450): collection failed, script missing.
- After: 13 watchdog tests pass (including a real HTTP stalled-scanner probe);
  `check python` (115 tests), `check workflow`, `deploy/validate.sh static`
  pass; the script compiles on Python 3.9 and the example config validates.
- Not run: a live Telegram delivery and a production deployment.

## Resume / next action

Implementation done. Next: independent review; deployment on the standalone
host needs a Telegram bot token and chat ID from the operator.
