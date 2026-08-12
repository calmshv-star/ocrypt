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
