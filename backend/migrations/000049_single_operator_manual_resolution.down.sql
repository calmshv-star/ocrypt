BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM manual_resolutions
        WHERE (accept_shortfall OR accept_cross_asset)
          AND status NOT IN ('approval_required', 'invalid', 'conflict')
          AND (approved_by IS NULL OR approved_by = requested_by)
    ) THEN
        RAISE EXCEPTION 'cannot restore second-operator policy after single-operator resolutions have advanced';
    END IF;
END $$;

ALTER TABLE manual_resolutions
    DROP CONSTRAINT IF EXISTS manual_resolution_operator_verification_state;

ALTER TABLE manual_resolutions
    ADD CONSTRAINT manual_resolution_distinct_actors CHECK (approved_by IS NULL OR approved_by <> requested_by),
    ADD CONSTRAINT manual_resolution_approval_state CHECK (
        status IN ('approval_required','invalid','conflict')
        OR NOT (accept_shortfall OR accept_cross_asset)
        OR approved_by IS NOT NULL
    );

COMMIT;
