#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?set the least-privilege migration database URL}"
: "${PUBLIC_BASE_URL:?set the public HTTPS API origin}"

case "$PUBLIC_BASE_URL" in
  https://*/* | *\?* | *\#* | *@*)
    echo "PUBLIC_BASE_URL must be an origin such as https://payments.example.com" >&2
    exit 2
    ;;
  https://*) ;;
  *)
    echo "PUBLIC_BASE_URL must use HTTPS" >&2
    exit 2
    ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec psql "$MIGRATION_DATABASE_URL" -X -v ON_ERROR_STOP=1 \
  -v "rate_gateway_origin=$PUBLIC_BASE_URL" \
  -f "$script_dir/bootstrap-rates.sql"
