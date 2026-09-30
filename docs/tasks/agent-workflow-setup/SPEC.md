# Agent workflow setup specification

## Requested outcome and interview

On 2026-09-30 the user requested the proposed five-stage workflow for ocrypt:
interview, persistent specification/plan, tests before implementation,
orchestration, and separate Git worktrees. No unresolved product question is
needed for this tooling setup. Product behavior changes are outside its scope.

## Scope

- Repository instructions and a discoverable ocrypt-specific skill.
- Persistent project/task requirements, plan, and decisions.
- Four project-scoped role profiles and bounded concurrency.
- A helper to initialize tasks, pin a base, allocate isolated writers, and
  run existing local check profiles with revision-tagged evidence.
- Behavior tests before helper implementation, independent review, and CI.
- Install guidance in the existing projectless ocrypt chat workspace so the
  workflow can be discovered there as well as from the repository.

## Acceptance matrix

| ID | Input/fault | Expected result | Check |
| --- | --- | --- | --- |
| AC1 | Two task IDs; existing/invalid task ID | Independent context; no overwrite or traversal | Workflow unit tests |
| AC2 | Dirty base | Refuse snapshot creation without losing changes | Workflow unit tests |
| AC3 | Two worker allocations, then edit one | Same pinned base; isolated edits; duplicates refused | Git fixture tests |
| AC4 | Corrupt base or invalid worker name | Stop before creating worktrees | Git fixture tests |
| AC5 | Inherited live flags/DB secrets | Check environment excludes them | Environment behavior test |
| AC6 | Missing check tool or failed check | Stop, retain failure and tested revision | Evidence behavior test |
| AC7 | Agent/skill definitions | Parse and expose distinct valid project roles | TOML/skill validation and review |
| AC8 | Future development change | Instructions require acceptance-first tests and integrated independent review | Independent scenario review |

## Non-goals and limits

No settlement, ledger, rates, providers, intervals, server settings, business
logic, migrations, or deployment changes. No production access is required.
Offline workflow validation is not evidence of a live payment or deployment.
