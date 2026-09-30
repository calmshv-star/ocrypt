# ocrypt agent instructions

For implementation work, use the `ocrypt-workflow` skill in
`.agents/skills/ocrypt-workflow/SKILL.md`. This project requests subagent
delegation for independent implementation tasks and independent final review.
The primary agent is the orchestrator. A small dependent fix can remain serial.

Read `CONTRIBUTING.md`, `SPEC.md`, and the relevant task documents under
`docs/tasks/<task>/` before editing. `PLAN.md` is the task index, not a shared
scratchpad. Resume from persisted progress and decisions; inspect Git state
before trusting an old chat or a local path.

## Working contract

- Ask only unresolved questions that affect observable behavior. Preserve the
  user's existing authorization; routine implementation choices are yours.
- Record scope, explicit parameters, non-goals, and acceptance checks before
  implementation. Preserve configured intervals, amounts, tolerances, quorum,
  enabled assets and providers unless the task requires a specific change.
- For new behavior or bug fixes, write the acceptance/regression test first,
  inspect the expected failure, then implement and re-run. Never weaken the
  accepted expectations to obtain a pass. Documentation-only changes use
  relevant validation rather than invented application tests.
- Give each writer a separate Git worktree pinned to the same accepted
  specification/test snapshot. Pass its absolute working directory, permitted
  paths, dependencies, acceptance IDs, and completion evidence explicitly.
  Subagents share a filesystem by default; spawning alone does not isolate them.
- Serialize overlapping edits and merges. Review the integrated result and
  repeat relevant checks after integration, even when each worker passed.
- Report tests actually run, skips, failures, evidence revision, and remaining
  runtime/deployment checks. A code commit is not evidence of deployment.

## Project boundaries

Use exact integer/string money. Preserve tenant scoping, canonical transfer
identity, replay/idempotency, compensating reorg entries, and atomic
settlement/ledger/callback/outbox transactions. AI/receipts are advisory;
independent chain evidence and finality authorize settlement. Product delivery
belongs to the integrator (for example Showy), not the payment core.
Treasury/refund controls and manual-resolution controls are different; follow
their actual contracts. Source/comments/contracts use English; UI copy uses
the six shared localization catalogs.

## Validation

The local helper uses an allowlist of environment variables so inherited live
test opt-ins and credentials are absent:

```sh
python3 scripts/agent_workflow.py check workflow
python3 scripts/agent_workflow.py check python
python3 scripts/agent_workflow.py check financial
python3 scripts/agent_workflow.py check backend
python3 scripts/agent_workflow.py check web
```

Use Python 3.11+, Go from `backend/go.mod`, Node from `package.json`, and the
pinned pnpm version. Install project dependencies as necessary. Select checks
proportionate to the change; the command map is in `docs/agent-workflow/README.md`.
The helper retains local revision-tagged evidence in `.agent-evidence/`.
Environment filtering is not a network sandbox; review newly added tests too.

`pnpm verify` is the existing live release gate. Do not substitute the local
helper for database/API/sandbox/browser evidence. Do not use production as a
test fixture. Follow the current task's authorization for external mutations;
writing workflow rules does not authorize settlement, treasury operations,
configuration changes, or deployment.
