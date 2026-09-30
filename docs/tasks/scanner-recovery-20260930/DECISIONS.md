# Decisions

- 2026-09-30: Interpret “делай все” as all four specifically proposed tasks,
  preserving established GitHub/server release authorization. No unrelated
  feature or dependency upgrade is part of the request.
- Use project ocrypt-workflow and its task documents rather than duplicating
  development-workflow. Keep currently selected models/permissions.
- One mutable external resource has one operator: only the orchestrator operates
  the server and GitHub. Isolated writers use distinct local Git worktrees.
- Base readyz200 shows the initial failure is the probe invocation, not proof
  that the scanner is broken. Plasma readiness503 requires diagnosis.
- Financial failure tests run against disposable PostgreSQL with synthetic data;
  production observations are read-only and never financial test fixtures.
