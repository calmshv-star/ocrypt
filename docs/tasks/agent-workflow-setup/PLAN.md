# Agent workflow setup plan

Source baseline: `779d8ed` (2026-09-30).

## Progress

- [x] Locate the active ocrypt checkout and current source baseline.
- [x] Read contributor guidance; map existing offline and live test boundaries.
- [x] Define the accepted workflow/tooling scope and acceptance matrix.
- [x] Write behavior tests before implementing the helper; initial run fails
  because the helper does not exist, as expected for a new tool.
- [x] Implement durable task context, isolated writer allocation, role profiles,
  and local check profiles.
- [x] Validate behavior and configuration, independently review, and fix findings.
- [x] Integrate/install the workflow in the user's active project context.
- [x] Record the implemented revision, executed checks, and remaining limitations.

## Assignments

| Agent | Ownership | Result |
| --- | --- | --- |
| Primary orchestrator | Workflow files, helper, integration, skill installation | Installed in the existing checkout and projectless chat context |
| `ocrypt_test_map` | Read-only map of existing checks and financial boundaries | Complete; live opt-ins and actual local commands identified |
| `ocrypt_workflow_review` | Integrated workflow and realistic assignment scenarios | Two findings fixed; repeat review found no remaining actionable defects |

## Evidence and next action

The tests-first run of `python3 -m unittest discover -s tests/workflow -v`
failed with `FileNotFoundError: scripts/agent_workflow.py` before the helper was
added. The first behavioral run led to fixture/clean-base corrections.
Independent review identified a snapshot/allocation race and a missing
release-gate dependency. Added failing regression checks, fixed both, and
implemented the test-author-to-implementation handoff within the same task ID.

- 15 targeted checks pass: 13 workflow unit/configuration checks and the two
  existing release-CI contract checks.
- 83 existing offline Python checks pass, with no skips in that selected profile.
- The skill passes the skill-creator validator; Git whitespace checks pass.
- Installed Codex's local `debug prompt-input` includes the repository guidance
  and workflow skill. Role TOMLs parse and match the current documented schema;
  this debug surface does not expose custom-role loading, so that is not claimed.

Implemented source revision: `dee40df`. The existing checkout was fast-forwarded
to that reviewed revision. The projectless chat now has scoped workspace routing
instructions; personal skill discovery links to the repository's skill source.
The installed client's prompt-input check confirms that routing and skill are
visible from the existing chat directory.

Repeated checks in the installed checkout: 13 workflow checks and 83 existing
offline Python checks pass on a clean source tree. Local `.agent-evidence/`
reports retain the exact revision and clean-state flag. This final plan update
changes documentation only.

Go, frontend builds, and live payment/deployment checks were not run for this
tooling-only change; their runnable profiles and prerequisites are documented.
Custom-role host invocation was not exercised; project TOMLs are validated and
the skill provides an explicit-instruction fallback for projectless chats.
No production access, product changes, or live tests are part of this setup.
