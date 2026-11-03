# setup-wizard plan

Goal: The first-run wizard of the TZ: profile, acceptance method, webhook and test payment steps, leading to live mode

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (a wrong label in the first test run).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-wizard` on main).
- [x] Implementation complete and focused checks pass.
- [ ] Integrated diff independently reviewed; relevant checks repeated.
- [ ] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: admin `SetupWizard.test.tsx` 4/4 and `App.test.tsx`; `tsc`; i18n tests.
