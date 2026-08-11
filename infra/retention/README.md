# Retention archive admission

These files are reviewable examples, not evidence that a provider was changed.
Before enabling `retention-worker`, an operator must provision a dedicated S3
bucket with versioning enabled and Object Lock enabled in `COMPLIANCE` mode.
Object Lock must be enabled when the bucket is created if the chosen provider
cannot enable it later.

Replace the example bucket ARN while preserving the action boundary. The worker
may only `PutObject`, `GetObject`, `GetObjectLockConfiguration`, and
`GetBucketVersioning`. It is explicitly denied object deletion, version
deletion, and bucket listing. Its HTTPS origin must be the exact origin placed
in `RETENTION_S3_ENDPOINT`; redirects and proxy environment variables are not
used by the runtime.

Provision an independent Ed25519 private key plus access-key ID, secret access
key, and session token files. Mount all four read-only. The database login must
inherit only the NOLOGIN/NOBYPASSRLS `retention_archive_worker` capability role.
Never put those values in Helm values, Compose environment values, or release
evidence.

Admission requires retained evidence for the bucket versioning and Object Lock
configuration, the exact IAM policy, an immutable Put/Head check including the
returned version ID, overwrite rejection, and Delete/List denial. This
repository does not run those live provider operations automatically.

Policy and legal-hold control remain deliberately unavailable: do not grant
`create_retention_policy_version`, `create_retention_legal_hold`, or
`release_retention_legal_hold` to the worker. A separately reviewed control
plane is required before operators can create policies or legal holds.
