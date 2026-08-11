# Merchant settings runtime and database capabilities

## `merchant-settings-api`

Private HTTPS `:8447`, TLS 1.3 with required verified client certificate. Private plain health `:9095` exposes `/healthz` and `/readyz` only.

Required environment: `DATABASE_URL`, `MERCHANT_SETTINGS_ASSERTION_KEY_FILE`, `MERCHANT_INVITE_TOKEN_KEY_RING_FILE`, `MERCHANT_SETTINGS_TLS_CERT_FILE`, `MERCHANT_SETTINGS_TLS_KEY_FILE`, `MERCHANT_SETTINGS_CLIENT_CA_FILE`. Optional: `MERCHANT_SETTINGS_HTTP_ADDRESS=:8447`, `MERCHANT_SETTINGS_HEALTH_ADDRESS=:9095`, `MERCHANT_SETTINGS_BODY_LIMIT_BYTES=262144`, `MERCHANT_SETTINGS_EMAIL_INVITES_ENABLED=false`.

The strict key-ring JSON is `{"current_key_id":"2026-08","keys":{"2026-07":"/secret/old","2026-08":"/secret/current"}}`; each file contains exactly 32 raw bytes or raw base64. It is invitation-only key material. With email enabled, startup and readiness require a delivery heartbeat newer than 30 seconds and all key IDs referenced by live jobs.

The `merchant_settings_api_runtime` capability is `NOLOGIN NOBYPASSRLS`. It needs the scoped 000009 table reads/writes used by the repository, global role-catalogue SELECT, selected `admin_users`/`admin_sessions` reads, sequence use for revocation signals, and EXECUTE on `append_merchant_settings_audit` and `merchant_invitation_delivery_keys_admitted`. It has no DELETE/TRUNCATE and no session-revocation or delivery-claim function.

## `merchant-invitation-delivery-worker`

No ingress or data port. Private plain health defaults to `:9097`. Required environment: `DATABASE_URL`, canonical UUID `MERCHANT_INVITATION_DELIVERY_WORKER_ID`, `MERCHANT_INVITE_TOKEN_KEY_RING_FILE`, `MERCHANT_SETTINGS_INVITE_NOTIFIER_URL`, `MERCHANT_SETTINGS_INVITE_NOTIFIER_BEARER_FILE`, `MERCHANT_SETTINGS_INVITE_NOTIFIER_CA_FILE`. Optional bounded values: `MERCHANT_INVITATION_DELIVERY_HEALTH_ADDRESS=:9097`, `POLL_INTERVAL=1s`, `LEASE=30s`, `BASE_BACKOFF=5s`, `MAX_BACKOFF=1h`, `MAX_ATTEMPTS=8`, `BATCH_SIZE=50`.

Its `merchant_invitation_delivery_worker` role is `NOLOGIN NOBYPASSRLS` with EXECUTE only on `merchant_invitation_delivery_keys_admitted(text[])`, `merchant_invitation_delivery_heartbeat(uuid)`, `claim_merchant_invitation_delivery(uuid,integer)`, `complete_merchant_invitation_delivery(uuid,uuid,text)`, and `fail_merchant_invitation_delivery(uuid,uuid,text,integer,integer)`. Security-definer functions own cross-tenant access. No table grant is required.

## `merchant-session-revocation-worker`

No ingress or data port. Private health defaults to `:9096`. Required: `DATABASE_URL`. Optional: `MERCHANT_SESSION_REVOCATION_HEALTH_ADDRESS=:9096`, `POLL_INTERVAL=1s`, `MAX_READY_AGE=10s`, `BATCH_SIZE=100`.

Its `merchant_session_revocation_worker` role is `NOLOGIN NOBYPASSRLS` and receives EXECUTE only on `consume_merchant_session_revocations(integer)`. The function atomically claims signals, revokes active admin sessions, and acknowledges signals. No table grants are required.
