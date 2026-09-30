# ocrypt task index

Keep a separate plan for each task. The orchestrator owns the task's plan;
workers return evidence instead of concurrently editing coordination files.

| Task | Scope | Plan |
| --- | --- | --- |
| release-20260930-workflow | Publish workflow source/tooling to GitHub and server | [Plan](docs/tasks/release-20260930-workflow/PLAN.md) |
| agent-workflow-setup | Adopt the five-stage agent development workflow | [Plan](docs/tasks/agent-workflow-setup/PLAN.md) |

Create subsequent tasks with `python3 scripts/agent_workflow.py init <slug>
--goal "<requested outcome>"`, then add their plan links here. Read the task's
plan for actual status; this index does not imply runtime deployment.
