\set ON_ERROR_STOP on

BEGIN;

CREATE OR REPLACE FUNCTION pg_temp.replace_rate_snapshot(
  requested_kind platform_config_kind,
  requested_key text
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  requested_scope uuid := platform_scope_uuid(NULL);
  requester uuid := '0198a100-0000-7000-8000-000000000003';
  approver uuid := '0198a100-0000-7000-8000-000000000004';
  new_request_id uuid := uuidv7();
  new_snapshot_id uuid := uuidv7();
  new_activation_id uuid := uuidv7();
  prior_snapshot_id uuid;
  old_snapshot platform_config_snapshots%ROWTYPE;
  old_fence bigint;
  next_payload jsonb;
  now_at timestamptz := clock_timestamp();
BEGIN
  SELECT h.snapshot_id,h.fence_token INTO prior_snapshot_id,old_fence
  FROM platform_config_heads h
  WHERE h.scope_id=requested_scope AND h.kind=requested_kind AND h.logical_key=requested_key
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'missing active rate snapshot %.%',requested_kind,requested_key;
  END IF;
  SELECT * INTO STRICT old_snapshot FROM platform_config_snapshots WHERE id=prior_snapshot_id;
  IF (old_snapshot.payload->>'max_age_seconds')::integer=900 THEN
    RETURN old_snapshot.id;
  END IF;

  next_payload := jsonb_set(old_snapshot.payload,'{max_age_seconds}','900'::jsonb,false);
  INSERT INTO platform_config_change_requests(
    id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
    requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
    created_at,updated_at,row_version)
  VALUES(new_request_id,requested_scope,requested_kind,requested_key,old_snapshot.version+1,old_snapshot.version,
    next_payload,digest(next_payload::text,'sha256'),'active',
    'Use a 15 minute freshness window for the slower independent public source while retaining quorum 2 of 2',
    requester,approver,approver,approver,now_at,now_at,now_at,now_at,now_at,now_at,4);
  INSERT INTO platform_config_snapshots(
    id,scope_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
  VALUES(new_snapshot_id,requested_scope,new_request_id,requested_kind,requested_key,old_snapshot.version+1,
    next_payload,digest(next_payload::text,'sha256'),approver,now_at);
  UPDATE platform_config_heads
  SET snapshot_id=new_snapshot_id,fence_token=old_fence+1,updated_at=now_at
  WHERE scope_id=requested_scope AND kind=requested_kind AND logical_key=requested_key;
  INSERT INTO platform_config_activations(
    id,scope_id,kind,logical_key,snapshot_id,fence_token,activation_type,actor_id,occurred_at)
  VALUES(new_activation_id,requested_scope,requested_kind,requested_key,new_snapshot_id,old_fence+1,'activate',approver,now_at);
  RETURN new_snapshot_id;
END $$;

SELECT pg_temp.replace_rate_snapshot(kind,logical_key)
FROM platform_config_heads
WHERE scope_id=platform_scope_uuid(NULL)
  AND kind IN ('rate_source','rate_policy')
  AND logical_key LIKE '%-rub%'
ORDER BY kind,logical_key;

UPDATE rate_runtime_jobs
SET next_attempt_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE policy_key LIKE 'rate-%-rub' AND status='active';

COMMIT;
