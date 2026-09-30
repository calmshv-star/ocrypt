# Agent workflow setup decisions

- 2026-09-30: Base implementation on current `origin/main` revision `779d8ed`
  in a separate worktree; avoid editing the existing chat's checkout mid-task.
- 2026-09-30: Reuse existing acceptance suites and release gates. Add tests for
  new workflow helper behavior only; do not invent payment changes to demo TDD.
- 2026-09-30: Use per-task documents and an ignored local allocation manifest
  instead of a single shared mutable specification/plan.
- 2026-09-30: Drop inherited live opt-ins using an environment allowlist. The
  repository contains explicitly enabled live probes, including native-EVM
  recovery replay; local success must remain distinct from live correctness.
- 2026-09-30: Keep reusable skill source in the repository. Install a personal
  discovery link after integration so the existing projectless ocrypt chat can
  find it. Do not replace global model or permission preferences.
- 2026-09-30: Independent review reproduced a race between snapshot and worker
  allocation. Both operations now share one task lock and reload state under it.
  The test-author handoff advances a task only when original worker commits
  have been integrated and their worktrees are clean; ancestry is retained.
- 2026-09-30: Include the workflow job in the existing mandatory release gate
  and extend its regression contract so a failing workflow check blocks admission.
