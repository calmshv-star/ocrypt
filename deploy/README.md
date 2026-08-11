# Deployment assets

- `docker/` builds one hardened image per executable and a separate migration image.
- `postgres/` defines NOLOGIN capability roles, convergent runtime grants, and the checksum migration runner.
- `helm/merchant-platform/` deploys every public, control-plane, financial, scanner, rate, and queue workload independently.
- `gateway/` is the closed local routing reference: management owns every checkout/payment-link path (including its Merchant-HMAC methods), core owns the remaining Merchant-HMAC allowlist, and browsers reach only the admin BFF.
- `observability/` probes all health planes without logging financial identifiers.
- `nats/` declares the fixed JetStream stream, durable reference consumer and
  least-privilege publisher/consumer permission examples.
- `release/manifest.example.json` is deliberately invalid until release evidence replaces every placeholder.

No manifest creates a credential or login role. Control and financial APIs remain
private ClusterIP services. Management and platform bridges use TLS 1.3 with
pinned private CAs; external ingress must also verify management backend CA/SNI.
Capability ingress must enforce per-source request/connection limits and preserve
the recorded non-overlapping route ownership.

Merchant team/settings is a separate private plane. Its health listener is plain
HTTP on the pod only; all data traffic uses TLS 1.3 on port 8447 with mandatory
verified client certificates and a request-bound assertion. The admin BFF pins
the server CA/SNI and presents its independent client certificate; no ingress may
target the private API. Session revocation and invitation delivery use separate
function-only database roles. Invitation delivery alone receives notifier HTTPS
egress and has no Service or ingress.

Retention archival is a separate disabled-by-default data-plane worker. Its
database login inherits only the NOLOGIN/NOBYPASSRLS
`retention_archive_worker` capability: read-only retention evidence plus the
claim, acknowledge, fail, prune, and health functions. It has no direct source
table mutation and cannot call policy or legal-hold control functions. The pod
uses private HTTP probes on `:9099` without a Service, database egress, and an
explicit S3 HTTPS egress peer. Its independent Ed25519 private key and S3
access-key ID, secret key, and session token are mounted as read-only files.

Merchant-event JetStream delivery is also disabled by default. PostgreSQL is
the source of truth and `/v1/events` recovery store. The outbox worker requires
an explicit `OUTBOX_PUBLISHER`, TLS 1.3 with pinned CA/SNI and client
certificate, one external credentials/token file, the exact pre-provisioned
stream policy, and a private TCP-4222 NetworkPolicy peer. It has no Service,
cannot create/delete/purge the stream, and never falls back to HTTPS.

JSON-MD5/Form-MD5 compatibility is a separate temporary `legacy-gateway`, also
disabled by default. It has no Service or shared-ingress route because its IP
allowlist depends on the direct peer address; production must supply a separately
reviewed source-preserving listener with bounded per-IP/trade limits. The pod
uses the function-only `legacy_compat_runtime` role, private health/metrics on
`:9101`, public legacy handling on `:8082`, exact database egress, and explicit
port-443 peers for the core API and admitted callback destinations. Core HMAC,
legacy MD5, and client mTLS secrets are external files. Migration 000018 and the
two-person manifest importer are documented in `legacy-compat/README.md`.

Release admission never treats unavailable infrastructure as a pass. The
manifest requires explicit successful, non-skipped evidence for a target-cluster
Helm server-side dry-run, built-container runtime smoke, live PostgreSQL migration
up/down, real-provider S3 Object Lock/overwrite-denial test, and the exact
platform-outbox destination's TLS 1.3 mutual-authentication/acknowledgement test.
It also records immutable contract evidence that reconciliation report operations
require the dedicated `reconciliation:read` API-client scope.

Retention release evidence additionally records the exact database role/login,
private probe and network exposure, image digest/SBOM/provenance, bucket
versioning, Object Lock `COMPLIANCE` mode, Put/Head version evidence, and denied
overwrite/Delete/List operations. The checked-in IAM and bucket configuration
under `infra/retention/` are review templates only; they do not claim that a
live provider or target cluster was exercised.

JetStream evidence separately proves mTLS and rejected bad credentials, exact
stream limits/replicas/permissions, lost-ack deduplication, outage and
backpressure recovery, database failure after publish ack, and reference inbox
redelivery without double business credit. Declarative JSON and unit tests do
not claim a live broker or cluster.

The scanner's production contract is an atomic `SCANNER_PLATFORM_RUNTIME_JSON`
set of active platform snapshot keys. RPC credentials are optional external file
references beneath `SCANNER_SECRET_DIR`; static chain/genesis/provider settings
exist only in the explicit Compose development override.

External platform event publication is a private, disabled-by-default
`platform-outbox-publisher`. It uses an exact least-privilege database role and
one pre-provisioned `outbox_publisher` service-identity UUID, TLS 1.3 mTLS,
a bearer file, a fixed `/v1/platform-admin/events` destination, explicit port-443
egress, loopback exec probes, and no Service or ingress. A release may claim
publication only when that workload and all live evidence are admitted.
