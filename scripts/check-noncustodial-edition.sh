#!/usr/bin/env bash
# Proves that the noncustodial (SaaS) edition cannot hold, sign, move or route
# funds: the execution and outbound clients must be absent from every linked
# binary. Each pattern must also be present in the full edition, so the check
# cannot pass merely because a type was renamed.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

module='github.com/calmshv-star/ocrypt/backend/internal'
forbidden=(
  "$module/financialapi\.(NewRemoteBuilder|\(\*RemoteBuilder\)\.)"
  "$module/financialapi\.(NewRemoteSigner|\(\*RemoteSigner\)\.)"
  "$module/financialapi\.(NewRemoteBroadcaster|\(\*RemoteBroadcaster\)\.)"
  "$module/financialapi\.\(\*remoteClient\)\."
  "$module/financialapi\.(NewServer|\(\*Server\)\.)"
  "$module/hostedproviders\.(NewHTTPAdapter|\(\*HTTPAdapter\)\.|HTTPAdapter\.)"
  "$module/hostedproviders\.\(\*RecoveryWorker\)\."
  "$module/providerconfig\.(NewHTTPProber|\(\*HTTPProber\)\.)"
  "$module/admin\.(NewTrustedFinancialProxy|\(\*TrustedFinancialProxy\)\.)"
)
# Any function or variable (not a type descriptor) from the custody packages.
# The noncustodial edition may link their types through shared persistence
# code, but never their behavior.
custody_code="^$module/(financialapi|treasury|refunds)\."

cd "$repository_root/backend"
commands=()
while IFS= read -r command; do
  commands+=("$command")
done < <(go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./cmd/...)

# Binaries are built one at a time and deleted after their symbol table is
# captured, so the check needs little disk space.
for edition in full noncustodial; do
  mkdir -p "$work/$edition"
  tags=""
  [[ "$edition" == noncustodial ]] && tags=noncustodial
  for command in "${commands[@]}"; do
    name="$(basename "$command")"
    CGO_ENABLED=0 go build -trimpath -tags "$tags" -o "$work/$edition/$name" "$command"
    go tool nm "$work/$edition/$name" | awk '{print $NF}' > "$work/$edition/$name.symbols"
    rm -f "$work/$edition/$name"
  done
done

failed=0
for pattern in "${forbidden[@]}" "$custody_code"; do
  if ! grep -Eqh "$pattern" "$work"/full/*.symbols; then
    echo "self-test failed: full edition no longer links $pattern; update this check" >&2
    failed=1
  fi
  for symbols in "$work"/noncustodial/*.symbols; do
    if grep -E "$pattern" "$symbols" | grep -Evq '^(type:|go:)'; then
      echo "noncustodial $(basename "$symbols" .symbols) links forbidden $pattern" >&2
      failed=1
    fi
  done
done

for stub in financial-api financial-worker; do
  CGO_ENABLED=0 go build -tags noncustodial -o "$work/$stub" "./cmd/$stub"
  set +e
  "$work/$stub" > "$work/$stub.out" 2>&1
  code=$?
  set -e
  if [[ "$code" -ne 2 ]] || ! grep -q "noncustodial edition" "$work/$stub.out"; then
    echo "noncustodial $stub must be a stub that exits 2 (got $code)" >&2
    failed=1
  fi
done

dockerfile="$repository_root/deploy/docker/Dockerfile.backend"
# shellcheck disable=SC2016 # literal Dockerfile text, not shell expansions
for required in 'ARG EDITION=full' 'noncustodial) build_tags=noncustodial' '-tags "${build_tags}"' 'financial-api|financial-worker) echo "BINARY=${BINARY} is not part of the noncustodial edition"' 'org.ocrypt.edition="${EDITION}"'; do
  if ! grep -qF -- "$required" "$dockerfile"; then
    echo "Dockerfile.backend is missing: $required" >&2
    failed=1
  fi
done

if [[ "$failed" -ne 0 ]]; then
  exit 1
fi
echo "noncustodial edition: ${#commands[@]} binaries checked, no custody or hosted-provider code linked"
