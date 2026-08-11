#!/bin/sh
set -eu

: "${REQUESTER_DSN:?REQUESTER_DSN is required}"
: "${APPROVER_DSN:?APPROVER_DSN is required}"
: "${LEGACY_VALID_FROM:?LEGACY_VALID_FROM is required}"
: "${LEGACY_VALID_UNTIL:?LEGACY_VALID_UNTIL is required}"
: "${LEGACY_SUNSET_AT:?LEGACY_SUNSET_AT is required}"
: "${LEGACY_CORE_KEY_ID:?LEGACY_CORE_KEY_ID is required}"

template="$(dirname "$0")/admit-legacy.sql"
ip_allowlist="${LEGACY_IP_ALLOWLIST:-172.18.0.0/16}"

admit() {
  suffix="$1"
  token="$2"
  network="$3"
  chain="$4"
  asset="$5"
  pid="$6"
  config_id="0198a100-0000-7000-8000-0000000000${suffix}"
  credential_id="0198a100-0000-7000-8000-0000000001${suffix}"
  request_id="0198a100-0000-7000-8000-0000000002${suffix}"
  common_args="-v ON_ERROR_STOP=1 -v request_id=$request_id -v config_id=$config_id -v credential_id=$credential_id -v core_key_id=$LEGACY_CORE_KEY_ID -v ip_allowlist=$ip_allowlist -v legacy_token=$token -v legacy_network=$network -v chain_id=$chain -v asset_id=$asset -v pid=$pid -v valid_from=$LEGACY_VALID_FROM -v valid_until=$LEGACY_VALID_UNTIL -v sunset_at=$LEGACY_SUNSET_AT"
  # shellcheck disable=SC2086
  psql "$REQUESTER_DSN" $common_args -v request_phase=true -f "$template" >/dev/null
  # shellcheck disable=SC2086
  psql "$APPROVER_DSN" $common_args -v request_phase=false -f "$template" >/dev/null
}

admit 50 USDT tron     tron:mainnet   usdt-tron    1000-usdt-tron
admit 51 TRX  tron     tron:mainnet   trx-tron     1000-trx-tron
admit 52 SOL  solana   solana:mainnet sol-solana   1000-sol-solana
admit 53 TON  ton      ton:mainnet    ton-ton      1000-ton-ton
admit 54 ETH  ethereum eip155:1       eth-ethereum 1000-eth-ethereum
