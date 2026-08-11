# ADR 0006: Immutable, progressive, reversible releases

- Status: Accepted
- Date: 2026-08-11

## Context

Tag-only images and all-at-once schema/application updates are not reproducible and
can break settlement invariants across mixed versions. Rollback is unsafe after a
destructive migration or incompatible key rotation.

## Decision

CI builds minimal non-root images, emits SBOM and provenance, scans them, signs
immutable digests, and promotes the same digests between environments. Admission
rejects unsigned, unapproved, tag-only images. Database changes follow expand/
contract: expand schema, deploy compatible code, backfill with checkpoints and
reconciliation, then contract only after the rollback window.

Deploy API, settlement, callback, then scanner as separate guarded stages. Each
stage requires readiness, error-rate and queue-age checks plus synthetic
idempotency/callback smoke tests. Scanner enablement additionally requires provider
quorum, genesis verification, finalized-head continuity, and shadow reconciliation.
Rollback changes image digests only; it never silently reverses data migrations.

## Consequences

Releases take longer and require artifact storage and policy infrastructure. The
extra evidence is necessary for a platform that automatically changes customer
entitlements based on irreversible external transfers.
