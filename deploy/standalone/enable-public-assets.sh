#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?set the least-privilege migration database URL}"
: "${PUBLIC_BASE_URL:?set the public HTTPS API origin}"
: "${EVM_DEPOSIT_ADDRESS:?set the operator-owned EVM deposit address}"

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

if ! printf '%s\n' "$EVM_DEPOSIT_ADDRESS" | grep -Eq '^0x[0-9A-Fa-f]{40}$'; then
  echo "EVM_DEPOSIT_ADDRESS must be a 20-byte hexadecimal EVM address" >&2
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec psql "$MIGRATION_DATABASE_URL" -X -v ON_ERROR_STOP=1 \
  -v "rate_gateway_origin=$PUBLIC_BASE_URL" \
  -v "evm_deposit_address=$EVM_DEPOSIT_ADDRESS" \
  -f "$script_dir/enable-public-assets.sql"
