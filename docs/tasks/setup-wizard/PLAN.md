# setup-wizard plan

Goal: The first-run wizard of the TZ: profile, acceptance method, webhook and test payment steps, leading to live mode

## Progress

- [x] Material questions resolved; specification and acceptance matrix recorded (agent decisions in DECISIONS.md).
- [x] Tests authored; expected failures inspected (a wrong label in the first test run).
- [x] Dependencies, writer ownership, and common base assigned (single writer, `feat/stage2-wizard`, rebased on `feat/stage2-account` because migration 000066 follows 000065).
- [x] Implementation complete and focused checks pass.
- [x] Integrated diff independently reviewed; relevant checks repeated.
- [x] Completion evidence and remaining runtime/deployment limits recorded.

## Evidence

- 2026-11-03: admin `SetupWizard.test.tsx` 4/4 and `App.test.tsx`; `tsc`; i18n tests.
- 2026-11-03 review (independent subagent): 2 high, 6 medium, 9 low findings,
  all addressed; see DECISIONS.md 6–12. While fixing H2 the live database
  showed that a self-service owner failed `validate_management_actor`, so no
  owner could create a payment link or an API key at all (migration 000066).
- 2026-11-03 after the fixes: admin `SetupWizard.test.tsx` 10/10 (8 new or
  changed cases failed before the fix), `check web`, `check python` (raw
  copy), `check backend`, `check workflow`, `deploy/validate.sh static`,
  gitleaks clean; Go SDK `TestVerifyWebhookRejectsEmptySecret` failed
  before the fix; `TestAdminProxyDelegatesCreatorScopesForAPIClients`;
  live S1 failed without 000066 (`the self-service owner must pass the
  management actor check for payment_links:write`) and passes with it.
- Mobile width measured in headless Chrome at 375 px: page scroll width 1149
  before `minmax(0, 1fr)`, 375 after.

## Remaining limits

- Not exercised against a running management API: the wizard tests mock the
  client; the proxy test runs the real assertion signer, authenticator and
  scope validation in process.
- Production still needs the 000066 migration applied before self-service
  owners can create payment links or keys.
- Follow-ups: logo and invoice currency need server fields; one-click sandbox
  needs a test credential flow in the cabinet; the verify call returns only
  "dependency unavailable", so the exact challenge failure is not shown;
  the Settings page shows an error instead of a form for a merchant without
  saved settings.
