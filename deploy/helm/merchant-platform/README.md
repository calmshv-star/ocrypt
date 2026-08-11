# Merchant Platform Helm chart

The chart deploys isolated API/BFF/control/financial/rate/scanner/provider-health workloads and
one deployment for every queue role. It creates no Secret, database, login role,
public LoadBalancer, signing key, service identity, or implicit platform-outbox claim.

## Admission prerequisites

- Kubernetes 1.30+, a NetworkPolicy-enforcing CNI, managed PostgreSQL with TLS/PITR, and tested restore evidence.
- `bootstrap-roles.sql` applied once by a database security administrator; one login/Secret inheriting each required capability role.
- The migration Job uses a separate non-superuser schema-owner URL. It verifies immutable checksums, applies every ordered `*.up.sql`, then reapplies explicit grants.
- All enabled images set by digest with `global.requireImageDigest=true`; every digest has an SBOM and signed provenance in the release manifest.
- External Secrets/CSI-materialized key files. Key purposes are independent; do not reuse management, credential, webhook, response, state, platform, or financial keys.
- Exact database, health, edge, and HTTPS-egress peers. Empty lists intentionally deny traffic.

## Platform runtime admission and publication

Production scanner values contain `SCANNER_PLATFORM_RUNTIME_JSON`, identifying
one active chain/finality set and its RPC, asset and maintenance snapshot keys.
The scanner reads the atomic heads and records immutable snapshot/fence evidence
with each committed range. Provider `credential_ref` values resolve only to strict
JSON header files below the mounted `SCANNER_SECRET_DIR`. The chart rejects legacy
static chain/genesis/provider values; those exist only in the explicit Compose
development override.

`platform-outbox-publisher` is disabled by default. Before enabling it, create a
dedicated login inheriting only `platform_outbox_publisher`, provision one enabled
`platform_admin_service_identities` UUID with purpose `outbox_publisher`, and put
that UUID in its independent Secret. The workload is single-replica because the
identity is also the lease owner. It has no Service or ingress; a static exec probe
checks its loopback `/healthz` and `/readyz` endpoints.

The destination must be the exact HTTPS `/v1/platform-admin/events` path. Mount
separate pinned CA, client certificate, client key and bearer files, and admit
port 443 only to the destination's explicit NetworkPolicy selector. Application
admission enforces TLS 1.3 mTLS and fenced acknowledgement. Set
`platformOutbox.claimExternalPublication=true` only in the same values layer that
enables this worker, and only after the release manifest contains successful live
destination and database-grant evidence.

## Provider operations health

`provider-health-worker` is disabled by default and uses a dedicated
`merchant_provider_health_worker` database login. Its unique pod identity fences
leases; its mounted credential directory is separate from the scanner. Admit
only the exact HTTPS provider peers on port 443. The worker exposes private
`/healthz`, `/readyz`, and `/metrics` on port 9100. Readiness requires a recent
successful cycle and at least one current, independent, healthy peer group; an
empty claim does not make the pod ready. Metrics contain only closed error/state
categories and aggregate counts, never provider IDs, tenants, endpoints,
addresses, credentials, or raw errors.

On-chain policies come only from approved immutable `rpc_provider` snapshots.
Invalid or rotated stale policy evidence pauses admission. Hosted bindings are
created paused and outbound traffic remains fail-closed until an independently
approved hosted policy version and read-only health evidence exist. Signed
callbacks remain an evidence-ingestion path while paused; pause must never turn
callback evidence into settlement or ledger effects.

## Reconciliation artifacts

Production reconciliation uses one shared S3-compatible HTTPS bucket; the local
directory adapter is intentionally not wired by this chart. Enable provider-side
versioning/Object Lock (or an equivalent immutable-retention policy), deny object
overwrite and deletion, retain access logs, and test restore/verification before
admission. `If-None-Match` in the application is an additional collision guard,
not a substitute for a bucket policy.

Use two independent object-store identities. The API Secret may only read the
`reconciliation/` prefix and receives the Ed25519 public verification-key ring.
The reconciliation worker Secret may only create and inspect objects in that
prefix and receives the private signing key. It must not delete/list unrelated
objects; the API must never receive write permission or the private key. Rotate
signing keys by publishing the new public key in the ring before switching the
worker key ID, and keep old public keys for the entire report-retention period.
The worker health listener is probed directly on the pod and has no ClusterIP
Service or ingress.

API downloads are authenticated before their write deadline is extended. The
chart admits a 900-second bounded download and a 1 GiB object ceiling; the worker
uses the identical byte ceiling plus a one-million-entry ceiling. Keep API and
worker byte limits equal during changes, canary the larger value first, and size
ingress/controller timeouts explicitly so they do not truncate a verified stream.
Worker `/healthz` includes database liveness; `/readyz` also enforces poll
freshness and maximum queue age.

Release admission also requires immutable evidence references for the provider
bucket configuration, both least-privilege policies, and a destructive
immutability test. `scripts/verify-release-manifest.sh` rejects releases unless
versioning, retention/Object Lock, overwrite denial, and the exact read/write
action sets are recorded. Evidence references belong in the release manifest;
credentials never do.

## Merchant event JetStream delivery

`outbox-worker` is disabled in base values and has no Service. PostgreSQL outbox
and event history remain authoritative; merchant `/v1/events` recovery does not
read JetStream. Before enabling the worker, an administrator must provision the
fixed `MERCHANT_EVENTS_V1` / `merchant.events.v1` stream using the checked-in
limits and a cluster with at least three replicas. The runtime may inspect that
configuration but cannot create, update, delete, purge, or delete messages.

