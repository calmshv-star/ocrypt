# go-live-request plan

Goal: Let a self-service merchant owner prove its receiving addresses, choose an acceptance preset and ask to go live, and let platform operators admit it without weakening any four-eyes control

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (live `S4` failed on the missing functions and on the held-signup expectation; BFF route drift failed until the contract was written).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-go-live` on top of `feat/stage2-completion`).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated (findings and fixes in DECISIONS.md).
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: live `S4_go_live_request` on fresh disposable PostgreSQL 18.6 databases passes, including the Go repository calls; `000061` down refused with a pending request, then down, up and runtime grants succeed.
- 2026-11-03: `go test ./internal/admin/` (proofs for EVM, Solana, TRON; routes); admin vitest 97/97; `tsc -p tsconfig.app.json`; i18n and raw-copy tests.

- 2026-11-03, after review fixes: live suite (16 cases) on fresh `ocrypt_fault_test_20261103_31`, `S4` covering no change before approval, wallet records, wallet and route admission gates, neutral policy reasons, the management audit, strict over an existing policy and operator-managed merchants; `000061` down twice, up and grants; admin vitest 98/98.

## Resume / next action

Independent review, then the PR. Operators admit wallets with the platform tools described in `deploy/standalone/go-live/README.md`.
