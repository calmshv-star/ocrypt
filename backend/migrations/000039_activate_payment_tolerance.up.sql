BEGIN;

-- Existing installations keep immutable matching-policy history, so activate
-- a new version instead of mutating the policy already bound to issued routes.
CREATE TEMP TABLE matching_policy_tolerance_rollout ON COMMIT DROP AS
WITH latest_policy AS (
  SELECT DISTINCT ON (tenant_id,merchant_id) *
  FROM automated_matching_policies
  ORDER BY tenant_id,merchant_id,effective_at DESC,version DESC
)
SELECT
  gen_random_uuid() AS change_id,
  gen_random_uuid() AS policy_id,
  p.tenant_id,
  p.merchant_id,
  GREATEST(
    p.version,
    COALESCE((
      SELECT max(c.proposed_version)
      FROM automated_matching_policy_changes c
      WHERE c.tenant_id=p.tenant_id
        AND c.merchant_id=p.merchant_id
    ),0)
  )+1 AS next_version,
  p.accumulate_partials,
  p.accept_late_within_grace,
  p.require_same_sender,
  p.gasfree_enabled,
  p.gasfree_fee_collectors,
  p.requested_by,
  p.approved_by,
  p.activated_by,
  clock_timestamp() AS rollout_at
FROM latest_policy p
WHERE p.underpayment_tolerance_bps<>500
   OR p.overpayment_mode<>'credit_expected_hold_excess';

INSERT INTO automated_matching_policy_changes(
  id,tenant_id,merchant_id,proposed_version,accumulate_partials,
  underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,
  require_same_sender,gasfree_enabled,gasfree_fee_collectors,status,
  created_by,requested_by,approved_by,activated_by,request_reason,
  approval_reason,activation_reason,approved_at,activated_at,effective_at,
  created_at,updated_at,version
)
SELECT
  change_id,tenant_id,merchant_id,next_version,accumulate_partials,
  500,'credit_expected_hold_excess',accept_late_within_grace,
  require_same_sender,gasfree_enabled,gasfree_fee_collectors,'activated',
  requested_by,requested_by,approved_by,activated_by,
  'Operator-approved five-percent payment shortfall rollout',
  'Deployment preserves the independently approved policy actors',
  'Activate five-percent shortfall and deterministic excess holding',
  rollout_at,rollout_at,rollout_at,rollout_at,rollout_at,4
FROM matching_policy_tolerance_rollout;

INSERT INTO automated_matching_policies(
  id,tenant_id,merchant_id,version,accumulate_partials,
  underpayment_tolerance_bps,overpayment_mode,accept_late_within_grace,
  require_same_sender,gasfree_enabled,gasfree_fee_collectors,effective_at,
  change_request_id,requested_by,approved_by,activated_by,approval_reference,
  config_hash,created_at
)
SELECT
  policy_id,tenant_id,merchant_id,next_version,accumulate_partials,
  500,'credit_expected_hold_excess',accept_late_within_grace,
  require_same_sender,gasfree_enabled,gasfree_fee_collectors,rollout_at,
  change_id,requested_by,approved_by,activated_by,
  'Operator-approved payment tolerance rollout 2026-08-13',
  digest(convert_to(jsonb_build_object(
    'accumulate_partials',accumulate_partials,
    'underpayment_tolerance_bps',500,
    'overpayment_mode','credit_expected_hold_excess',
    'accept_late_within_grace',accept_late_within_grace,
    'require_same_sender',require_same_sender,
    'gasfree_enabled',gasfree_enabled,
    'gasfree_fee_collectors',to_jsonb(gasfree_fee_collectors)
  )::text,'UTF8'),'sha256'),
  rollout_at
FROM matching_policy_tolerance_rollout;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM (
      SELECT DISTINCT ON (tenant_id,merchant_id)
        underpayment_tolerance_bps,overpayment_mode
      FROM automated_matching_policies
      ORDER BY tenant_id,merchant_id,effective_at DESC,version DESC
    ) latest
    WHERE latest.underpayment_tolerance_bps<>500
       OR latest.overpayment_mode<>'credit_expected_hold_excess'
  ) THEN
    RAISE EXCEPTION 'payment tolerance rollout did not activate for every existing merchant';
  END IF;
END $$;

COMMIT;
