# Local containers

The base Compose file publishes no host ports. Copy `.env.example` to an untracked
`.env`, provision the listed key/certificate files, then select only the profiles
you need. Compose is for local integration, not production secret management.

```sh
# A DBA applies bootstrap-roles.sql once. The migrator then runs with a distinct
# schema-owner URL and is idempotent by immutable filename/checksum.
docker compose --env-file infra/.env -f infra/compose.yaml --profile migration run --rm migration

# Public API, private control plane and the only local edge listener.
docker compose --env-file infra/.env -f infra/compose.yaml \
  -f infra/compose.dev.yaml --profile application --profile control --profile edge up -d

# Isolated queue workers; verification/scanner/rates/financial/platform runtime are separate opt-ins.
docker compose --env-file infra/.env -f infra/compose.yaml --profile workers up -d

# Production-shaped scanner snapshot admission and private platform publication.
docker compose --env-file infra/.env -f infra/compose.yaml \
  --profile scanner --profile platform-runtime up -d

# Immutable retention archive worker after PostgreSQL and S3 admission.
docker compose --env-file infra/.env -f infra/compose.yaml --profile retention up -d

# Private local JetStream exercise after supplying operator config, TLS and
# credentials and provisioning the development replicas=1 stream explicitly.
docker compose --env-file infra/.env -f infra/compose.yaml --profile jetstream up -d
```

Only the gateway is loopback-published by `compose.dev.yaml`. It verifies the
management service CA/SNI before forwarding payment-link and checkout paths.
Admin, management, platform-admin, and financial listeners are never directly
published. Management and platform certificates need DNS SANs matching the names
used by the BFF/gateway.

The local gateway assigns all `/v1/payment-links*`,
`/v1/public/payment-links*`, and `/v1/checkout-sessions*` requests exclusively to
management-api and applies per-source request/connection limits. Its core API
location is an explicit non-overlapping allowlist. Production ingress must
provide equivalent rate limiting and longest-prefix ownership guarantees.

Each `*_DATABASE_URL` must be a different login inheriting exactly one NOLOGIN
role from `deploy/postgres/bootstrap-roles.sql`. Never use the PostgreSQL bootstrap
superuser as a migration or runtime URL. The rate profile additionally requires
an enabled UUID in `rate_runtime_identities`, global-only targets, and read-only
provider credential files.

The base scanner accepts only `SCANNER_PLATFORM_RUNTIME_JSON` snapshot keys and
optional credential header files below the read-only scanner secret directory.
Legacy chain/genesis/provider variables work only through `compose.dev.yaml`,
which explicitly enables the unsafe development fallback.

Before enabling `platform-runtime`, provision exactly one enabled database
service identity with purpose `outbox_publisher` using
`deploy/postgres/provision-platform-outbox-identity.sql`; its UUID must equal the
worker Secret. The destination is exactly HTTPS `/v1/platform-admin/events`, with
a private CA, independent client certificate/key, and bearer file. The health
listener stays on container loopback and is not published.

Login names may carry a `_login` suffix, but their inherited capability names
are exact. In particular, the reconciliation login inherits only
`merchant_reconciliation_worker`; do not abbreviate or alias that role.

The retention login is `retention_archive_worker_login` in the sample and must
inherit only `retention_archive_worker`. The `retention` profile has no host
port, Service, or control-plane endpoint. It requires `APP_ENV=production`, an
HTTPS S3 origin, versioning and Object Lock `COMPLIANCE`, a dedicated Ed25519
private key, and independent S3 credential files. Follow
`infra/retention/README.md`; the worker IAM may Put/Get and inspect versioning
and Object Lock, but may not Delete or List. Policy and legal-hold management
remain unavailable until a separate control plane is reviewed and admitted.

The `jetstream` profile contains no host port and uses an internal-only network.
It refuses an empty server config and requires independent server TLS/operator
files plus outbox CA, client certificate/key, and NATS credentials file. The
single-node Compose value is development-only; production uses at least three
replicas and the exact policy under `deploy/nats/`. PostgreSQL remains the
recovery truth, so never use this profile to replace `/v1/events` or bypass an
unavailable broker.

The `reports` profile and the API use the same external S3-compatible HTTPS
bucket, but deliberately use different credentials. The API credential is
read-only and receives only the reconciliation public signing key. The worker
credential may create/inspect immutable objects and is the only process that
receives the private signing key. Configure bucket versioning/Object Lock, deny
overwrite and deletion, and restrict both identities to the `reconciliation/`
prefix. Compose does not fall back to a shared host directory, because that
would diverge across replicas and is unsuitable for production.

The sample pins API and worker reconciliation artifacts to the same 1 GiB
ceiling, caps a worker artifact at one million entries, and allows an
authenticated report download up to 900 seconds. Upstream reverse-proxy
timeouts must be at least as large or the client will still see truncation.
