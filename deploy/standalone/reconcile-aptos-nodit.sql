\set ON_ERROR_STOP on

BEGIN;
\ir activate-platform-snapshot.sql

DO $$
BEGIN
  IF NOT EXISTS(SELECT 1 FROM chains WHERE id='aptos:1' AND status='disabled') THEN
    RAISE EXCEPTION 'Aptos chain must remain disabled during Nodit provider reconciliation';
  END IF;
  IF 2 <> (
    SELECT count(*) FROM assets
    WHERE id IN ('usdc-aptos','usdt-aptos') AND chain_id='aptos:1' AND status='deposit_disabled'
  ) THEN
    RAISE EXCEPTION 'Aptos assets must remain deposit_disabled during Nodit provider reconciliation';
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM wallets
    WHERE id='0198a100-0000-7000-8000-000000000067' AND chain_id='aptos:1' AND status='disabled'
  ) THEN
    RAISE EXCEPTION 'Aptos wallet pool must remain disabled during Nodit provider reconciliation';
  END IF;
  IF 3 <> (
    SELECT count(*)
    FROM platform_config_heads h
    JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    WHERE h.scope_id=platform_scope_uuid(NULL)
      AND (
        (h.kind='chain' AND h.logical_key='aptos:1' AND s.payload->>'status'='disabled') OR
        (h.kind='asset_contract' AND h.logical_key IN ('usdc-aptos','usdt-aptos') AND s.payload->>'status'='deposit_disabled')
      )
  ) THEN
    RAISE EXCEPTION 'Aptos runtime chain and assets must remain disabled';
  END IF;
END $$;

SELECT pg_temp.reconcile_platform_snapshot(
  NULL,'rpc_provider','rpc/aptos-nodit',
  jsonb_build_object(
    'chain_ref','aptos:1',
    'endpoint','https://fullnode.mainnet.aptoslabs.com',
    'indexer_endpoint_ref','aptos/nodit-indexer',
    'capabilities',jsonb_build_array('blocks','transactions'),
    'provider_kind','aptos-fullnode',
    'provider_id','aptos-nodit',
    'timeout_ms',10000,
    'provider_operations',(
      SELECT jsonb_object_agg(operation,jsonb_build_object(
        'timeout_ms',10000,'max_attempts',2,'backoff_ms',250,
        'rate_limit',60,'rate_window_seconds',60,'max_health_age_seconds',120,
        'failure_threshold',3,'open_seconds',30,'half_open_successes',2,
        'priority',20,'failure_domain','nodit','max_lag_blocks',128
      ))
      FROM unnest(ARRAY['health','head','range','transaction_lookup','transfer_verify']) operation
    )
  ),
  '0198a100-0000-7000-8000-000000000003','0198a100-0000-7000-8000-000000000004'
);

UPDATE provider_operation_bindings
SET status='paused',version=version+1,updated_at=clock_timestamp()
WHERE provider_kind='on_chain' AND provider_id='aptos-labs' AND status='active';

DO $$
BEGIN
  IF NOT EXISTS(
    SELECT 1
    FROM platform_config_heads h
    JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
    JOIN provider_operation_bindings b ON b.platform_snapshot_id=s.id
    WHERE h.scope_id=platform_scope_uuid(NULL)
      AND h.kind='rpc_provider' AND h.logical_key='rpc/aptos-nodit'
      AND s.payload->>'provider_id'='aptos-nodit'
      AND s.payload->>'indexer_endpoint_ref'='aptos/nodit-indexer'
      AND s.payload->'provider_operations'->'head'->>'failure_domain'='nodit'
      AND b.provider_kind='on_chain' AND b.provider_id='aptos-nodit' AND b.status='active'
  ) THEN
    RAISE EXCEPTION 'Nodit Aptos provider reconciliation did not produce the expected active metadata binding';
  END IF;
  IF EXISTS(
    SELECT 1 FROM provider_operation_bindings
    WHERE provider_kind='on_chain' AND provider_id='aptos-labs' AND status='active'
  ) THEN
    RAISE EXCEPTION 'generic Aptos secondary provider remained active';
  END IF;
  IF EXISTS(SELECT 1 FROM chains WHERE id='aptos:1' AND status<>'disabled') OR
     EXISTS(SELECT 1 FROM assets WHERE chain_id='aptos:1' AND status<>'deposit_disabled') OR
     EXISTS(SELECT 1 FROM wallets WHERE id='0198a100-0000-7000-8000-000000000067' AND status<>'disabled') THEN
    RAISE EXCEPTION 'Nodit provider reconciliation changed Aptos payment admission state';
  END IF;
END $$;

\if :{?dry_run}
ROLLBACK;
\else
COMMIT;
\endif
