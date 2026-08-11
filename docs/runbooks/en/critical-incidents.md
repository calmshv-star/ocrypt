# Critical incident procedures

## Controls for every incident

1. Declare severity, UTC start time, incident commander, operations lead, security
   lead when needed, and a scribe. Use one audited incident channel.
2. Stop deployments, migrations, key rotation, backfills, and manual payment
   resolution. Preserve the last known-good image digests and configuration hashes.
3. Record impact by environment, tenant count, chain/asset, time interval, and state
   transition. Do not paste secrets, full webhook bodies, wallet addresses, or
   customer personal data into chat or tickets.
4. Prefer reversible isolation: remove gateway traffic, scale only the affected
   worker to zero, or deny its egress. Never delete rows, move scanner cursors,
   mark a payment paid, or retry callbacks with ad-hoc SQL.
5. Take timestamped snapshots of health, readiness, deployment events, sanitized
   logs, queue age/counts, provider heads, database health, and recent releases.
6. Require two-person review for customer-balance or entitlement changes. Maintain
   an append-only list of affected IDs in restricted storage and reconcile it later.
7. Close only after backlog recovery, invariant checks, customer remediation,
   alarm validation, evidence retention, and an assigned corrective-action owner.

## API or worker unavailable

1. Compare `/healthz` with `/readyz`. Healthy but not ready usually indicates the
   database or credentials; neither healthy indicates process, scheduling, image,
   memory, or node failure.
2. Check desired/ready replicas, restarts, PDB blocks, pending pods, Secret key
   presence (never print values), database connection saturation, TLS expiry, DNS,
   and NetworkPolicy changes. Confirm the failed component and avoid restarting all
   workloads together.
3. If a new digest/config caused the failure, roll only that deployment back to the
   recorded digest. Do not roll schema backward. If the database is unavailable,
   prevent split-brain and follow the restore guide.
4. Verify signed API requests, idempotent intent creation, readiness, settlement
   queue progress, and one controlled callback before reopening traffic gradually.

## Scanner lag or provider disagreement

The scanner is not release-ready today. These controls apply when it is enabled.

1. Scale scanner and settlement workers to zero; leave callback delivery running
   for already committed events. Do not advance cursors or lower finality to catch up.
2. Capture each provider URL identity, reported chain/genesis, finalized head,
   block hash at the last agreed height, latency, and error class. Never trust a
   single explorer screenshot or customer receipt as authoritative evidence.
3. Remove a disagreeing provider only after proving the remaining independent
   providers meet quorum. Confirm there is no shared upstream behind “independent” URLs.
4. Find the last height whose hash and parent chain agree at quorum. Run an isolated
   overlapping rescan into comparison storage. Reconcile transfer uniqueness,
   asset-contract allowlists, decimals, recipient addresses, logs/internal calls,
   and confirmation thresholds.
5. Resume scanner in a canary shard, then settlement, while watching oldest queue
   age and duplicates. Never auto-credit unmatched or AI-suggested candidates.

## Chain reorganization after observation

1. Pause scanner and settlement for the affected chain. Record the old/new hashes,
   common ancestor, depth, provider quorum, and affected transfer event IDs.
2. Determine whether any event crossed the configured finality boundary and caused
   a ledger or entitlement transition. Keep both chain histories as evidence.
3. Mark orphaned observations through the designed reorg/reversal workflow. Never
   delete observations or edit ledger entries. Financial correction is a new,
   balanced, linked compensating transaction under dual control.
4. Notify affected merchants with stable event IDs and remediation state; do not
   promise irreversibility. Rescan from before the common ancestor and reconcile
   before resuming normal finality.

## Callback delivery outage

1. Confirm settlement remains correct; callbacks are notifications and must not
   become the source of truth. Measure oldest pending delivery and affected endpoints.
2. Inspect callback worker readiness, lease expiry, DNS/HTTPS egress, certificate
   failures, response classes, and envelope-key decryptability without exposing URLs
   containing credentials or response bodies.
3. Fix the boundary, then let the durable queue retry with the original event IDs
   and signatures. Do not synthesize duplicate events or bypass SSRF validation.
4. Rate-limit recovery per endpoint, honor backoff, and verify that a receiver can
   deduplicate repeated delivery. Escalate terminal failures to the merchant.

## Unmatched-payment surge

1. Stop automated/manual matching if the surge exceeds the approved baseline; keep
   ingestion evidence. AI output is advisory only and can never authorize settlement.
2. Segment by chain, asset contract/mint, recipient, transfer mechanism (logs,
   internal calls, token instructions), amount decimals, time window, and route
   version. Check address-pool exhaustion and assignment expiry.
3. Verify node normalization against at least two independent providers and known
   fixtures for smart-contract and exchange withdrawals. Reject screenshots as proof.
4. Repair parsers/routes forward, replay into comparison storage, and require dual
   control for every manual resolution. Idempotency and unique transfer identity
   must be proven before applying a credit.

## Credential or key exposure

1. Treat the value as compromised; do not ask anyone to repost it. Revoke or disable
   the API/RPC/database credential at its authority and isolate the affected workload.
2. Preserve authentication logs, secret access audit, network flow, image digest,
   and deployment history. Identify first/last exposure and possible use.
3. Issue a least-privilege replacement, update the external secret, roll only the
   affected workload, and verify old credential rejection. Rotate dependent signing
   material only with an explicit compatibility plan.
4. Envelope keys cannot be blindly replaced: first implement/execute versioned
   re-encryption, verify every ciphertext, and retain rollback material under dual
   control. If decryptability is uncertain, snapshot before any change.
5. Review unauthorized intents, resolutions, callbacks and ledger transitions; use
   compensating workflows, not history deletion. Notify parties under the approved
   legal and security process.

## Suspected database corruption or split-brain

1. Remove write traffic and stop API/scanner/workers. Fence the suspected primary;
   do not allow two writable primaries or repeatedly restart storage nodes.
2. Preserve provider events, database logs, WAL/archive state, timelines, snapshots,
   and the last known-good transaction time. Open the backup/restore guide.
3. Restore to a new isolated database, reconcile, and use a controlled cutover.
   Never overwrite the only source or improvise row-level repairs during the incident.
