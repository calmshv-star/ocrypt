# Service unavailable

1. Declare the incident and freeze deployments, migrations, key rotation, and
   manual resolutions.
2. Compare `/healthz` and `/readyz`; inspect only the affected deployment's desired/
   ready replicas, restarts, scheduling events, Secret key presence, database/TLS,
   DNS, and NetworkPolicy changes.
3. If a release caused the outage, restore the recorded prior image digest for that
   deployment. Do not roll the schema backward or restart every workload together.
4. If integrity or database availability is uncertain, stop writers and use the
   [restore procedure](backup-restore.md). Preserve evidence before changing state.
5. Reopen traffic gradually only after readiness, signed/idempotent API checks,
   queue progress, and one controlled callback succeed.

See [critical incidents](critical-incidents.md) for component-specific procedures.