Supply only credential-free `tls://` server URLs, TLS 1.3 pinned CA/SNI, a
client certificate/key and exactly one mounted NATS credentials or token file.
The production values select a credentials file. Admit TCP 4222 only to the
exact JetStream namespace/pod selector; do not add HTTPS egress, a Service,
ingress or public broker port. Readiness checks connectivity and exact stream
policy with a bounded deadline, so drift and outages fail closed.

The 1 MiB maximum applies to the canonical full envelope, not only its payload.
The 30-minute duplicate window exceeds the 15-minute maximum retry delay; both
must change together. A release still requires live evidence for mTLS failure,
stream-policy drift, lost acknowledgements, deduplication, backpressure,
post-ack database failure and durable inbox redelivery.

## Temporary JSON-MD5/Form-MD5 compatibility

`legacy-gateway` is disabled in base values and the chart deliberately never
creates its Service or ingress. The historical IP allowlist uses the socket peer,
so production must provide a separately admitted source-preserving listener and
populate `network.ingressFrom` only with that listener. Preserve per-source and
per-trade rate limits at that boundary; forwarded client-IP headers are rejected.

Before enabling, apply 000018, run the identity-backed requester/approver manifest
workflow in `deploy/legacy-compat/README.md`, set an unexpired
`LEGACY_SUNSET_AT`, mount the independent core mTLS and credential-file Secrets,
and provide exact database plus port-443 core/callback NetworkPolicy peers. The
runtime has private readiness/metrics on 9101 and the legacy listener on 8082.
Readiness requires current 000018 evidence and grants, one active admitted config,
safe external secret files, current credential/sunset windows, a fresh worker
cycle, and no stale callback lease. Record live PostgreSQL, edge, callback TLS,
image/SBOM, and rollback evidence in the release manifest; the checked-in defaults
do not claim any of those checks ran.

## Route ownership and TLS

When `ingress.enabled=true`, validation requires edge/database peers, a nonempty
controller-specific management HTTPS configuration, and
`managementTLSVerified=true`. That attestation means the ingress controller is
configured to verify the private management CA and service SNI—not merely to use
HTTPS. `ci-values.yaml` shows ingress-nginx settings; adapt them to your controller.

- `management-api`: public `/v1/public/payment-links/*` and token checkout paths
- `management-api`: Merchant-HMAC `/v1/payment-links/*` and
  `/v1/checkout-sessions`, plus the public capability paths above
- `api`: explicit merchant-HMAC `/v1/*` prefixes in `ingress.coreAPIPaths`
- `admin-api`: `/admin/v1/*` on the admin hostname

No ingress points at platform-admin or financial services. The admin pod is the
only allowed internal caller of management/platform control APIs; both bridges
pin private CAs and require TLS 1.3. The browser never receives internal assertion
keys. When `financial-api` is enabled, chart admission requires explicit private
caller peers; those callers must pin its CA and present the independent operator
assertion. It is never attached to the public ingress.

Capability routes require controller-side per-source request and connection
limits before chart admission. The separate prefixes and explicit core allowlist
prevent longest-prefix routing from leaking checkout or payment-link requests to
the core API. Record the effective production limits and immutable controller
configuration evidence in the release manifest.

## Merchant team/settings private plane

`merchant-settings-api` is a private ClusterIP whose data port is 8447. It
requires TLS 1.3, a client certificate chained to its independent client CA, and
a request-bound assertion key; its plaintext port 9095 is only for pod health.
The session-revocation consumer has no Service and assumes a distinct
`merchant_session_revocation_worker` capability that can execute only its
security-definer consume function. Invitation delivery likewise has no Service
and assumes `merchant_invitation_delivery_worker`, which has only its five
security-definer function capabilities and notifier HTTPS egress. None of these
private runtimes is routed by public ingress.

The admin BFF uses a pinned server CA and exact service SNI, presents an independent
client certificate, and mints a request-bound assertion from server-side session
and project scope. The browser never supplies trusted actor, scope, or permission
headers. The settings API client CA must trust only the admitted BFF identity.

Invitation token keys are mounted as a read-only directory shared by the API and
delivery worker. The strict key-ring JSON points to individual 32-byte key files;
keep old keys until no live job references them. Email invitations remain
explicitly disabled by default, and the delivery workload itself is disabled in
the base values. Enable it only with its unique pod UUID, key ring, notifier HTTPS
origin, bearer file, pinned CA, and an explicit NetworkPolicy peer that permits
port 443 only to the admitted notifier egress path. Bring it to ready before setting
`MERCHANT_SETTINGS_EMAIL_INVITES_ENABLED=true`; API startup/readiness then fails
closed on a stale heartbeat or a stranded key reference.

Reconciliation report create/status/download calls are a separate merchant API
capability and require exactly the `reconciliation:read` client scope. Release
admission records immutable evidence for that scope boundary; event/payment scopes
must not be treated as substitutes.

## Install

```sh
helm upgrade --install merchant deploy/helm/merchant-platform \
  --namespace merchant --create-namespace --values values.production.yaml \
  --atomic --timeout 10m
```

Run `deploy/validate.sh full`, server-side dry-run against the target cluster,
signature/admission verification, and canary smoke tests before promotion. HPA is
opt-in; use queue age/scanner lag/rate freshness metrics when available rather
than CPU alone. Secret rotation requires application-aware overlap/re-encryption
and an explicit rollout.

External platform-config outbox publication remains a release blocker until the
disabled-by-default private publisher, exact destination and all required live
release evidence are explicitly admitted.
