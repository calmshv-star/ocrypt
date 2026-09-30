# Agent development workflow

The default development sequence is interview → specification/plan →
tests before implementation → coordinated subagents → isolated Git worktrees.
Read `AGENTS.md` and the `ocrypt-workflow` skill for the working contract.
The existing payment architecture, test suites, and live release gate remain
authoritative. These tools do not run a background agent service.

## Start and resume a task

Use Python 3.11+ and a clean task-owned source snapshot:

```sh
python3 scripts/agent_workflow.py init payment-replay --goal "Prevent duplicate credit after callback replay"
```

The helper creates `docs/tasks/payment-replay/SPEC.md`, `PLAN.md`, and
`DECISIONS.md`, plus ignored local `state.json`. Fill the interview, explicit
scope/parameters, and acceptance rows before writing implementation code.
Update the root `PLAN.md` index. Context documents are committed; the local
state contains machine-specific worktree paths and is not committed.

Write regression checks from the specification, inspect the expected failures,
and commit the accepted specification/tests. Then pin that snapshot:

```sh
python3 scripts/agent_workflow.py snapshot payment-replay
python3 scripts/agent_workflow.py worker payment-replay backend
python3 scripts/agent_workflow.py worker payment-replay checkout
python3 scripts/agent_workflow.py status payment-replay
```

`worker` creates branch `agent/<task>/<worker>` and a directory beside the
orchestrator checkout under `.ocrypt-agent-worktrees/<task>/<worker>`.
All workers start from the explicitly pinned commit, even if the orchestrator
later advances. The helper refuses duplicate allocations and existing paths.
It does not copy uncommitted changes or delete/stash existing work.

Pass each writer the printed absolute path, role instructions, permitted files,
acceptance IDs, dependency order, and canonical task document path. Coordination
documents belong to the orchestrator. A custom profile does not automatically
allocate a worktree; allocation precedes the spawn.

The author of tests can be assigned an initial worktree from the initial base.
After integrating its tests, advance the same task with `snapshot`; it verifies
that all existing writers are clean and their commits are ancestors of the
orchestrator revision, then moves their records to `completed_workers`. New
writers start from the accepted specification/test revision. Use distinct
worker names across phases. Stop/join writers before advancing; a clean Git
directory alone does not prove an agent is idle. Snapshot and allocation share
a lock, so simultaneous commands cannot overwrite each other's state.
For a short task, the orchestrator may author tests before assigning workers.

Integrate accepted worker commits sequentially using merge/fast-forward, which
preserves their ancestry for the handoff check. Cherry-pick/squash is not
accepted as proof by `snapshot`; keep the original commits. Review
the integrated diff independently and repeat affected checks. Update the task
plan with revision, results, limitations, and next action before stopping.
Worktrees are retained for recovery. Remove a CLI-created worktree only after
its changes are committed/integrated or preserved, using `git worktree remove`
without force. Use `archive_worktree` for Codex-managed worktrees.

## Role profiles

Project profiles are under `.codex/agents/`:

| Profile | Assignment | Model |
| --- | --- | --- |
| `ocrypt_orchestrator` | Specification, ownership, integration, acceptance | GPT-6 Astra |
| `ocrypt_test_author` | Behavior/regression checks before implementation | GPT-6.1 Sol |
| `ocrypt_worker` | Bounded implementation in an assigned worktree | GPT-6.1 Sol |
| `ocrypt_reviewer` | Independent comparison with the specification | GPT-6 Astra |

The current primary agent can serve as orchestrator. Model settings are project
profiles, not global preference changes. Check model availability; document
any substitution rather than promising cost savings. If a spawn interface has
no role selector, pass the profile instructions explicitly. `.codex/config.toml`
caps concurrent subagents at three, subject to the host's actual limit.

Official configuration references:
[subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents),
[worktrees](https://learn.chatgpt.com/docs/environments/git-worktrees), and
[persistent plans](https://developers.openai.com/cookbook/articles/codex_exec_plans).
The process was requested from [the reference video](https://www.youtube.com/watch?v=1tCfHLFnXSw);
these instructions and helpers are an original implementation, not copied code.

## Local check profiles

```sh
python3 scripts/agent_workflow.py check workflow
python3 scripts/agent_workflow.py check python
python3 scripts/agent_workflow.py check backend
python3 scripts/agent_workflow.py check financial
python3 scripts/agent_workflow.py check web
```

| Profile | Actual checks | Prerequisites |
| --- | --- | --- |
| `workflow` | Behavioral unit tests of task/worktree allocation, environment filtering, failure evidence, role syntax | Python 3.11+, Git |
| `python` | Existing unit, security, i18n, release-fixture, SDK/source-parity tests | Dependencies in `tests/requirements.txt` |
| `backend` | `go test ./...`, `go build ./cmd/...`, `go vet ./...`, `go test -race ./...` | Go version from `backend/go.mod`; race compiler |
| `financial` | Money/domain/provider/scanner/application/Postgres/auth/webhook/refund/treasury tests; focused vet/race; retained identity and aggregation fuzz targets | Go and race compiler |
| `web` | `pnpm typecheck`, `pnpm test`, `pnpm build` | Node/pnpm versions from `package.json`; frozen-lockfile installation |

From a clean dependency environment install `pnpm install --frozen-lockfile`
and Python test requirements as needed. For an initial bug's red check, run
the focused test directly using a reviewed local environment; the helper
profiles are broader integration checks, not a requirement to rerun everything
after every line edit. Use an appropriate formatting check for the changed code.

The runner drops inherited credentials, database/target URLs, live opt-ins,
and command-injection environment settings such as `NODE_OPTIONS` and
`PYTHONPATH`. It allows PATH, standard user/temp/locale settings, Go cache/tool
settings, and PNPM_HOME. Package caches can still fetch public dependencies;
this is not a network sandbox or proof that arbitrary new tests are offline.
The runner stops at the first nonzero exit, including missing tools, and
retains the failure rather than reporting success.

Evidence is written to `.agent-evidence/<time>-<profile>-<id>/result.json`
with logs, source revision, and dirty-state flag. Logs are local and ignored.
Their process result is not a semantic acceptance verdict: inspect skipped
tests and outputs and map results to the task's acceptance IDs. A green result
does not prove untested scenarios or a deployed revision.

## Live checks

`pnpm verify` invokes the existing `scripts/release-check.sh` and requires
the release manifest, canary merchant API, separate sandbox API, credentials,
and browser targets. Use a dedicated test environment under the task's
authorization. Never enable native EVM recovery replay or other production
diagnostics just to make an ordinary development check pass.

Static PostgreSQL source-contract checks do not prove database transactions,
failover, migrations, or live outbox/callback delivery. Follow
`docs/TEST_PLAN.md` and `docs/IMPLEMENTATION_STATUS.md` for those gates.
If external prerequisites are absent, finish the local work and state exactly
which runtime/deployment checks remain unverified.
