CREATE OR REPLACE FUNCTION pg_temp.activate_platform_snapshot(
  requested_tenant uuid, requested_kind platform_config_kind, requested_key text,
  requested_payload jsonb, requester uuid, approver uuid
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  request_id uuid := uuidv7();
  created_snapshot_id uuid := uuidv7();
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
  VALUES(created_snapshot_id,requested_scope,requested_tenant,request_id,requested_kind,requested_key,1,requested_payload,
    digest(requested_payload::text,'sha256'),approver,now_at);
  INSERT INTO platform_config_heads(scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,updated_at)
  VALUES(requested_scope,requested_tenant,requested_kind,requested_key,created_snapshot_id,1,now_at);
  INSERT INTO platform_config_activations(id,scope_id,tenant_id,kind,logical_key,snapshot_id,fence_token,activation_type,actor_id,occurred_at)
  VALUES(activation_id,requested_scope,requested_tenant,requested_kind,requested_key,created_snapshot_id,1,'activate',approver,now_at);
  RETURN created_snapshot_id;
END $$;

CREATE OR REPLACE FUNCTION pg_temp.reconcile_platform_snapshot(
  requested_tenant uuid, requested_kind platform_config_kind, requested_key text,
  requested_payload jsonb, requester uuid, approver uuid
) RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
  request_id uuid := uuidv7();
  created_snapshot_id uuid := uuidv7();
  activation_id uuid := uuidv7();
  requested_scope uuid := platform_scope_uuid(requested_tenant);
  now_at timestamptz := clock_timestamp();
  current_snapshot uuid;
  current_payload jsonb;
  current_version bigint;
  current_fence bigint;
BEGIN
  SELECT h.snapshot_id,s.payload,s.version,h.fence_token
    INTO current_snapshot,current_payload,current_version,current_fence
  FROM platform_config_heads h
  JOIN platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id
  WHERE h.scope_id=requested_scope AND h.kind=requested_kind AND h.logical_key=requested_key
  FOR UPDATE;

  IF NOT FOUND THEN
    RETURN pg_temp.activate_platform_snapshot(
      requested_tenant,requested_kind,requested_key,requested_payload,requester,approver
    );
  END IF;
  IF current_payload=requested_payload THEN
    RETURN current_snapshot;
  END IF;

  INSERT INTO platform_config_change_requests(
    id,tenant_id,scope_id,kind,logical_key,version,based_on_version,payload,payload_hash,status,reason,
    requested_by,approved_by,scheduled_by,activated_by,requested_at,decided_at,scheduled_for,activated_at,
    created_at,updated_at,row_version)
  VALUES(request_id,requested_tenant,requested_scope,requested_kind,requested_key,current_version+1,current_version,requested_payload,
    digest(requested_payload::text,'sha256'),'active','Reconcile verified public-chain safety configuration',requester,approver,approver,approver,
    now_at,now_at,now_at,now_at,now_at,now_at,4);
  INSERT INTO platform_config_snapshots(
    id,scope_id,tenant_id,change_request_id,kind,logical_key,version,payload,payload_hash,activated_by,activated_at)
  VALUES(created_snapshot_id,requested_scope,requested_tenant,request_id,requested_kind,requested_key,current_version+1,requested_payload,
    digest(requested_payload::text,'sha256'),approver,now_at);
  UPDATE platform_config_change_requests c
  SET status='superseded',updated_at=now_at,row_version=row_version+1
  FROM platform_config_snapshots s
  WHERE s.id=current_snapshot AND c.id=s.change_request_id AND c.status='active';
  UPDATE platform_config_heads
  SET snapshot_id=created_snapshot_id,fence_token=current_fence+1,updated_at=now_at
  WHERE scope_id=requested_scope AND kind=requested_kind AND logical_key=requested_key;
  INSERT INTO platform_config_activations(
    id,scope_id,tenant_id,kind,logical_key,snapshot_id,previous_snapshot_id,fence_token,activation_type,actor_id,occurred_at)
  VALUES(activation_id,requested_scope,requested_tenant,requested_kind,requested_key,created_snapshot_id,current_snapshot,current_fence+1,'activate',approver,now_at);
  RETURN created_snapshot_id;
END $$;
