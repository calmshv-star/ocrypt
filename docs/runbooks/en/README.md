# Operator runbooks

Use these procedures for production and staging. Start with the common controls in
the critical-incident guide; then follow the matching scenario. The restore guide
is mandatory reading before anyone receives production database recovery access.

- [Critical incidents](critical-incidents.md): service outage, scanner divergence,
  reorganization, callback failure, unmatched-payment surge, and credential exposure.
- [Backup and restore](backup-restore.md): backup controls, isolated restore,
  reconciliation, and cutover.
- [Release and rollback](release-rollback.md): immutable promotion, canary gates,
  and safe rollback.
- [Service unavailable](service-unavailable.md): concise target for availability alerts.

The scanner is disabled by default until its active platform snapshot keys,
provider credential references, egress and immutable evidence path are admitted.
Do not treat deployment scaffolding or a passing health probe as authorization
for automatic chain ingestion.

## Minimum production gate

Require immutable signed image digests, external Secrets, distinct database roles,
enforced NetworkPolicies, TLS to PostgreSQL, verified PITR, a recent restore drill,
on-call ownership, redacted centralized logs, and tested alarms. The application
still lacks native financial/queue metrics, so the provided health alarms alone do
not satisfy the launch gate.
