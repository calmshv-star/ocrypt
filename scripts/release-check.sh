#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

required_release_env=(
  RELEASE_MANIFEST
  MERCHANT_BASE_URL
  MERCHANT_KEY_ID
  MERCHANT_SECRET
  SANDBOX_BASE_URL
  SANDBOX_KEY_ID
  SANDBOX_SECRET
  LANDING_E2E_URL
  ADMIN_E2E_URL
  ADMIN_MANAGEMENT_E2E_URL
  UNMATCHED_E2E_URL
  CHECKOUT_E2E_URL
)
for variable in "${required_release_env[@]}"; do
  if [[ -z "${!variable:-}" ]]; then
    echo "Release gate requires $variable" >&2
    exit 1
  fi
done

export REQUIRE_CONTRACT_TARGET=1
export REQUIRE_E2E_TARGETS=1
export REQUIRE_SANDBOX_CONTRACT=1
export RUN_SANDBOX_CONTRACT=1

./scripts/verify-release-manifest.sh "$RELEASE_MANIFEST"

test -z "$(gofmt -l backend)"
(
  cd backend
  go build ./cmd/...
  go test ./...
  go vet ./...
  go test -race ./...
)

pnpm install --frozen-lockfile
python -m pytest -q
pnpm typecheck
pnpm test
pnpm build
PYTHONPATH=sdk/python/src python -m unittest discover -s sdk/python/tests -v
(
  cd sdk/go
  go test ./...
)
./deploy/validate.sh full
if rg -n --hidden --glob '!node_modules/**' --glob '!.git/**' \
  'pza__[A-Za-z0-9_-]{20,}|-----BEGIN (RSA|EC|OPENSSH) PRIVATE KEY-----' .; then
  echo "Release gate found a credential signature" >&2
  exit 1
fi
pnpm --filter @merchant/qa test:e2e
