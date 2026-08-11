BEGIN;
DROP FUNCTION IF EXISTS platform_wallet_runtime_admission(uuid,uuid,text);
DROP FUNCTION IF EXISTS platform_route_runtime_admission(uuid,uuid,text,text);
DROP FUNCTION IF EXISTS platform_latest_pause(uuid,platform_config_kind,text);
DROP TABLE IF EXISTS scanner_runtime_config_evidence;
ALTER TABLE address_assignments DROP CONSTRAINT IF EXISTS address_assignment_runtime_evidence_complete;
ALTER TABLE address_assignments DROP COLUMN IF EXISTS platform_wallet_pool_fence,DROP COLUMN IF EXISTS platform_wallet_pool_snapshot_id;
ALTER TABLE rate_quotes DROP CONSTRAINT IF EXISTS rate_quote_runtime_evidence_complete;
ALTER TABLE rate_quotes
  DROP COLUMN IF EXISTS runtime_required_finality,
  DROP COLUMN IF EXISTS platform_finality_fence,DROP COLUMN IF EXISTS platform_finality_snapshot_id,
  DROP COLUMN IF EXISTS platform_asset_fence,DROP COLUMN IF EXISTS platform_asset_snapshot_id,
  DROP COLUMN IF EXISTS platform_chain_fence,DROP COLUMN IF EXISTS platform_chain_snapshot_id,
  DROP COLUMN IF EXISTS platform_environment_fence,DROP COLUMN IF EXISTS platform_environment_snapshot_id;
COMMIT;
