# address-screening plan

Goal: Screen payer addresses against the official sanctions lists, warn the merchant about a payment from a listed address, and refuse listed addresses as go-live receiving addresses

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (route drift until the contract was written; importer test caught a wrong Solana fixture; S7 caught colliding fixture transaction ids).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-address-screening` on top of `feat/stage2-notifications`).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated (findings and fixes in DECISIONS.md).
- [x] Completion evidence and remaining runtime/deployment limits recorded (SPEC out of scope; DECISIONS item 11).

## Evidence

- 2026-11-03: live `S7_address_screening` on fresh `ocrypt_fault_test_20261103_48`; importer tests 10/10; `go test ./internal/admin/ ./internal/notifications/`; admin `Sanctions.test.tsx` 3/3; `000064` up, down, up.
- 2026-11-03, after review fixes: live `S7` on fresh `ocrypt_fault_test_20261103_51` (multi-tag load, route window, owner fallback, hourly dedupe, masked references, shrink guard, deposit and go-live refusal, global access); importer tests 12/12; GoLive and Sanctions vitest.
