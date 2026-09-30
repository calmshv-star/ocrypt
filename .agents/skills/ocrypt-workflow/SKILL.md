---
name: ocrypt-workflow
description: Develop or fix ocrypt using a short interview, persisted specification and plan, tests before implementation, coordinated subagents, and separate Git worktrees. Use for ocrypt code changes and resuming its development tasks; read-only reporting does not need the implementation pipeline.
---

# ocrypt workflow

Resolve the repository from the current workspace and verify that its origin
is `calmshv-star/ocrypt`. Read its `AGENTS.md`, `SPEC.md`, and existing task
documents. For a projectless ocrypt chat, consult that chat's `AGENTS.md` for
the checkout path; verify it before acting. Do not select an old copy merely
because its directory is named ocrypt.

## Interview and durable task context

Inspect relevant source/contracts/tests first. Ask unresolved product
questions in one short batch; use available answers and existing authorization.
Persist explicit scope and parameter changes in the task specification.
Create a task with `python3 scripts/agent_workflow.py init <slug> --goal
"<outcome>"` from a clean, committed base. If the checkout is dirty, preserve
the changes and arrange a task-owned snapshot or an appropriate separate
worktree; never silently stash/discard another task's work.

Fill the generated acceptance matrix, task plan, and decisions. Mark unknowns
as unknown instead of inventing requirements. Maintain those documents at
handoffs, material discoveries, compaction, and completion.

## Tests first

For a bug or new behavior, delegate to the test-author role when independent
context helps. Give it the accepted specification and relevant raw artifacts.
Write checks of requested behavior, run and inspect the expected failure, then
implement. Distinguish an expected behavioral failure from missing tools,
broken fixtures, or unrelated baseline failures. Avoid requiring already
correct behavior to fail. Documentation/configuration work uses validation
suited to the artifact. Never modify expectations merely to pass.

## Orchestrator and isolated writers

This skill requests subagents for independent implementation assignments and
independent final review. The primary agent coordinates; use project profiles
in `.codex/agents/` where supported. If the spawn interface lacks custom-role
selection, read the profile and pass its instructions explicitly; use model
overrides only where that interface supports them. Inherit the current parent
permissions. The local cap is three concurrent subagents, within runtime limits.

After committing the accepted task documents and integrating the test-author
commit (if delegated), run
`python3 scripts/agent_workflow.py snapshot <slug>` to pin their common base.
Snapshot advances only when existing writers are clean and their commits are
integrated; it retains their allocation history. Use distinct worker names for
later phases. Do not advance while an assigned writer is still running.
Create each writer with `python3 scripts/agent_workflow.py worker <slug>
<worker-name>`. The helper prints its absolute worktree directory. Include that
directory, branch/base, allowed files, acceptance IDs, dependencies, commands,
and required evidence in its assignment. Writers must use this directory for
every edit/check. Pass the canonical coordination directory separately.
If managed Codex worktree tools are used instead, specify this same explicit
base and record the resulting paths in the task plan.

Keep overlapping work serial. A dependent small change may use one writer;
do not manufacture parallelism. Require commits/diffs and test evidence from
workers. Only the orchestrator edits shared plans and integrates changes.
Do not merge a worker until its acceptance checks and focused review pass.
Preserve recoverable work and follow the lifecycle tool used to create it.

## Integrated verification and delivery

Use the check map in `docs/agent-workflow/README.md`. The helper's `check`
commands exclude inherited live credentials/opt-ins and save local logs tagged
with the tested revision. Use independent review of the integrated diff
against the specification, then rerun affected checks after merging.
Report missing tools, skipped live tests, baseline failures, and unverified
deployment separately. Failed checks go back to the responsible worker with
the failure evidence; stop dependent work for unresolved product questions or
unavailable external prerequisites.

Document the final revision, fulfilled acceptance IDs, check results, review,
and remaining limits. Follow the user's existing publication/deployment scope;
this skill adds no authorization to modify production or move funds.
