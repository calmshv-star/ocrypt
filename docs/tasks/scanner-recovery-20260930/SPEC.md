# Scanner repair and recovery acceptance

## Request and authorization

On 2026-09-30 the user requested all four proposed tasks: Base healthcheck repair,
Plasma readiness recovery, isolated payment failure testing, and audit of old
GitHub fixes. Existing session authorization includes GitHub publication and
server installation. Only the orchestrator mutates GitHub or server resources.

## Scope and preserved behavior

Base currently has CMD-SHELL healthcheck in a shell-free image, while direct
readyz is HTTP 200. Fix the invocation, preserving the exact image and business
configuration. Plasma currently returns HTTP 503; establish its actual cause,
repair within evidence-supported scope, and demonstrate ongoing cursor progress.
Never jump/reset a cursor, disable token scanning, weaken quorum/finality, alter
rate interval (30 minutes), monetary tolerances, or manually credit payments.
Preserve rollback materials and use normal scanner leases on a necessary restart.

Payment-fault tests must exercise actual PostgreSQL persistence in a separate
throwaway database/runtime: duplicate/retry after lost settlement response,
worker/repository restart, lost callback acknowledgement with idempotent consumer,
and reorg/reinclusion compensation. Existing in-memory/source tests are not live
PostgreSQL evidence. No production transaction or production DB fixture.

Audit open financial/recovery PRs against current main: distinguish already
integrated behavior, remaining code, obsolete assumptions, and actual conflicts.
Do not mass merge dependency upgrades or unrelated product changes. Audit is
read-only; report exact findings before any justified integration.

## Acceptance matrix

| ID | Requirement | Check |
| --- | --- | --- |
| S1 | Base probe executes without a shell | Regression of repair planner; deployed CMD exec health test, direct readyz 200, Docker healthy |
| S2 | Runtime repair preserves scanner settings/image and history | Config invariant tests; before/after image/config comparison; retained cursor and rollback |
| S3 | Plasma cause repaired without missing blocks | Redacted diagnostic evidence; independent providers where relevant; repeated commits and head-lag observations |
| F1 | Retry/restart cannot double-credit | Actual Store + isolated PostgreSQL; exact ledger/match/callback/outbox counts |
| F2 | Lost delivery acknowledgement is recoverable | Actual persisted callback retry with idempotent synthetic consumer; one business effect |
| F3 | Reorg reverses, reinclusion restores exactly once | Actual reorg persistence and immutable compensating ledger assertions |
| G1 | Open financial/recovery fixes classified | PR-head/main diff and actual corresponding source/tests; written audit |
| V1 | Integrated repair reviewed and validated | Independent review, targeted checks, full required CI before merge/publication |

## Prerequisites and limits

Use the installed Go 1.26.6 container and PostgreSQL 18 image on the server for
isolated tests, on a new private internal Docker network with no production
mounts/credentials. Agents may write owned local worktrees but cannot operate
the server/GitHub. Test credentials are synthetic and never production-derived.
If an external provider lacks an admitted alternative, keep history intact and
record the precise blocker instead of claiming recovery.
