\set ON_ERROR_STOP on

-- Required psql variables include api credentials plus one operator-owned
-- deposit address per enabled chain. This template intentionally ships no
-- real wallet addresses and must never be used with placeholder recipients.

BEGIN;

INSERT INTO tenants(id,public_id,name,status,default_timezone,default_locale,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000001','tn_example_live','Example tenant','active','UTC','en',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO merchants(id,tenant_id,code,display_name,environment,settlement_currency,status,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000002','0198a100-0000-7000-8000-000000000001','example','Example merchant','live','RUB','active',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO admin_users(id,oidc_issuer,oidc_subject,display_name,email,status,created_at,updated_at)
VALUES
('0198a100-0000-7000-8000-000000000003','https://bootstrap.ocrypt.internal','bootstrap-requester','Bootstrap requester',NULL,'active',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000004','https://bootstrap.ocrypt.internal','bootstrap-approver','Bootstrap approver',NULL,'active',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO chains(id,family,network_name,status,required_confirmations,maximum_reorg_depth,transaction_url_template,created_at,updated_at)
VALUES
('eip155:1','evm','Ethereum Mainnet','active',3,64,'https://etherscan.io/tx/{tx}',clock_timestamp(),clock_timestamp()),
('solana:mainnet','solana','Solana Mainnet','active',1,64,'https://solscan.io/tx/{tx}',clock_timestamp(),clock_timestamp()),
('ton:mainnet','ton','TON Mainnet','active',1,64,'https://tonviewer.com/transaction/{tx}',clock_timestamp(),clock_timestamp()),
('tron:mainnet','tron','TRON Mainnet','active',1,64,'https://tronscan.org/#/transaction/{tx}',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO assets(id,chain_id,symbol,name,kind,canonical_contract,decimals,status,created_at,updated_at)
VALUES
('eth-ethereum','eip155:1','ETH','Ether','native','native',18,'active',clock_timestamp(),clock_timestamp()),
('sol-solana','solana:mainnet','SOL','Solana','native','native',9,'active',clock_timestamp(),clock_timestamp()),
('ton-ton','ton:mainnet','TON','Toncoin','native','native',9,'active',clock_timestamp(),clock_timestamp()),
('trx-tron','tron:mainnet','TRX','TRON','native','native',6,'active',clock_timestamp(),clock_timestamp()),
('usdt-tron','tron:mainnet','USDT','Tether USD','fungible_token','TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t',6,'active',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO wallets(id,tenant_id,merchant_id,chain_id,custody_mode,status,created_at,updated_at)
VALUES
('0198a100-0000-7000-8000-000000000010','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002','eip155:1','watch_only','active',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000011','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002','solana:mainnet','watch_only','active',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000012','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002','ton:mainnet','watch_only','active',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000013','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002','tron:mainnet','watch_only','active',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO addresses(id,tenant_id,wallet_id,chain_id,canonical_address,display_address,purpose,status,created_at,updated_at)
VALUES
('0198a100-0000-7000-8000-000000000020','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000010','eip155:1',:'evm_deposit_address',:'evm_deposit_address','deposit','available',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000021','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000011','solana:mainnet',:'solana_deposit_address',:'solana_deposit_address','deposit','available',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000022','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000012','ton:mainnet',:'ton_canonical_deposit_address',:'ton_display_deposit_address','deposit','available',clock_timestamp(),clock_timestamp()),
('0198a100-0000-7000-8000-000000000023','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000013','tron:mainnet',:'tron_deposit_address',:'tron_deposit_address','deposit','available',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO api_clients(id,tenant_id,merchant_id,key_id,algorithm,scopes,encrypted_secret,valid_from,valid_until,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000030','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002',
       :'api_key_id','hmac-sha256',ARRAY['payments:read','payments:write','events:read','reconciliation:read'],:'api_secret_envelope'::bytea,
       clock_timestamp(),clock_timestamp()+interval '365 days',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

-- The compatibility gateway gets a separate least-privilege credential. The
-- admission function deliberately requires exactly these three scopes, so a
-- broader merchant integration credential cannot be reused by the bridge.
INSERT INTO api_clients(id,tenant_id,merchant_id,key_id,algorithm,scopes,encrypted_secret,valid_from,valid_until,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000031','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002',
       :'legacy_core_api_key_id','hmac-sha256',ARRAY['payments:read','payments:write','events:read'],:'legacy_core_api_secret_envelope'::bytea,
       clock_timestamp(),clock_timestamp()+interval '365 days',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

CREATE OR REPLACE FUNCTION pg_temp.activate_platform_snapshot(
  requested_tenant uuid, requested_kind platform_config_kind, requested_key text,
  requested_payload jsonb, requester uuid, approver uuid
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  request_id uuid := uuidv7();
  snapshot_id uuid := uuidv7();
  activation_id uuid := uuidv7();
  requested_scope uuid := platform_scope_uuid(requested_tenant);
  now_at timestamptz := clock_timestamp();
BEGIN
  INSERT INTO platform_config_change_requests(
    id,tenant_id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
    requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
    created_at,updated_at,row_version)
  VALUES(request_id,requested_tenant,requested_scope,requested_kind,requested_key,1,0,requested_payload,
    digest(requested_payload::text,'sha256'),'active','Initial standalone production bootstrap',requester,approver,approver,approver,
    now_at,now_at,now_at,now_at,now_at,now_at,4);
  INSERT INTO platform_config_snapshots(
    id,scope_id,tenant_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
  VALUES(snapshot_id,requested_scope,requested_tenant,request_id,requested_kind,requested_key,1,requested_payload,
    digest(requested_payload::text,'sha256'),approver,now_at);
  INSERT INTO platform_config_heads(scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,updated_at)
  VALUES(requested_scope,requested_tenant,requested_kind,requested_key,snapshot_id,1,now_at);
  INSERT INTO platform_config_activations(id,scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,activation_type,actor_id,occurred_at)
  VALUES(activation_id,requested_scope,requested_tenant,requested_kind,requested_key,snapshot_id,1,'activate',approver,now_at);
  RETURN snapshot_id;
END $$;

SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','merchant_environment','0198a100-0000-7000-8000-000000000002',
  '{"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='merchant_environment' AND logical_key='0198a100-0000-7000-8000-000000000002');
SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','feature_flag','new_routes',
  '{"key":"new_routes","enabled":true,"rollout_bps":10000}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='feature_flag' AND logical_key='new_routes');

SELECT pg_temp.activate_platform_snapshot(NULL,'chain','eip155:1','{"family":"evm","network":"ethereum-mainnet","status":"active","genesis_hash":"d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3","quorum":2,"overlap":64,"range_size":128,"max_head_age_seconds":60}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='chain' AND logical_key='eip155:1');
SELECT pg_temp.activate_platform_snapshot(NULL,'chain','solana:mainnet','{"family":"solana","network":"mainnet-beta","status":"active","genesis_hash":"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2dXs","quorum":2,"overlap":64,"range_size":128,"max_head_age_seconds":60}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='chain' AND logical_key='solana:mainnet');
SELECT pg_temp.activate_platform_snapshot(NULL,'chain','ton:mainnet','{"family":"ton","network":"mainnet","status":"active","genesis_hash":"placeholder-until-rpc-admission","quorum":2,"overlap":64,"range_size":128,"max_head_age_seconds":60,"page_size":100}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='chain' AND logical_key='ton:mainnet');
SELECT pg_temp.activate_platform_snapshot(NULL,'chain','tron:mainnet','{"family":"tron","network":"mainnet","status":"active","genesis_hash":"placeholder-until-rpc-admission","quorum":2,"overlap":64,"range_size":128,"max_head_age_seconds":60}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='chain' AND logical_key='tron:mainnet');

SELECT pg_temp.activate_platform_snapshot(NULL,'finality_policy','eip155:1','{"chain_ref":"eip155:1","confirmations":3,"reorg_depth":64}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key='eip155:1');
SELECT pg_temp.activate_platform_snapshot(NULL,'finality_policy','solana:mainnet','{"chain_ref":"solana:mainnet","confirmations":1,"reorg_depth":64}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key='solana:mainnet');
SELECT pg_temp.activate_platform_snapshot(NULL,'finality_policy','ton:mainnet','{"chain_ref":"ton:mainnet","confirmations":1,"reorg_depth":64}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key='ton:mainnet');
SELECT pg_temp.activate_platform_snapshot(NULL,'finality_policy','tron:mainnet','{"chain_ref":"tron:mainnet","confirmations":1,"reorg_depth":64}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='finality_policy' AND logical_key='tron:mainnet');

SELECT pg_temp.activate_platform_snapshot(NULL,'asset_contract','eth-ethereum','{"chain_ref":"eip155:1","asset_code":"eth-ethereum","family":"evm","contract":"native","decimals":18,"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key='eth-ethereum');
SELECT pg_temp.activate_platform_snapshot(NULL,'asset_contract','sol-solana','{"chain_ref":"solana:mainnet","asset_code":"sol-solana","family":"solana","contract":"native","decimals":9,"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key='sol-solana');
SELECT pg_temp.activate_platform_snapshot(NULL,'asset_contract','ton-ton','{"chain_ref":"ton:mainnet","asset_code":"ton-ton","family":"ton","contract":"native","decimals":9,"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key='ton-ton');
SELECT pg_temp.activate_platform_snapshot(NULL,'asset_contract','trx-tron','{"chain_ref":"tron:mainnet","asset_code":"trx-tron","family":"tron","contract":"native","decimals":6,"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key='trx-tron');
SELECT pg_temp.activate_platform_snapshot(NULL,'asset_contract','usdt-tron','{"chain_ref":"tron:mainnet","asset_code":"usdt-tron","family":"tron","contract":"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t","decimals":6,"status":"active"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid(NULL) AND kind='asset_contract' AND logical_key='usdt-tron');

SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','wallet_pool','0198a100-0000-7000-8000-000000000010','{"status":"active","chain_ref":"eip155:1"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='wallet_pool' AND logical_key='0198a100-0000-7000-8000-000000000010');
SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','wallet_pool','0198a100-0000-7000-8000-000000000011','{"status":"active","chain_ref":"solana:mainnet"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='wallet_pool' AND logical_key='0198a100-0000-7000-8000-000000000011');
SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','wallet_pool','0198a100-0000-7000-8000-000000000012','{"status":"active","chain_ref":"ton:mainnet"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='wallet_pool' AND logical_key='0198a100-0000-7000-8000-000000000012');
SELECT pg_temp.activate_platform_snapshot('0198a100-0000-7000-8000-000000000001','wallet_pool','0198a100-0000-7000-8000-000000000013','{"status":"active","chain_ref":"tron:mainnet"}'::jsonb,'0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004')
WHERE NOT EXISTS(SELECT 1 FROM platform_config_heads WHERE scope_id=platform_scope_uuid('0198a100-0000-7000-8000-000000000001') AND kind='wallet_pool' AND logical_key='0198a100-0000-7000-8000-000000000013');

INSERT INTO automated_matching_policy_changes(
  id,tenant_id,merchant_id,proposed_version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,
  accept_late_within_grace,require_same_sender,gasfree_enabled,status,created_by,requested_by,approved_by,activated_by,
  request_reason,approval_reason,activation_reason,approved_at,activated_at,effective_at,created_at,updated_at)
VALUES('0198a100-0000-7000-8000-000000000040','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002',1,
  true,0,'manual_review',false,false,false,'activated','0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000003',
  '0198a100-0000-7000-8000-000000000004','0198a100-0000-7000-8000-000000000004','Initial partial-payment policy','Independent bootstrap approval',
  'Activate exact partial aggregation',clock_timestamp(),clock_timestamp(),clock_timestamp()-interval '1 second',clock_timestamp(),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

INSERT INTO automated_matching_policies(
  id,tenant_id,merchant_id,version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,
  accept_late_within_grace,require_same_sender,gasfree_enabled,effective_at,change_request_id,
  requested_by,approved_by,activated_by,approval_reference,config_hash,created_at)
VALUES('0198a100-0000-7000-8000-000000000041','0198a100-0000-7000-8000-000000000001','0198a100-0000-7000-8000-000000000002',1,
  true,0,'manual_review',false,false,false,clock_timestamp()-interval '1 second','0198a100-0000-7000-8000-000000000040',
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004','0198a100-0000-7000-8000-000000000004',
  'Initial partial-payment policy',digest('{"accumulate_partials":true,"underpayment_tolerance_bps":0,"overpayment_mode":"manual_review","accept_late_within_grace":false,"require_same_sender":false}'::text,'sha256'),clock_timestamp())
ON CONFLICT(id) DO NOTHING;

COMMIT;

SELECT 'wallets='||count(*) FROM wallets WHERE tenant_id='0198a100-0000-7000-8000-000000000001';
SELECT 'addresses='||count(*) FROM addresses WHERE tenant_id='0198a100-0000-7000-8000-000000000001';
SELECT 'assets='||count(*) FROM assets WHERE id IN ('eth-ethereum','sol-solana','ton-ton','trx-tron','usdt-tron');
