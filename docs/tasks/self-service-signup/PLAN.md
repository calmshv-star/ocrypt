# self-service-signup plan

Goal: Let a merchant register, verify email and MFA, and land in their own test-mode cabinet without an invitation

Initial source revision: `ea10f0eb96dd6d7b0d69af67bd1452d9277bf07b`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [x] Dependencies, writer ownership, and common base assigned (single writer, branch `feat/stage2-self-service`).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated (findings and fixes in DECISIONS.md).
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

- 2026-11-02, repo root, base `ea10f0e`: live `S1_self_service_signup_registration` on local PostgreSQL 18.6 passed; `go test ./internal/admin/...` passed.
- 2026-11-03, after review fixes: live suite on fresh `ocrypt_fault_test_20261103_04` (58 migrations + runtime grants) all 13 cases pass, including `S1`; migration 000058 down then up on the same database succeeds; `go test ./internal/admin/... ./cmd/admin-api/... ./internal/management/...` pass; admin vitest 71/71, checkout 18/18, i18n 3/3, `pytest tests` 115 passed.
- 2026-11-03, repo root, uncommitted UI on `1c9e7ca`: `pnpm --filter ./apps/admin exec tsc --noEmit -p .` clean; `pnpm --filter ./apps/admin test` 65/65 passed (8 files, including `Signup.test.tsx` 5/5); `pnpm --filter ./packages/i18n test` 3/3; `pnpm --filter ./apps/admin build` succeeded; `agent_workflow.py check web` passed (`.agent-evidence/20261102T213650Z-web-edb964f1`).

## Resume / next action

Backend, migration, BFF and UI are done and the review findings are fixed. Next: independent review of the integrated
diff, then the stage 2 PR. Live-mode switch and billing follow in the next increment.
