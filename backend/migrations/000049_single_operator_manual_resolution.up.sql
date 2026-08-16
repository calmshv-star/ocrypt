BEGIN;

-- A manual payment decision is made by the currently authenticated operator.
-- The independent chain verifier remains mandatory before settlement, but a
-- second human approval is no longer part of this workflow.
ALTER TABLE manual_resolutions
    DROP CONSTRAINT IF EXISTS manual_resolution_distinct_actors,
    DROP CONSTRAINT IF EXISTS manual_resolution_approval_state;

ALTER TABLE manual_resolutions
    ADD CONSTRAINT manual_resolution_operator_verification_state CHECK (
        NOT (accept_shortfall OR accept_cross_asset)
        OR status IN (
            'approval_required',
            'verification_requested',
            'verification_retry',
            'resolved',
            'invalid',
            'conflict',
            'reorged'
        )
    );

-- Existing requests must not remain blocked waiting for an actor that the
-- product no longer requires. The verifier will re-check candidate freshness,
-- route identity, amount, asset, finality and canonical transfer evidence.
UPDATE unmatched_payments u
SET status = 'verification_requested',
    updated_at = clock_timestamp(),
    version = version + 1
WHERE u.status = 'approval_required'
  AND EXISTS (
      SELECT 1
      FROM manual_resolutions mr
      WHERE mr.unmatched_id = u.id
        AND mr.tenant_id = u.tenant_id
        AND mr.status = 'approval_required'
        AND mr.target_route_id = u.selected_route_id
  );

UPDATE manual_resolutions
SET status = 'verification_requested',
    next_attempt_at = clock_timestamp(),
    updated_at = clock_timestamp(),
    version = version + 1
WHERE status = 'approval_required';

UPDATE admin_action_requests ar
SET status = 'cancelled',
    decision_reason = 'Second-operator approval removed; automatic verification queued.',
    decided_at = clock_timestamp(),
    version = version + 1
WHERE ar.status = 'pending_approval'
  AND ar.kind = 'manual_resolution'
  AND EXISTS (
      SELECT 1
      FROM manual_resolutions mr
      WHERE mr.id = ar.core_resolution_id
        AND mr.status = 'verification_requested'
  );

COMMIT;
