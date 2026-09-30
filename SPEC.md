# ocrypt development contract

This file governs how changes are developed. Product behavior remains defined
by the versioned contracts, source, tests, and existing ADRs. Read the affected
documents rather than treating this file as a replacement product specification.

## Required workflow

1. Resolve material unknowns through a short interview; record existing answers.
2. Persist a task specification, acceptance matrix, plan, and decisions.
3. For behavior changes, establish failing tests before implementation.
4. Delegate independent work with bounded ownership and explicit criteria.
5. Isolate writers in Git worktrees, integrate, independently review, and verify.

Each task has its own `docs/tasks/<slug>/SPEC.md`, `PLAN.md`, and `DECISIONS.md`.
Acceptance rows identify the behavior, input/fault, expected result, exact
check, and evidence. Financial changes include negative, duplicate/replay,
concurrency, and exact-money cases relevant to the affected boundary.
Business parameter changes must be visible in the task's specification.

## Completion

A task is complete when the accepted behavior is implemented, relevant checks
pass on the integrated revision, review findings are resolved, and its plan
contains the evidence and outstanding limitations. A failed command, missing
tool, skipped runtime check, or inaccessible external environment must be
reported as such. Implementation completion and deployment completion are
separate claims with separate evidence.

## Existing authoritative references

- `CONTRIBUTING.md`: domain boundaries, localization, and quality requirements.
- `contracts/`: API, management, events, and exact-money contracts.
- `docs/TEST_PLAN.md`: acceptance and financial fault schedules.
- `docs/adr/`: architecture decisions.
- `docs/IMPLEMENTATION_STATUS.md`: implemented and live-unverified boundaries.
- `integrations/showy/README.md`: integrator fulfillment boundary.
