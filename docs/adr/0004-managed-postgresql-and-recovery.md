# ADR 0004: Managed PostgreSQL with point-in-time recovery

- Status: Accepted
- Date: 2026-08-11

## Context

Payment state, idempotency records, scanner cursors, and outbox claims require
transactional recovery. A `pg_dump` CronJob in the application namespace does not
provide continuous recovery, independent credentials, immutable retention, or a
credible regional-disaster story.

## Decision

Production PostgreSQL is operated outside the application chart by a managed
service or mature operator. It provides synchronous durability appropriate to the
region, encrypted transport/storage, continuous WAL archiving, point-in-time
recovery, daily logical exports for independent validation, cross-account or
cross-region immutable copies, and monitored backup freshness. Migration, runtime,
backup, and restore identities are distinct.

Target RPO is at most five minutes and target service RTO is four hours unless the
merchant risk assessment sets stricter values. Every quarter, restore to an
isolated environment and reconcile counts, state constraints, idempotency keys,
transfer uniqueness, outbox ordering, and scanner cursor continuity before signing
the evidence. Production recovery always restores to a new database and cuts over;
it never overwrites the only known-good copy.

## Consequences

The chart does not create PostgreSQL or backup credentials. Provider configuration,
retention locks, restore automation and evidence are required launch deliverables.
PITR can replay an incorrect manual action, so reconciliation and an audited repair
plan remain mandatory.
