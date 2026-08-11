# PostgreSQL backup and restore

## Objective and ownership

Protect payment intents, chain observations, idempotency, matching, ledger, outbox,
and scanner cursor history. The database owner operates backups; the incident
commander approves production recovery; finance/risk signs reconciliation; security
controls restore credentials. Target RPO is five minutes and target RTO is four
hours unless a stricter approved service objective applies.

## Required backup controls

- continuous encrypted WAL archiving and point-in-time recovery;
- daily logical export for independent structural validation;
- encrypted immutable copies in a separate account or failure domain;
- retention that covers operational error and compliance requirements;
- monitored last-success time, WAL gap, copy integrity, storage lock, and expiry;
- separate runtime, migration, backup, and restore identities with audited access;
- quarterly isolated restore drills and annual regional-failure exercise.

A provider “backup enabled” flag is not evidence. Retain configuration, successful
job identity, timestamps, WAL continuity, object checksums, encryption-key recovery,
restore duration, reconciliation result, and approver signatures.

## Pre-restore decision

1. Declare an incident and stop releases/migrations. Determine whether the source
   is unavailable, corrupted, compromised, or merely slow.
2. Fence writes if integrity or split-brain is possible. Record the last known-good
   UTC transaction time and why it is trustworthy. Preserve logs, timelines, WAL,
   snapshots, provider chain evidence, and current image/config digests.
3. Choose PITR for operational corruption/deletion and a logical restore for
   independent validation or selective forensic comparison. Never restore over the
   current database. Create a new isolated target with no application network path.
4. Confirm the restore point precedes the bad action but includes the required
   finalized transfers. A second reviewer approves target, time, backup set, keys,
   and expected data-loss window.

## Restore and validate

1. Restore with provider tooling into a new instance/account. Enforce TLS, private
   networking, encryption, audit logs, and temporary restore credentials. Disable
   application access and outbound callbacks.
2. For a logical archive, use matching PostgreSQL tooling, stop on the first error,
   restore schema and data in one controlled run, and save the tool/version/output.
   Do not use owner or ACL metadata to grant unintended privileges.
3. Apply only forward, release-compatible migrations after recording the restored
   schema version. Never run a down migration against recovered financial data.
4. Run structural checks: PostgreSQL consistency/provider diagnostics, constraints,
   foreign keys, expected extensions/collations, RLS enabled and forced where
   required, and least-privilege grants.
5. Reconcile at minimum:
   - counts and state distributions by tenant/time for intents and routes;
   - uniqueness of transfer identity and active payment matches;
   - balanced ledger transactions (sum of entries equals zero per transaction);
   - idempotency keys and response hashes;
   - unmatched/manual resolution history and approvers;
   - callback/outbox identity, ordering, attempts, and terminal state;
   - scanner cursor/gap continuity against independent finalized chain sources;
   - address assignments and amount reservations without conflicting active leases.
6. Compare the lost RPO interval with immutable chain/provider and application
   evidence. Build an approved replay/remediation list. Never bulk-credit based only
   on amounts, timestamps, screenshots, or AI match scores.

## Cutover

1. Create new least-privilege runtime credentials; never reuse restore credentials.
   Materialize external Secrets and test connectivity from isolated canary pods.
2. Start API with public ingress still closed, then settlement, callback, and scanner
   separately. Scanner stays disabled unless its release gate is complete. Run
   signed/idempotent API smoke tests and controlled queue/callback tests.
3. Open traffic gradually while monitoring readiness, database errors, queue age,
   duplicates, provider agreement, and callback failure. Keep the old database
   fenced and read-only for the evidence/rollback period.
4. Execute only reviewed replay or compensating operations with stable external IDs
   and dual control. Notify merchants of the exact impact window and remediation.
5. Close after RPO/RTO measurement, reconciliation sign-off, backup-on-new-primary
   verification, alarm tests, credential retirement, evidence retention, and a
   scheduled corrective-action review.

## Restore-drill pass criteria

The drill fails if recovery keys are unavailable, WAL has a gap, the target is not
isolated, the schema cannot start the pinned release, any ledger transaction is
unbalanced, unique/idempotency constraints differ, scanner continuity cannot be
proven, the measured objectives are missed without accepted risk, or evidence lacks
two independent approvers.
