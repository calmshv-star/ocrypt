#!/usr/bin/env bash
# Proves that the noncustodial (SaaS) edition cannot hold, sign, move or route
# funds: the execution and outbound clients must be absent from every linked
# binary. The same patterns must be present in the full edition, so the check
# cannot pass merely because a type was renamed.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

forbidden=(
  'internal/financialapi\.\(\*Remote(Builder|Signer|Broadcaster)\)\.'
  'internal/financialapi\.\(\*Server\)\.'
  'internal/hostedproviders\.(\(\*HTTPAdapter\)|HTTPAdapter)\.'
  'internal/providerconfig\.\(\*HTTPProber\)\.'
  'internal/admin\.\(\*TrustedFinancialProxy\)\.'
)

cd "$repository_root/backend"
mkdir -p "$work/full" "$work/noncustodial"
go build -trimpath -o "$work/full/" ./cmd/...
go build -trimpath -tags noncustodial -o "$work/noncustodial/" ./cmd/...

for edition in full noncustodial; do
  for binary in "$work/$edition"/*; do
    go tool nm "$binary" | awk '{print $NF}' > "$binary.symbols"
  done
done

failed=0
for pattern in "${forbidden[@]}"; do
  if ! grep -Eqh "$pattern" "$work"/full/*.symbols; then
    echo "self-test failed: full edition no longer links $pattern; update this check" >&2
    failed=1
  fi
  for symbols in "$work"/noncustodial/*.symbols; do
    if grep -Eq "$pattern" "$symbols"; then
      echo "noncustodial $(basename "$symbols" .symbols) links forbidden $pattern" >&2
      failed=1
    fi
  done
done

for stub in financial-api financial-worker; do
  set +e
  "$work/noncustodial/$stub" > "$work/$stub.out" 2>&1
  code=$?
  set -e
  if [[ "$code" -ne 2 ]] || ! grep -q "noncustodial edition" "$work/$stub.out"; then
    echo "noncustodial $stub must be a stub that exits 2 (got $code)" >&2
    failed=1
  fi
done

dockerfile="$repository_root/deploy/docker/Dockerfile.backend"
for required in 'ARG EDITION=full' 'noncustodial) build_tags=noncustodial' 'financial-api|financial-worker) echo "BINARY=${BINARY} is not part of the noncustodial edition"' 'org.ocrypt.edition="${EDITION}"'; do
  if ! grep -qF "$required" "$dockerfile"; then
    echo "Dockerfile.backend is missing: $required" >&2
    failed=1
  fi
done

if [[ "$failed" -ne 0 ]]; then
  exit 1
fi
echo "noncustodial edition: $(ls "$work/noncustodial" | grep -vc '\.symbols$') binaries checked, no custody or hosted-provider clients linked"
