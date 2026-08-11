\set ON_ERROR_STOP on

\if :request_phase
SELECT 1/(CASE WHEN request_legacy_compat_config_admission(
  :'request_id'::uuid,
  jsonb_build_object(
    'asset_id', :'asset_id',
    'callback_key_id', 'legacy-callback-v1',
    'chain_id', :'chain_id',
    'config_id', :'config_id'::uuid,
    'core_key_id', :'core_key_id',
    'core_secret_ref', 'legacy-core-v1',
    'credential_id', :'credential_id'::uuid,
    'currency', 'RUB',
    'currency_scale', 2,
    'ip_allowlist', jsonb_build_array(:'ip_allowlist'),
    'legacy_payment_type', 'json_md5',
    'legacy_network', :'legacy_network',
    'legacy_secret_ref', 'legacy-json-md5-v1',
    'legacy_token', :'legacy_token',
    'merchant_id', '0198a100-0000-7000-8000-000000000002'::uuid,
    'pid', :'pid',
    'protocol', 'json_md5',
    'sunset_at', :'sunset_at'::timestamptz,
    'tenant_id', '0198a100-0000-7000-8000-000000000001'::uuid,
    'valid_from', :'valid_from'::timestamptz,
    'valid_until', :'valid_until'::timestamptz
  )
) THEN 1 ELSE 0 END);
\else
SELECT 1/(CASE WHEN approve_legacy_compat_config_admission(
  :'request_id'::uuid,
  jsonb_build_object(
    'asset_id', :'asset_id',
    'callback_key_id', 'legacy-callback-v1',
    'chain_id', :'chain_id',
    'config_id', :'config_id'::uuid,
    'core_key_id', :'core_key_id',
    'core_secret_ref', 'legacy-core-v1',
    'credential_id', :'credential_id'::uuid,
    'currency', 'RUB',
    'currency_scale', 2,
    'ip_allowlist', jsonb_build_array(:'ip_allowlist'),
    'legacy_payment_type', 'json_md5',
    'legacy_network', :'legacy_network',
    'legacy_secret_ref', 'legacy-json-md5-v1',
    'legacy_token', :'legacy_token',
    'merchant_id', '0198a100-0000-7000-8000-000000000002'::uuid,
    'pid', :'pid',
    'protocol', 'json_md5',
    'sunset_at', :'sunset_at'::timestamptz,
    'tenant_id', '0198a100-0000-7000-8000-000000000001'::uuid,
    'valid_from', :'valid_from'::timestamptz,
    'valid_until', :'valid_until'::timestamptz
  )
) THEN 1 ELSE 0 END);
\endif
