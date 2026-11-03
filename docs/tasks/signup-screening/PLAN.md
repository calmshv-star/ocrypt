# signup-screening plan

Goal: Screen every self-service signup against official sanctions lists and the client IP region, and hold any match for an operator decision instead of admitting it

Initial source revision: `16e75c3a9170d1d2b1bba46065f161b4ab0564c1`. Pin the final accepted specification/test
snapshot with the helper before assigning implementation workers.

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md; the owner asked for no questions).
- [x] Tests authored; expected failures inspected: live S1/S2 failed on the missing functions, then on `S.A.` splitting into two words, then on word-set matching; BFF tests failed on the missing routes and contract.
- [x] Dependencies, writer ownership, and common base assigned (single writer, branch `feat/stage2-screening` from `16e75c3`).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated (findings and fixes in DECISIONS.md).
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Assignments

| Worker | Role | Absolute worktree / branch / base | Owned paths | Acceptance IDs | Dependencies |
| --- | --- | --- | --- | --- | --- |

## Evidence

Record command, working directory, revision, dirty state, result, skipped checks,
and a local evidence reference. Never commit credentials or customer records.

- 2026-11-03, `backend/`, uncommitted on `16e75c3`: live suite on fresh `ocrypt_fault_test_20261103_11` (59 migrations + runtime grants), 14/14 including `S1` and `S2_signup_screening`; then `000059` down, up and runtime grants again on the same database succeed.
- 2026-11-03: importer SQL for the OFAC and UN fixtures piped into `psql` on that database loads 7 and 6 entries with the expected keys (for example `commerce rogue`, `персон плеисхолдер`).
- 2026-11-03: `go test ./internal/admin/ ./cmd/admin-api/` pass; `go build -tags noncustodial ./...` pass; admin `tsc -p tsconfig.app.json` clean, vitest 80/80; i18n 3/3; `pytest tests/unit/test_signup_screening_import.py` 8/8.

- 2026-11-03, after review fixes: live suite on fresh `ocrypt_fault_test_20261103_18`, 14/14 including `S1` and `S2`; `000059` down refused with held signups, then down, up and runtime grants after deciding them; importer output via `psql` loads `whole_word` and squashed keys; importer runs on Python 3.9.6 and 3.12; `go vet ./...`, admin and admin-api tests, noncustodial build and edition check pass; `check workflow`, `check web` (one pre-existing locale-switch race in `App.test.tsx` fixed by waiting for the label) and `check backend` pass; `pytest tests` 208 passed; gitleaks clean.

## Resume / next action

PR #126 is open. Next stage-2 item: acceptance presets for the onboarding wizard.
