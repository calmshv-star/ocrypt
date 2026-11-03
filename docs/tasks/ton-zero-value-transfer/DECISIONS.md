# ton-zero-value-transfer decisions

Goal: TON scanner skips successful zero-value transfers instead of failing the range

For each material decision record date, accepted choice, reason, evidence,
and effect on scope or acceptance criteria. Separate user requirements from
agent assumptions. Amend the specification when accepted behavior changes.

## 2026-10-22 — skip zero-value transfers

- User requirement: fix the stalled scanners autonomously using open solutions.
- Agent decision: a well-formed zero amount is skipped (`continue`), not an
  error. Reason: it cannot fund a route; failing closed on it blocks every later
  payment for that chain forever. Strict parsing of malformed amounts is kept.
- Serial implementation by the orchestrator (small dependent fix); no worker
  worktrees allocated.
