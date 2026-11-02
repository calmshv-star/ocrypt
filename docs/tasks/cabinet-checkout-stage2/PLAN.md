# cabinet-checkout-stage2 plan

Goal: Plain-language payment statuses, onboarding progress, PWA cabinet and checkout requirements CHK-2, CHK-5, CHK-6 and CHK-8

Initial source revision: `60b306674e1702d8b5677a89a79283b3084a9f02`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded.
- [x] Tests authored; expected failures inspected (or artifact validation defined).
- [x] Dependencies, writer ownership, and common base assigned (single writer, branch `feat/stage2-self-service`).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

- 2026-11-03, repo root, `60b3066`: `go test ./internal/management/ ./internal/httpapi/` pass; checkout SQL prepared and executed as `merchant_management_runtime` on a fresh migrated PostgreSQL 18.6 database; admin vitest 71/71, checkout 19/19, i18n 3/3; `pytest tests` 117 passed; admin `vite build` ships the manifest and icons.
- Not run: a real wallet opening the links on a phone; a real edge proxy setting the region header.

## Resume / next action

Implemented and tested. The integrated diff still needs an independent review before merge.
