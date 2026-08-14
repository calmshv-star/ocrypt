#!/bin/sh
set -eu

: "${NODIT_ENDPOINT_FILE:?set NODIT_ENDPOINT_FILE to a mode-0600 file containing the Nodit Aptos Mainnet Indexer URL}"

if [ ! -f "$NODIT_ENDPOINT_FILE" ] || [ -L "$NODIT_ENDPOINT_FILE" ]; then
  echo "NODIT_ENDPOINT_FILE must be a regular non-symlink file" >&2
  exit 2
fi

for command_name in curl jq install mktemp stat; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "$command_name is required" >&2
    exit 2
  fi
done

endpoint_file_mode=$(stat -c '%a' "$NODIT_ENDPOINT_FILE" 2>/dev/null || stat -f '%Lp' "$NODIT_ENDPOINT_FILE")
if [ "$endpoint_file_mode" != "600" ]; then
  echo "NODIT_ENDPOINT_FILE must have mode 0600" >&2
  exit 2
fi

nodit_endpoint=$(tr -d '\r\n' < "$NODIT_ENDPOINT_FILE")
if ! printf '%s\n' "$nodit_endpoint" | grep -Eq '^https://([A-Za-z0-9-]+\.)*nodit\.io/[A-Za-z0-9._~/-]+$'; then
  echo "NODIT_ENDPOINT_FILE must contain one HTTPS Nodit endpoint without query parameters" >&2
  exit 2
fi

secret_root=${SCANNER_SECRET_ROOT:-/opt/ocrypt/secrets/scanner}
secret_dir=$secret_root/aptos
secret_target=$secret_dir/nodit-indexer.url
max_version_lag=${APTOS_INDEXER_MAX_VERSION_LAG:-50000}
case "$max_version_lag" in
  '' | *[!0-9]*)
    echo "APTOS_INDEXER_MAX_VERSION_LAG must be an unsigned integer" >&2
    exit 2
    ;;
esac

umask 077
mkdir -p "$secret_dir"
payload_file=$(mktemp "$secret_dir/.aptos-indexer-query.XXXXXX")
official_response=$(mktemp "$secret_dir/.aptos-labs-response.XXXXXX")
nodit_response=$(mktemp "$secret_dir/.nodit-response.XXXXXX")
nodit_curl_config=$(mktemp "$secret_dir/.nodit-curl.XXXXXX")
secret_candidate=$(mktemp "$secret_dir/.nodit-indexer.XXXXXX")
cleanup() {
  rm -f "$payload_file" "$official_response" "$nodit_response" "$nodit_curl_config" "$secret_candidate"
}
trap cleanup EXIT HUP INT TERM

printf '%s' '{"query":"query OcryptAptosIndexerCheck { processor_status(where: {processor: {_eq: \"fungible_asset_processor\"}}) { last_success_version processor } fungible_asset_activities(limit: 1, order_by: [{transaction_version: desc}, {event_index: desc}]) { amount asset_type event_index is_transaction_success owner_address transaction_version type } }"}' > "$payload_file"

if ! curl --silent --show-error --fail-with-body --max-time 20 \
  --request POST --header 'Content-Type: application/json' \
  --data-binary "@$payload_file" --output "$official_response" \
  'https://api.mainnet.aptoslabs.com/v1/graphql'; then
  echo "Aptos Labs indexer validation failed" >&2
  exit 1
fi

# Keep the credential-bearing Nodit URL out of the process list.
printf 'url = "%s"\nsilent\nshow-error\nfail-with-body\nmax-time = 20\nrequest = "POST"\nheader = "Content-Type: application/json"\n' "$nodit_endpoint" > "$nodit_curl_config"
if ! curl --config "$nodit_curl_config" \
  --data-binary "@$payload_file" --output "$nodit_response"; then
  echo "Nodit Aptos indexer validation failed" >&2
  exit 1
fi

validate_response() {
  jq -e '
    (.errors == null or .errors == []) and
    (.data.processor_status | length == 1) and
    (.data.processor_status[0].processor == "fungible_asset_processor") and
    (.data.processor_status[0].last_success_version | tostring | test("^[0-9]+$")) and
    (.data.fungible_asset_activities | length == 1) and
    (.data.fungible_asset_activities[0] |
      has("amount") and has("asset_type") and has("event_index") and
      has("is_transaction_success") and has("owner_address") and
      has("transaction_version") and has("type"))
  ' "$1" >/dev/null
}

if ! validate_response "$official_response"; then
  echo "Aptos Labs returned an incompatible indexer schema" >&2
  exit 1
fi
if ! validate_response "$nodit_response"; then
  echo "Nodit returned an incompatible indexer schema" >&2
  exit 1
fi

official_version=$(jq -r '.data.processor_status[0].last_success_version' "$official_response")
nodit_version=$(jq -r '.data.processor_status[0].last_success_version' "$nodit_response")
if [ "$official_version" -ge "$nodit_version" ]; then
  version_lag=$((official_version - nodit_version))
else
  version_lag=$((nodit_version - official_version))
fi
if [ "$version_lag" -gt "$max_version_lag" ]; then
  echo "Aptos indexers are too far apart to install safely" >&2
  exit 1
fi

printf '%s\n' "$nodit_endpoint" > "$secret_candidate"
install -m 0600 "$secret_candidate" "$secret_target"
echo "Nodit Aptos indexer installed and validated; Aptos deposits remain disabled until the separate activation check passes."
