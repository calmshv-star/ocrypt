# ocrypt workflow decisions

## 2026-09-30: adopt the requested five-stage workflow

The user requested the interview, specification/plan, tests-first,
orchestrator, and Git-worktree process from
https://www.youtube.com/watch?v=1tCfHLFnXSw for ocrypt.
Repository guidance makes it the default for implementation work.

## Task context is separate from shared project context

Root guidance remains stable. Task requirements, progress, and decisions live
in separate task directories so concurrent assignments do not overwrite each
other. Local worktree paths and test logs are ignored by Git. Existing ADRs
remain authoritative for product architecture.

## Orchestration uses existing Codex capabilities

Project agent profiles define planner, test author, worker, and reviewer roles.
No background service or new model API billing integration is needed.
The planner/reviewer profiles use GPT-6 Astra; focused implementation/test
profiles use GPT-6.1 Sol. Availability is checked at invocation; record any
substitution. Model choice does not establish a cost or speed guarantee.
Do not change the user's global model or permission configuration.

## Local and live evidence remain distinct

Ordinary checks receive an allowlisted environment without production
credentials or inherited live opt-ins. This reduces accidental activation of
the repository's live tests; it is not network isolation. Existing release
gates remain authoritative for live admission. No product behavior or runtime
parameters are changed by installing this workflow.
