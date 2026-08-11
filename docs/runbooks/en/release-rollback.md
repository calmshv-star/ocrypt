# Release and rollback

## Before promotion

1. Record source revision, migration set, signed API/worker/scanner image digests,
   SBOM/provenance verification, rendered manifest hash, values/config hash, and
   approvers. Tags alone are not deployable identities.
2. Require format, unit, integration, race, migration up/down/up on disposable data,
   OpenAPI/contract, frontend, accessibility/localization, container, Helm render,
   policy, dependency and secret scans. No gate may pass by silently skipping a target.
3. Prove the migration is expand/contract and compatible with both old and new
   binaries. Take/verify a recovery point before schema or key changes.
4. Validate Secret references, database roles, NetworkPolicies, PDB capacity,
   readiness, alerts, and rollback digests in staging using production topology.

## Progressive release

1. Apply expansion migrations. Deploy API canary, then API fleet, settlement,
   callback, and finally scanner. The scanner remains disabled until its dedicated
   release gate passes.
2. At every stage verify readiness, error class, database saturation, oldest queue
   age, duplicate suppression, idempotent API replay, callback signature/retry, and
   ledger/settlement invariants. Hold long enough to cross normal retry windows.
3. Increase traffic/replicas gradually. Record timestamps and evidence for each gate.
   Contract schema only in a later release after the rollback window and backfill sign-off.

## Rollback

1. Stop promotion and identify whether code, config, secret material, policy, or
   schema caused the failure. Isolate only the affected component.
2. Restore the previous admitted image digest and compatible config. Do not run down
   migrations on production financial data and do not revert envelope keys without
   proving ciphertext compatibility.
3. If new code wrote incompatible data, keep writers stopped and use a reviewed
   forward repair or compensating transaction. Preserve original history.
4. Verify readiness and all stage gates before reopening. Document affected events,
   replay needs, RPO/RTO, and corrective actions. A Helm “deployed” status alone is
   not evidence of successful rollback.
