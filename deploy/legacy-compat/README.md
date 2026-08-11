# Legacy compatibility admission

Migration `000018_legacy_compatibility` exposes a function-only, identity-backed two-person importer. The schema owner provisions two separate credentialed login identities outside this repository. Each login inherits exactly one NOLOGIN capability:

```sql
GRANT legacy_compat_admission_requester TO legacy_requester_login;
GRANT legacy_compat_admission_approver TO legacy_approver_login;
```

Do not grant either identity membership in the other role, the runtime role, or table privileges. Record the identity-provider/credential-provisioning evidence in the release manifest.

The requester prepares a reviewed copy of `examples/legacy-compat/admission-manifest.example.json`. UUIDs must identify one active tenant/merchant and one active asset on the stated chain. The core HMAC key must belong to that tenant/merchant, be currently valid, and have exactly the required `payments:read`, `payments:write`, and `events:read` capabilities. Secret references name files beneath `LEGACY_SECRET_DIR`; no secret bytes belong in the manifest.

Run the request with the requester login. The database supplies the authoritative request time, derives the canonical JSONB SHA-256 itself, and expires the pending record after 30 minutes:

```sh
psql "$LEGACY_REQUESTER_DATABASE_URL" \
  --set=request_id="00000000-0000-7000-8000-000000000185" \
  --set=manifest="$(jq -c . reviewed-legacy-manifest.json)" \
  --command="SELECT request_legacy_compat_config_admission(:'request_id'::uuid, :'manifest'::jsonb);"
```

The approver obtains the manifest through the approved change-control channel, reviews it independently, and presents the complete same manifest—not only a request ID or caller-supplied hash:

```sh
psql "$LEGACY_APPROVER_DATABASE_URL" \
  --set=request_id="00000000-0000-7000-8000-000000000185" \
  --set=manifest="$(jq -c . independently-reviewed-legacy-manifest.json)" \
  --command="SELECT approve_legacy_compat_config_admission(:'request_id'::uuid, :'manifest'::jsonb);"
```

Both calls must return `t`. Approval fails closed when the identities are not distinct, the manifest differs, the 30-minute request expired, the credential/config window is invalid, the sunset passed, or tenant/merchant/asset/core-key admission no longer holds. There is no supported direct `INSERT` path for runtime, requester, or approver roles.

Before enabling the workload, attach the requester and approver session identities, canonical manifest digest, unexpired sunset, 000018 migration/grant check, mounted-secret evidence, source-preserving edge/rate-limit evidence, and callback TLS/SSRF test to `deploy/release/manifest.example.json`. The repository has not executed these commands against a live PostgreSQL or Kubernetes environment.
