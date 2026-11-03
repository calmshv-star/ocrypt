# active-sessions plan

Goal: Let every cabinet user see their own active sessions with a coarse device label, end any other session or all of them, and see how the current session was confirmed

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (service tests failed on the missing methods; contract drift failed until the paths were written).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-sessions` on top of `feat/stage2-go-live`).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: `go test ./internal/admin/`; live `S5_user_sessions` on fresh disposable `ocrypt_fault_test_20261103_33` (62 migrations and runtime grants); admin `Security.test.tsx` 5/5; `tsc -p tsconfig.app.json`.
