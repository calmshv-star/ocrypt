\set ON_ERROR_STOP on
\pset pager off
BEGIN READ ONLY;

-- Production acceptance: inspect the same projection used by invoice planning.
-- Do not create synthetic production orders/webhooks or alter customer data.
DO $$
DECLARE
  asset text;
  ready boolean;
BEGIN
  FOREACH asset IN ARRAY ARRAY['usdt-tron','trx-tron','sol-solana','ton-ton','eth-ethereum'] LOOP
    SELECT EXISTS (
      SELECT 1
      FROM platform_config_heads h
      JOIN platform_config_snapshots s ON s.id=h.snapshot_id
      JOIN rate_runtime_jobs j ON j.scope_id=h.scope_id AND j.policy_key=h.logical_key
      JOIN admitted_rate_ticks t ON t.rate_policy_snapshot_id=s.id
      JOIN asset_rate_ticks p ON p.id=t.id
      WHERE h.scope_id=platform_scope_uuid(NULL) AND h.kind='rate_policy'
        AND h.logical_key='rate-'||asset||'-rub'
        AND s.payload->'sources'=jsonb_build_array(
          asset||'-rub-coingecko',asset||'-rub-coinpaprika',asset||'-rub-coinmarketcap')
        AND (s.payload->>'quorum')::integer=2
        AND (s.payload->>'poll_interval_seconds')::integer BETWEEN 1 AND 300
        AND j.status='active' AND j.last_success_at>clock_timestamp()-interval '10 minutes'
        AND t.base_asset=asset AND t.quote_asset='RUB'
        AND t.admitted_at>clock_timestamp()-interval '10 minutes'
        AND t.expires_at>clock_timestamp() AND t.quorum=2
        AND p.status='active' AND p.asset_id=asset AND p.fiat_currency='RUB'
        AND p.numerator=t.price_numerator AND p.denominator=t.price_denominator
        AND p.observed_at+make_interval(secs=>p.max_age_seconds)>clock_timestamp()
        AND (
          SELECT count(DISTINCT o.provider_ref)
          FROM admitted_rate_tick_observations x
          JOIN rate_source_observations o ON o.id=x.observation_id AND o.scope_id=x.scope_id
          WHERE x.tick_id=t.id AND x.scope_id=t.scope_id
        )=t.source_count
    ) INTO ready;
    IF NOT ready THEN
      RAISE EXCEPTION 'rate readiness failed for %: check sources, quorum, freshness and planner projection',asset;
    END IF;
    RAISE NOTICE 'rate readiness passed: %/RUB',asset;
  END LOOP;
END $$;

COMMIT;
