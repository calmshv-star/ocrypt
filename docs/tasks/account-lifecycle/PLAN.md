# account-lifecycle plan

Goal: REG-5: access recovery, email change and account deletion with data export for cabinet users

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (route drift until the contract was written; S8 caught a fixture without granted_by and a wrong closed-merchant count).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-account` on main).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: live `S8_account_lifecycle` on fresh `ocrypt_fault_test_20261103_62` (65 migrations and grants); `000065` up, down, up; `go test ./internal/admin/ ./internal/notifications/`; admin `Security.test.tsx` 10/10.
