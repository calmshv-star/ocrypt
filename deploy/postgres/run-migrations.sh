#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?MIGRATION_DATABASE_URL is required}"
export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-10}"
export PGAPPNAME="merchant-platform-migrator"

work_dir="$(mktemp -d /tmp/merchant-migrations.XXXXXX)"
cleanup() {
  case "$work_dir" in
    /tmp/merchant-migrations.*) rm -rf "$work_dir" ;;
    *) echo "refusing to remove unexpected migration work directory" >&2 ;;
  esac
}
trap cleanup EXIT HUP INT TERM
driver="$work_dir/driver.sql"

if [ -n "${MIGRATION_TEST_ABORT_AFTER_BODY:-}" ] && [ "${MIGRATION_ALLOW_TEST_HOOKS:-}" != 1 ]; then
  echo "migration test hook requires MIGRATION_ALLOW_TEST_HOOKS=1" >&2
  exit 2
fi

# One PostgreSQL session owns a global advisory lock for the whole run. Each
# migration body and its checksum ledger row then commit atomically in their own
# transaction. A killed process releases both the current transaction and lock.
cat >"$driver" <<'SQL'
\set ON_ERROR_STOP on
SELECT pg_advisory_lock(hashtextextended('merchant-platform:schema-migrations',0));
BEGIN;
CREATE TABLE IF NOT EXISTS public.schema_migrations (
  filename text PRIMARY KEY,
  sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
  applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
REVOKE ALL ON public.schema_migrations FROM PUBLIC;
DO $roles$
DECLARE required_role text;
BEGIN
  FOREACH required_role IN ARRAY ARRAY[
    'merchant_api_runtime','merchant_management_runtime','merchant_admin_runtime',
    'platform_admin_runtime','platform_outbox_publisher','merchant_financial_runtime','rate_runtime_worker',
    'merchant_scanner_worker','merchant_settlement_worker','merchant_matching_worker',
    'merchant_callback_worker','merchant_outbox_worker','merchant_resolution_worker',
    'merchant_proof_worker','merchant_plan_worker','merchant_financial_worker',
    'merchant_reconciliation_worker','merchant_settings_api_runtime',
    'merchant_session_revocation_worker','merchant_invitation_delivery_worker',
    'retention_archive_worker'
  ] LOOP
    IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=required_role) THEN
      RAISE EXCEPTION 'required capability role is missing: %',required_role;
    END IF;
  END LOOP;
END $roles$;
COMMIT;
SQL

for migration in /deploy/migrations/*.up.sql; do
  filename="$(basename "$migration")"
  if ! printf '%s\n' "$filename" | grep -Eq '^[0-9]{6}_[a-z0-9_]+\.up\.sql$'; then
    echo "invalid migration filename: $filename" >&2
    exit 1
  fi
  checksum="$(sha256sum "$migration" | awk '{print $1}')"
  case "$checksum" in
    *[!a-f0-9]*|'') echo "invalid migration checksum: $filename" >&2; exit 1 ;;
  esac
  if [ "${#checksum}" -ne 64 ]; then
    echo "invalid migration checksum length: $filename" >&2
    exit 1
  fi

  # Historical migrations used top-level BEGIN/COMMIT while newer files rely
  # on the runner. Strip only the exact top-level marker lines; reject any
  # partial/multiple marker layout before doing so.
  marker_count="$(grep -Ec '^[[:space:]]*(BEGIN|COMMIT);[[:space:]]*$' "$migration" || true)"
  if [ "$marker_count" -ne 0 ] && [ "$marker_count" -ne 2 ]; then
    echo "migration has an unsupported transaction marker layout: $filename" >&2
    exit 1
  fi
  body="$work_dir/$filename"
  if [ "$marker_count" -eq 2 ]; then
    first_marker="$(grep -E '^[[:space:]]*(BEGIN|COMMIT);[[:space:]]*$' "$migration" | sed -n '1p' | tr -d '[:space:]')"
    last_marker="$(grep -E '^[[:space:]]*(BEGIN|COMMIT);[[:space:]]*$' "$migration" | sed -n '$p' | tr -d '[:space:]')"
    if [ "$first_marker" != 'BEGIN;' ] || [ "$last_marker" != 'COMMIT;' ]; then
      echo "migration transaction markers are not an outer BEGIN/COMMIT pair: $filename" >&2
      exit 1
    fi
    sed -E '/^[[:space:]]*(BEGIN|COMMIT);[[:space:]]*$/d' "$migration" >"$body"
  else
    cp "$migration" "$body"
  fi

  abort_after_body=false
  if [ "${MIGRATION_TEST_ABORT_AFTER_BODY:-}" = "$filename" ]; then
    abort_after_body=true
  fi
  cat >>"$driver" <<SQL
DO \$migration_check\$
BEGIN
  IF EXISTS(SELECT 1 FROM public.schema_migrations WHERE filename='$filename' AND sha256<>'$checksum') THEN
    RAISE EXCEPTION 'immutable migration checksum mismatch: $filename';
  END IF;
END \$migration_check\$;
SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE filename='$filename') AS migration_already_applied \gset
\if :migration_already_applied
\echo 'already applied: $filename'
\else
BEGIN;
\i '$body'
\if $abort_after_body
\echo 'test abort after migration body: $filename'
SELECT 1/0;
\endif
INSERT INTO public.schema_migrations(filename,sha256) VALUES('$filename','$checksum');
COMMIT;
\endif
SQL
done

cat >>"$driver" <<'SQL'
BEGIN;
\i '/deploy/runtime-grants.sql'
COMMIT;
SELECT pg_advisory_unlock(hashtextextended('merchant-platform:schema-migrations',0));
SQL

psql "$MIGRATION_DATABASE_URL" -f "$driver"
