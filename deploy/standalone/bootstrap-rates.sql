\set ON_ERROR_STOP on

BEGIN;

INSERT INTO rate_runtime_identities(id,name,purpose,enabled)
VALUES('0198a100-0000-7000-8000-000000000050','ocrypt-rate-worker','rate_collection',true)
ON CONFLICT(id) DO UPDATE SET enabled=true;

CREATE OR REPLACE FUNCTION pg_temp.activate_rate_snapshot(
  requested_kind platform_config_kind,
  requested_key text,
  requested_payload jsonb
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  request_id uuid := uuidv7();
  snapshot_id uuid := uuidv7();
  activation_id uuid := uuidv7();
  requested_scope uuid := platform_scope_uuid(NULL);
  requester uuid := '0198a100-0000-7000-8000-000000000003';
  approver uuid := '0198a100-0000-7000-8000-000000000004';
  now_at timestamptz := clock_timestamp();
BEGIN
  IF EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=requested_scope AND kind=requested_kind AND logical_key=requested_key) THEN
    RETURN NULL;
  END IF;
  INSERT INTO platform_config_change_requests(
    id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
    requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
    created_at,updated_at,row_version)
  VALUES(request_id,requested_scope,requested_kind,requested_key,1,0,requested_payload,
    digest(requested_payload::text,'sha256'),'active','Initial independent RUB rate admission',
    requester,approver,approver,approver,now_at,now_at,now_at,now_at,now_at,now_at,4);
  INSERT INTO platform_config_snapshots(
    id,scope_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
  VALUES(snapshot_id,requested_scope,request_id,requested_kind,requested_key,1,requested_payload,
    digest(requested_payload::text,'sha256'),approver,now_at);
  INSERT INTO platform_config_heads(scope_id,kind,logical_key,snapshot_id,fence_token,updated_at)
  VALUES(requested_scope,requested_kind,requested_key,snapshot_id,1,now_at);
  INSERT INTO platform_config_activations(id,scope_id,kind,logical_key,snapshot_id,fence_token,activation_type,actor_id,occurred_at)
  VALUES(activation_id,requested_scope,requested_kind,requested_key,snapshot_id,1,'activate',approver,now_at);
  RETURN snapshot_id;
END $$;

SELECT pg_temp.activate_rate_snapshot(
  'rate_source', asset_id||'-rub-coingecko',
  jsonb_build_object(
    'provider_ref','coingecko-keyless-public',
    'endpoint','https://api.pay.example.com/v1/public/rates/coingecko/'||asset_id,
    'base_asset',asset_id,'quote_asset','RUB','max_age_seconds',900,
    'timeout_ms',10000,'max_response_bytes',4096
  )
) FROM (VALUES('eth-ethereum'),('sol-solana'),('ton-ton'),('trx-tron'),('usdt-tron')) AS assets(asset_id);

SELECT pg_temp.activate_rate_snapshot(
  'rate_source', asset_id||'-rub-coinpaprika',
  jsonb_build_object(
    'provider_ref','coinpaprika-keyless-public',
    'endpoint','https://api.pay.example.com/v1/public/rates/coinpaprika/'||asset_id,
    'base_asset',asset_id,'quote_asset','RUB','max_age_seconds',900,
    'timeout_ms',10000,'max_response_bytes',4096
  )
) FROM (VALUES('eth-ethereum'),('sol-solana'),('ton-ton'),('trx-tron'),('usdt-tron')) AS assets(asset_id);

SELECT pg_temp.activate_rate_snapshot(
  'rate_policy', 'rate-'||asset_id||'-rub',
  jsonb_build_object(
    'base_asset',asset_id,'quote_asset','RUB',
    'sources',jsonb_build_array(asset_id||'-rub-coingecko',asset_id||'-rub-coinpaprika'),
    'quorum',2,'max_age_seconds',900,'max_spread_bps',300,
    'future_tolerance_seconds',10,'poll_interval_seconds',30
  )
) FROM (VALUES('eth-ethereum'),('sol-solana'),('ton-ton'),('trx-tron'),('usdt-tron')) AS assets(asset_id);

COMMIT;
