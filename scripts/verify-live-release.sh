#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-}"
[[ -n "$manifest" && -f "$manifest" ]] || {
  echo "usage: $0 LIVE_RELEASE_MANIFEST.json" >&2
  exit 2
}

for dependency in jq docker curl; do
  command -v "$dependency" >/dev/null 2>&1 || {
    echo "live release gate requires $dependency" >&2
    exit 2
  }
done

allowed_url='^(https://|http://(127[.]0[.]0[.]1|localhost)(:[0-9]+)?/)'
jq -e --arg allowed_url "$allowed_url" '
  (.expected_revision | type == "string" and test("^[a-f0-9]{40}$")) and
  (.containers | type == "array" and length > 0 and all(.[];
    (.name | type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$")) and
    (.max_restarts | type == "number" and floor == . and . >= 0))) and
  (.http_checks | type == "array" and length > 0 and all(.[];
    (.url | type == "string" and test($allowed_url)) and
    (.status | type == "number" and floor == . and . >= 100 and . <= 599) and
    (.timeout_seconds | type == "number" and floor == . and . >= 1 and . <= 30))) and
  (.files | type == "array" and length > 0 and all(.[];
    (.path | type == "string" and startswith("/") and (contains("/../") | not)) and
    (.sha256 | type == "string" and test("^[a-f0-9]{64}$"))))
' "$manifest" >/dev/null || {
  echo "live release manifest is invalid or incomplete" >&2
  exit 1
}

expected_revision="$(jq -er '.expected_revision' "$manifest")"

while IFS= read -r container; do
  name="$(jq -er '.name' <<<"$container")"
  max_restarts="$(jq -er '.max_restarts' <<<"$container")"
  running="$(docker inspect "$name" --format '{{.State.Running}}')"
  actual_revision="$(docker inspect "$name" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')"
  restart_count="$(docker inspect "$name" --format '{{.RestartCount}}')"
  [[ "$running" == "true" ]] || {
    echo "$name is not running" >&2
    exit 1
  }
  [[ "$actual_revision" == "$expected_revision" ]] || {
    echo "$name revision mismatch: expected $expected_revision, got $actual_revision" >&2
    exit 1
  }
  [[ "$restart_count" =~ ^[0-9]+$ && "$restart_count" -le "$max_restarts" ]] || {
    echo "$name restart count $restart_count exceeds $max_restarts" >&2
    exit 1
  }
done < <(jq -c '.containers[]' "$manifest")

while IFS= read -r check; do
  url="$(jq -er '.url' <<<"$check")"
  expected_status="$(jq -er '.status' <<<"$check")"
  timeout_seconds="$(jq -er '.timeout_seconds' <<<"$check")"
  actual_status="$(curl -sS --max-time "$timeout_seconds" -o /dev/null -w '%{http_code}' "$url")"
  [[ "$actual_status" == "$expected_status" ]] || {
    echo "$url returned HTTP $actual_status, expected $expected_status" >&2
    exit 1
  }
done < <(jq -c '.http_checks[]' "$manifest")

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

while IFS= read -r file; do
  path="$(jq -er '.path' <<<"$file")"
  expected_hash="$(jq -er '.sha256' <<<"$file")"
  [[ -s "$path" ]] || {
    echo "release file is missing or empty: $path" >&2
    exit 1
  }
  actual_hash="$(hash_file "$path")"
  [[ "$actual_hash" == "$expected_hash" ]] || {
    echo "release file hash mismatch: $path" >&2
    exit 1
  }
done < <(jq -c '.files[]' "$manifest")

printf 'live release verified: revision=%s containers=%s http=%s files=%s\n' \
  "$expected_revision" \
  "$(jq '.containers | length' "$manifest")" \
  "$(jq '.http_checks | length' "$manifest")" \
  "$(jq '.files | length' "$manifest")"
