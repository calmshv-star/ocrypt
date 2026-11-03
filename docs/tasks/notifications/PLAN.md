# notifications plan

Goal: Email and Telegram notifications for cabinet users: chosen payment and webhook events per merchant, and an alert for every new sign-in

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (route drift until the contract was written; live S6 failed on fixtures and one wrong locale expectation before passing).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-notifications` on top of `feat/stage2-sessions`).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: full live suite (18 cases) on fresh `ocrypt_fault_test_20261103_44` with 63 migrations and runtime grants; `000063` down, up and grants; `go test ./internal/notifications/ ./internal/admin/`; admin `Notifications.test.tsx` 3/3; `deploy/validate.sh static`.
