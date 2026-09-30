#!/bin/sh
set -eu

# The caller owns PostgreSQL lifecycle. Never use a production endpoint or DB.
if [ "${OCRYPT_RUN_PAYMENT_FAULT_TESTS:-}" != 1 ]; then
  echo "Refusing to run: set OCRYPT_RUN_PAYMENT_FAULT_TESTS=1 for a disposable database." >&2
  exit 2
fi
if [ -z "${OCRYPT_PAYMENT_FAULT_DATABASE_URL:-}" ]; then
  echo "Refusing to run: a separate OCRYPT_PAYMENT_FAULT_DATABASE_URL is required." >&2
  exit 2
fi
repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root/backend"
# This narrow selector cannot activate unrelated opt-in live diagnostics.
exec go test -count=1 -timeout=4m -v ./internal/adapters/postgres \
  -run '^TestPaymentFault(RecoveryPostgres|DatabaseNamesFailClosed)$'
