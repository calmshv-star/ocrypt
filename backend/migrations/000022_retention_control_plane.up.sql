BEGIN;

-- 000015 owns archive/prune execution. This migration adds the deliberately
-- separate operator control plane; no API role receives direct mutation rights.
CREATE TABLE retention_policy_heads (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    data_class text NOT NULL CHECK(data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK(policy_version>0),
    fence bigint NOT NULL CHECK(fence>0),
    activated_at timestamptz NOT NULL,
    PRIMARY KEY(tenant_id,data_class),
    FOREIGN KEY(policy_id,tenant_id,data_class,policy_version)
      REFERENCES retention_policy_versions(id,tenant_id,data_class,version) ON DELETE RESTRICT
);

INSERT INTO retention_policy_heads(tenant_id,data_class,policy_id,policy_version,fence,activated_at)
SELECT DISTINCT ON (tenant_id,data_class) tenant_id,data_class,id,version,1,effective_at
FROM retention_policy_versions
ORDER BY tenant_id,data_class,version DESC;

CREATE TABLE retention_policy_change_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    data_class text NOT NULL CHECK(data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    expected_policy_version bigint NOT NULL CHECK(expected_policy_version>=0),
    expected_head_fence bigint NOT NULL CHECK(expected_head_fence>=0),
    archive_after_days integer NOT NULL CHECK(archive_after_days BETWEEN 1 AND 3650),
    prune_grace_days integer NOT NULL CHECK(prune_grace_days BETWEEN 1 AND 90),
    object_lock_days integer NOT NULL CHECK(object_lock_days BETWEEN 30 AND 3650),
    prune_enabled boolean NOT NULL,
    status text NOT NULL CHECK(status IN ('pending_approval','scheduled','active','rejected','conflict','expired')),
    reason text NOT NULL CHECK(length(btrim(reason)) BETWEEN 8 AND 2048),
    requested_by uuid NOT NULL,
    requested_session_id text NOT NULL CHECK(length(requested_session_id) BETWEEN 1 AND 255),
    requested_mfa_at timestamptz NOT NULL,
    approved_by uuid,
    approved_session_id text,
    approved_mfa_at timestamptz,
    approved_at timestamptz,
    rejected_by uuid,
    rejected_session_id text,
    decision_reason text,
    scheduled_for timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    activated_policy_id uuid,
    activated_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    row_version bigint NOT NULL DEFAULT 1 CHECK(row_version>0),
    UNIQUE(id,tenant_id),
    CHECK(object_lock_days>prune_grace_days),
    CHECK(data_class='published_outbox_payload' OR NOT prune_enabled),
    CHECK(scheduled_for>=created_at),
    CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
    CHECK(
      status='pending_approval' AND approved_by IS NULL AND rejected_by IS NULL AND approved_at IS NULL AND decided_at IS NULL AND activated_at IS NULL OR
      status='scheduled' AND approved_by IS NOT NULL AND approved_session_id IS NOT NULL AND approved_mfa_at IS NOT NULL AND approved_at IS NOT NULL AND decided_at IS NOT NULL AND rejected_by IS NULL AND activated_at IS NULL OR
      status='active' AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND decided_at IS NOT NULL AND activated_policy_id IS NOT NULL AND activated_at IS NOT NULL OR
      status='rejected' AND rejected_by IS NOT NULL AND rejected_session_id IS NOT NULL AND decision_reason IS NOT NULL AND decided_at IS NOT NULL AND activated_at IS NULL OR
      status='conflict' AND approved_by IS NOT NULL AND approved_at IS NOT NULL AND decision_reason IS NOT NULL AND decided_at IS NOT NULL AND activated_at IS NULL OR
      status='expired' AND approved_by IS NULL AND rejected_by IS NULL AND decision_reason IS NOT NULL AND decided_at IS NOT NULL AND activated_at IS NULL
    ),
    CHECK(approved_by IS NULL OR approved_by<>requested_by),
    CHECK(approved_at IS NULL OR approved_at>=created_at),
    CHECK(activated_at IS NULL OR activated_at>=approved_at AND activated_at>=scheduled_for),
    FOREIGN KEY(activated_policy_id,tenant_id,data_class)
      REFERENCES retention_policy_versions(id,tenant_id,data_class) ON DELETE RESTRICT
);
CREATE INDEX retention_policy_change_due_idx ON retention_policy_change_requests(scheduled_for,id) WHERE status='scheduled';
CREATE INDEX retention_policy_change_list_idx ON retention_policy_change_requests(tenant_id,id);
CREATE UNIQUE INDEX retention_policy_one_open_change ON retention_policy_change_requests(tenant_id,data_class) WHERE status IN ('pending_approval','scheduled');

ALTER TABLE retention_legal_holds
  ADD COLUMN case_reference text,
  ADD COLUMN created_session_id text,
  ADD COLUMN created_mfa_at timestamptz,
  ADD COLUMN expired_at timestamptz,
  ADD COLUMN expired_by text,
  ADD COLUMN expiry_reason text;
ALTER TABLE retention_legal_holds ADD CONSTRAINT retention_legal_holds_tenant_identity UNIQUE(id,tenant_id);
ALTER TABLE retention_legal_holds ADD CONSTRAINT retention_hold_case_reference_shape
  CHECK(case_reference IS NULL OR case_reference ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$');
ALTER TABLE retention_legal_holds ADD CONSTRAINT retention_hold_creator_session_shape
  CHECK((created_session_id IS NULL AND created_mfa_at IS NULL) OR
        (length(created_session_id) BETWEEN 1 AND 255 AND created_mfa_at IS NOT NULL AND created_mfa_at<=created_at));
ALTER TABLE retention_legal_holds DROP CONSTRAINT IF EXISTS retention_legal_holds_check2;
ALTER TABLE retention_legal_holds ADD CONSTRAINT retention_hold_terminal_shape CHECK(
  (released_at IS NULL AND released_by IS NULL AND release_reason IS NULL AND expired_at IS NULL AND expired_by IS NULL AND expiry_reason IS NULL) OR
  (released_at IS NOT NULL AND released_by IS NOT NULL AND length(released_by) BETWEEN 1 AND 255 AND
    release_reason IS NOT NULL AND length(release_reason) BETWEEN 8 AND 2048 AND expired_at IS NULL AND expired_by IS NULL AND expiry_reason IS NULL) OR
  (expired_at IS NOT NULL AND expired_by IS NOT NULL AND length(expired_by) BETWEEN 1 AND 255 AND
    expiry_reason IS NOT NULL AND length(expiry_reason) BETWEEN 8 AND 2048 AND released_at IS NULL AND released_by IS NULL AND release_reason IS NULL)
);
DROP INDEX IF EXISTS retention_legal_holds_active_idx;
CREATE INDEX retention_legal_holds_active_idx
  ON retention_legal_holds(tenant_id,data_class,expires_at) WHERE released_at IS NULL AND expired_at IS NULL;

CREATE TABLE retention_hold_release_requests (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    hold_id uuid NOT NULL,
    expected_hold_version bigint NOT NULL CHECK(expected_hold_version>0),
    status text NOT NULL CHECK(status IN ('pending_approval','completed','rejected','conflict','expired')),
    reason text NOT NULL CHECK(length(btrim(reason)) BETWEEN 8 AND 2048),
    requested_by uuid NOT NULL,
    requested_session_id text NOT NULL CHECK(length(requested_session_id) BETWEEN 1 AND 255),
    requested_mfa_at timestamptz NOT NULL,
    approved_by uuid,
    approved_session_id text,
    approved_mfa_at timestamptz,
    rejected_by uuid,
    rejected_session_id text,
    decision_reason text,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    updated_at timestamptz NOT NULL,
    row_version bigint NOT NULL DEFAULT 1 CHECK(row_version>0),
    UNIQUE(id,tenant_id),
    FOREIGN KEY(hold_id,tenant_id) REFERENCES retention_legal_holds(id,tenant_id) ON DELETE RESTRICT,
    CHECK(
      status='pending_approval' AND approved_by IS NULL AND rejected_by IS NULL AND decided_at IS NULL OR
      status='completed' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL OR
      status='rejected' AND rejected_by IS NOT NULL AND approved_by IS NULL AND decided_at IS NOT NULL OR
      status='conflict' AND approved_by IS NOT NULL AND rejected_by IS NULL AND decided_at IS NOT NULL OR
      status='expired' AND approved_by IS NULL AND rejected_by IS NULL AND decision_reason IS NOT NULL AND decided_at IS NOT NULL
    ),
    CHECK(approved_by IS NULL OR approved_by<>requested_by),
    CHECK(decided_at IS NULL OR decided_at>=created_at),
    CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes')
);
CREATE UNIQUE INDEX retention_hold_one_pending_release
  ON retention_hold_release_requests(hold_id) WHERE status='pending_approval';
CREATE INDEX retention_hold_release_list_idx ON retention_hold_release_requests(tenant_id,id);

CREATE TABLE retention_control_idempotency (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    actor_id uuid NOT NULL,
    operation text NOT NULL CHECK(length(operation) BETWEEN 1 AND 128),
    idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
    resource_type text NOT NULL CHECK(resource_type IN ('policy_change','legal_hold','hold_release')),
    resource_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY(tenant_id,actor_id,operation,idempotency_key)
);

CREATE TABLE retention_control_worker_heartbeats (
    worker_id text PRIMARY KEY CHECK(worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
    last_cycle_at timestamptz NOT NULL,
    processed integer NOT NULL CHECK(processed BETWEEN 0 AND 100),
    version bigint NOT NULL DEFAULT 1 CHECK(version>0)
);

ALTER TABLE retention_policy_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policy_change_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_hold_release_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_control_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policy_heads FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_policy_change_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_hold_release_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_control_idempotency FORCE ROW LEVEL SECURITY;
CREATE POLICY retention_policy_head_scope ON retention_policy_heads USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_policy_change_scope ON retention_policy_change_requests USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_hold_release_scope ON retention_hold_release_requests USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_control_idempotency_scope ON retention_control_idempotency USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE FUNCTION retention_control_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN RAISE EXCEPTION 'retention control evidence is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER retention_policy_heads_no_delete BEFORE DELETE ON retention_policy_heads FOR EACH ROW EXECUTE FUNCTION retention_control_immutable();
CREATE TRIGGER retention_control_idempotency_immutable BEFORE UPDATE OR DELETE ON retention_control_idempotency FOR EACH ROW EXECUTE FUNCTION retention_control_immutable();

CREATE OR REPLACE FUNCTION retention_guard_legal_hold_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF current_setting('app.retention_hold_write',true)<>'1' THEN
    RAISE EXCEPTION 'legal holds may change only through fenced retention functions' USING ERRCODE='42501';
  END IF;
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'legal holds cannot be deleted' USING ERRCODE='55000'; END IF;
  IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.data_class<>OLD.data_class OR
     NEW.scope_type<>OLD.scope_type OR NEW.merchant_id IS DISTINCT FROM OLD.merchant_id OR
     NEW.source_table IS DISTINCT FROM OLD.source_table OR NEW.source_record_id IS DISTINCT FROM OLD.source_record_id OR
     NEW.reason<>OLD.reason OR NEW.actor_id<>OLD.actor_id OR NEW.created_at<>OLD.created_at OR
     NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.case_reference IS DISTINCT FROM OLD.case_reference OR
     NEW.created_session_id IS DISTINCT FROM OLD.created_session_id OR NEW.created_mfa_at IS DISTINCT FROM OLD.created_mfa_at OR
     OLD.released_at IS NOT NULL OR OLD.expired_at IS NOT NULL OR NEW.version<>OLD.version+1 OR
     (NEW.released_at IS NOT NULL)=(NEW.expired_at IS NOT NULL)) THEN
    RAISE EXCEPTION 'legal hold identity or lifecycle is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION retention_control_reserve_idempotency(
  requested_tenant uuid,requested_actor uuid,requested_operation text,requested_key text,
  requested_hash bytea,requested_type text,requested_resource uuid,requested_now timestamptz
) RETURNS TABLE(resource_id uuid,reserved boolean) LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$
DECLARE prior public.retention_control_idempotency%ROWTYPE;
BEGIN
  IF length(requested_key) NOT BETWEEN 8 AND 255 OR octet_length(requested_hash)<>32 THEN
    RAISE EXCEPTION 'retention idempotency input is invalid' USING ERRCODE='22023';
  END IF;
  INSERT INTO public.retention_control_idempotency(tenant_id,actor_id,operation,idempotency_key,request_hash,resource_type,resource_id,created_at)
  VALUES(requested_tenant,requested_actor,requested_operation,requested_key,requested_hash,requested_type,requested_resource,requested_now)
  ON CONFLICT DO NOTHING;
  IF FOUND THEN RETURN QUERY SELECT requested_resource,true; RETURN; END IF;
  SELECT * INTO prior FROM public.retention_control_idempotency WHERE tenant_id=requested_tenant AND actor_id=requested_actor AND operation=requested_operation AND idempotency_key=requested_key;
  IF prior.request_hash<>requested_hash OR prior.resource_type<>requested_type THEN
    RAISE EXCEPTION 'retention idempotency conflict' USING ERRCODE='RI022';
  END IF;
  RETURN QUERY SELECT prior.resource_id,false;
END $$;
REVOKE ALL ON FUNCTION retention_control_reserve_idempotency(uuid,uuid,text,text,bytea,text,uuid,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_control_mfa_valid(mfa_at timestamptz,at_time timestamptz) RETURNS boolean
LANGUAGE sql IMMUTABLE SET search_path=pg_catalog AS $$
  SELECT mfa_at IS NOT NULL AND mfa_at<=at_time+interval '10 seconds' AND mfa_at>=at_time-interval '10 minutes'
$$;
REVOKE ALL ON FUNCTION retention_control_mfa_valid(timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_admin_allowed(
  requested_actor uuid,requested_tenant uuid,requested_permission text,requested_session text,
  requested_step_up timestamptz,require_fresh_mfa boolean
) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT requested_tenant IS NOT NULL
     AND current_setting('app.tenant_id',true)=requested_tenant::text
     AND current_setting('app.retention_actor_id',true)=requested_actor::text
     AND current_setting('app.retention_session_id',true)=requested_session
     AND (NOT require_fresh_mfa OR public.retention_control_mfa_valid(requested_step_up,clock_timestamp()))
     AND EXISTS(
       SELECT 1 FROM public.admin_role_bindings b
       JOIN public.admin_users u ON u.id=b.user_id
       JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
       WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key=requested_permission
         AND b.merchant_id IS NULL AND b.revoked_at IS NULL
         AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
         AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant)
     )
$$;
REVOKE ALL ON FUNCTION retention_admin_allowed(uuid,uuid,text,text,timestamptz,boolean) FROM PUBLIC;

CREATE FUNCTION retention_control_record_exists(
  requested_tenant uuid,requested_class text,requested_scope text,requested_merchant uuid,requested_table text,requested_record uuid
) RETURNS boolean LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF requested_scope='tenant' THEN
    RETURN requested_merchant IS NULL AND requested_table IS NULL AND requested_record IS NULL AND EXISTS(SELECT 1 FROM public.tenants WHERE id=requested_tenant AND status='active');
  ELSIF requested_scope='merchant' THEN
    RETURN requested_merchant IS NOT NULL AND requested_table IS NULL AND requested_record IS NULL AND EXISTS(SELECT 1 FROM public.merchants WHERE id=requested_merchant AND tenant_id=requested_tenant);
  ELSIF requested_scope<>'record' OR requested_merchant IS NULL OR requested_record IS NULL THEN RETURN false;
  ELSIF requested_class='callback_event_body' AND requested_table='callback_events' THEN
    RETURN EXISTS(SELECT 1 FROM public.callback_events WHERE id=requested_record AND tenant_id=requested_tenant AND merchant_id=requested_merchant);
  ELSIF requested_class='published_outbox_payload' AND requested_table='outbox_events' THEN
    RETURN EXISTS(SELECT 1 FROM public.outbox_events WHERE id=requested_record AND tenant_id=requested_tenant AND merchant_id=requested_merchant);
  ELSIF requested_class='event_history_payload' AND requested_table='event_history' THEN
    RETURN EXISTS(SELECT 1 FROM public.event_history WHERE event_id=requested_record AND tenant_id=requested_tenant AND merchant_id=requested_merchant);
  END IF;
  RETURN false;
END $$;
REVOKE ALL ON FUNCTION retention_control_record_exists(uuid,text,text,uuid,text,uuid) FROM PUBLIC;

CREATE FUNCTION request_retention_policy_change(
  requested_id uuid,requested_tenant uuid,requested_class text,expected_version bigint,expected_fence bigint,
  requested_archive integer,requested_grace integer,requested_lock integer,requested_prune boolean,
  requested_schedule timestamptz,requested_reason text,requested_actor uuid,requested_session text,
  requested_mfa timestamptz,idem_key text,idem_hash bytea
) RETURNS SETOF retention_policy_change_requests LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); replay uuid; inserted boolean; head public.retention_policy_heads%ROWTYPE; audit_id uuid:=gen_random_uuid(); outbox_id uuid:=gen_random_uuid();
BEGIN
  IF NOT public.retention_admin_allowed(requested_actor,requested_tenant,'retention:policy_request',requested_session,requested_mfa,true) THEN RAISE EXCEPTION 'retention policy request denied' USING ERRCODE='RA022'; END IF;
  IF requested_class NOT IN ('callback_event_body','published_outbox_payload','event_history_payload') OR requested_archive NOT BETWEEN 1 AND 3650 OR
     requested_grace NOT BETWEEN 1 AND 90 OR requested_lock NOT BETWEEN 30 AND 3650 OR requested_lock<=requested_grace OR
     requested_class IN ('callback_event_body','event_history_payload') AND requested_prune OR expected_version<0 OR expected_fence<0 OR
     requested_schedule<now_at OR requested_schedule>now_at+interval '365 days' OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR
     length(requested_session) NOT BETWEEN 1 AND 255 OR NOT public.retention_control_mfa_valid(requested_mfa,now_at) OR
     NOT EXISTS(SELECT 1 FROM public.tenants WHERE id=requested_tenant AND status='active') THEN
    RAISE EXCEPTION 'invalid retention policy request' USING ERRCODE='22023';
  END IF;
  SELECT resource_id,reserved INTO replay,inserted FROM public.retention_control_reserve_idempotency(requested_tenant,requested_actor,'retention.policy.request',idem_key,idem_hash,'policy_change',requested_id,now_at);
  IF NOT inserted THEN RETURN QUERY SELECT * FROM public.retention_policy_change_requests WHERE id=replay AND tenant_id=requested_tenant; RETURN; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_tenant::text||chr(31)||requested_class,0));
  SELECT * INTO head FROM public.retention_policy_heads WHERE tenant_id=requested_tenant AND data_class=requested_class;
  IF (NOT FOUND AND (expected_version<>0 OR expected_fence<>0)) OR (FOUND AND (head.policy_version<>expected_version OR head.fence<>expected_fence)) THEN
    RAISE EXCEPTION 'retention policy head conflict' USING ERRCODE='40001';
  END IF;
  INSERT INTO public.retention_policy_change_requests(id,tenant_id,data_class,expected_policy_version,expected_head_fence,archive_after_days,
    prune_grace_days,object_lock_days,prune_enabled,status,reason,requested_by,requested_session_id,requested_mfa_at,scheduled_for,expires_at,created_at,updated_at)
  VALUES(requested_id,requested_tenant,requested_class,expected_version,expected_fence,requested_archive,requested_grace,requested_lock,requested_prune,
    'pending_approval',btrim(requested_reason),requested_actor,requested_session,requested_mfa,requested_schedule,now_at+interval '30 minutes',now_at,now_at);
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_policy.requested','retention_policy_change',requested_id::text,btrim(requested_reason),
    jsonb_build_object('data_class',requested_class,'expected_policy_version',expected_version,'expected_head_fence',expected_fence,'archive_after_days',requested_archive,'prune_grace_days',requested_grace,'object_lock_days',requested_lock,'prune_enabled',requested_prune,'scheduled_for',requested_schedule),now_at);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_policy_change',requested_id::text,1,'platform_admin.retention_policy.requested',
    jsonb_build_object('request_id',requested_id,'tenant_id',requested_tenant,'data_class',requested_class,'status','pending_approval','row_version',1),now_at,now_at);
  RETURN QUERY SELECT * FROM public.retention_policy_change_requests WHERE id=requested_id;
END $$;
REVOKE ALL ON FUNCTION request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION decide_retention_policy_change(
  requested_id uuid,requested_tenant uuid,expected_row bigint,approve boolean,requested_reason text,
  requested_actor uuid,requested_session text,requested_mfa timestamptz,idem_key text,idem_hash bytea
) RETURNS SETOF retention_policy_change_requests LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); replay uuid; inserted boolean; change public.retention_policy_change_requests%ROWTYPE; action text; audit_id uuid:=gen_random_uuid(); outbox_id uuid:=gen_random_uuid();
BEGIN
  IF NOT public.retention_admin_allowed(requested_actor,requested_tenant,'retention:policy_approve',requested_session,requested_mfa,true) THEN RAISE EXCEPTION 'retention policy decision denied' USING ERRCODE='RA022'; END IF;
  IF expected_row<1 OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR length(requested_session) NOT BETWEEN 1 AND 255 OR NOT public.retention_control_mfa_valid(requested_mfa,now_at) THEN RAISE EXCEPTION 'invalid retention policy decision' USING ERRCODE='22023'; END IF;
  action:=CASE WHEN approve THEN 'retention.policy.approve' ELSE 'retention.policy.reject' END;
  SELECT resource_id,reserved INTO replay,inserted FROM public.retention_control_reserve_idempotency(requested_tenant,requested_actor,action,idem_key,idem_hash,'policy_change',requested_id,now_at);
  IF NOT inserted THEN RETURN QUERY SELECT * FROM public.retention_policy_change_requests WHERE id=replay AND tenant_id=requested_tenant; RETURN; END IF;
  SELECT * INTO change FROM public.retention_policy_change_requests WHERE id=requested_id AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'retention policy request not found' USING ERRCODE='P0002'; END IF;
  IF change.status='pending_approval' AND change.expires_at<=now_at THEN
    UPDATE public.retention_policy_change_requests SET status='expired',decision_reason='approval_window_expired',decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
    SELECT * INTO change FROM public.retention_policy_change_requests WHERE id=requested_id;
    PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_policy.expired','retention_policy_change',requested_id::text,'approval window expired',jsonb_build_object('data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at);
    INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_policy_change',requested_id::text,change.row_version,'platform_admin.retention_policy.expired',jsonb_build_object('request_id',requested_id,'tenant_id',requested_tenant,'data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at,now_at);
    RETURN NEXT change; RETURN;
  END IF;
  IF change.status<>'pending_approval' OR change.row_version<>expected_row OR change.requested_by=requested_actor THEN RAISE EXCEPTION 'retention policy decision conflict' USING ERRCODE='40001'; END IF;
  IF approve THEN
    UPDATE public.retention_policy_change_requests SET status='scheduled',approved_by=requested_actor,approved_session_id=requested_session,
      approved_mfa_at=requested_mfa,approved_at=now_at,decided_at=now_at,decision_reason=btrim(requested_reason),updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
    action:='approved';
  ELSE
    UPDATE public.retention_policy_change_requests SET status='rejected',rejected_by=requested_actor,rejected_session_id=requested_session,
      decision_reason=btrim(requested_reason),decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
    action:='rejected';
  END IF;
  SELECT * INTO change FROM public.retention_policy_change_requests WHERE id=requested_id;
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_policy.'||action,'retention_policy_change',requested_id::text,btrim(requested_reason),jsonb_build_object('data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_policy_change',requested_id::text,change.row_version,'platform_admin.retention_policy.'||action,jsonb_build_object('request_id',requested_id,'tenant_id',requested_tenant,'data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at,now_at);
  RETURN NEXT change;
END $$;
REVOKE ALL ON FUNCTION decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION create_retention_control_hold(
  requested_id uuid,requested_tenant uuid,requested_class text,requested_scope text,requested_merchant uuid,
  requested_table text,requested_record uuid,requested_case text,requested_reason text,requested_expires timestamptz,
  requested_actor uuid,requested_session text,requested_mfa timestamptz,idem_key text,idem_hash bytea
) RETURNS SETOF retention_legal_holds LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); replay uuid; inserted boolean; prior_write text:=current_setting('app.retention_hold_write',true); audit_id uuid:=gen_random_uuid(); outbox_id uuid:=gen_random_uuid();
BEGIN
  IF NOT public.retention_admin_allowed(requested_actor,requested_tenant,'retention:hold_create',requested_session,requested_mfa,true) THEN RAISE EXCEPTION 'retention hold creation denied' USING ERRCODE='RA022'; END IF;
  IF requested_class NOT IN ('callback_event_body','published_outbox_payload','event_history_payload') OR requested_scope NOT IN ('tenant','merchant','record') OR
     requested_case !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$' OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR
     length(requested_session) NOT BETWEEN 1 AND 255 OR NOT public.retention_control_mfa_valid(requested_mfa,now_at) OR
     requested_expires IS NOT NULL AND (requested_expires<now_at+interval '1 hour' OR requested_expires>now_at+interval '3650 days') OR
     NOT public.retention_control_record_exists(requested_tenant,requested_class,requested_scope,requested_merchant,requested_table,requested_record) THEN
    RAISE EXCEPTION 'invalid legal hold request' USING ERRCODE='22023';
  END IF;
  SELECT resource_id,reserved INTO replay,inserted FROM public.retention_control_reserve_idempotency(requested_tenant,requested_actor,'retention.hold.create',idem_key,idem_hash,'legal_hold',requested_id,now_at);
  IF NOT inserted THEN RETURN QUERY SELECT * FROM public.retention_legal_holds WHERE id=replay AND tenant_id=requested_tenant; RETURN; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_tenant::text||chr(31)||requested_class,0));
  PERFORM set_config('app.retention_hold_write','1',true);
  INSERT INTO public.retention_legal_holds(id,tenant_id,data_class,scope_type,merchant_id,source_table,source_record_id,case_reference,
    reason,actor_id,created_session_id,created_mfa_at,created_at,expires_at)
  VALUES(requested_id,requested_tenant,requested_class,requested_scope,requested_merchant,requested_table,requested_record,requested_case,
    btrim(requested_reason),requested_actor::text,requested_session,requested_mfa,now_at,requested_expires);
  PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_hold.created','retention_legal_hold',requested_id::text,btrim(requested_reason),jsonb_build_object('data_class',requested_class,'scope_type',requested_scope,'merchant_id',requested_merchant,'source_table',requested_table,'source_record_id',requested_record,'case_reference',requested_case,'expires_at',requested_expires),now_at);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_legal_hold',requested_id::text,1,'platform_admin.retention_hold.created',jsonb_build_object('hold_id',requested_id,'tenant_id',requested_tenant,'data_class',requested_class,'scope_type',requested_scope,'case_reference',requested_case,'status','active','version',1),now_at,now_at);
  RETURN QUERY SELECT * FROM public.retention_legal_holds WHERE id=requested_id;
EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  RAISE;
END $$;
REVOKE ALL ON FUNCTION create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION request_retention_hold_release(
  requested_id uuid,requested_tenant uuid,requested_hold uuid,expected_hold bigint,requested_reason text,
  requested_actor uuid,requested_session text,requested_mfa timestamptz,idem_key text,idem_hash bytea
) RETURNS SETOF retention_hold_release_requests LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); replay uuid; inserted boolean; held public.retention_legal_holds%ROWTYPE; audit_id uuid:=gen_random_uuid(); outbox_id uuid:=gen_random_uuid();
BEGIN
  IF NOT public.retention_admin_allowed(requested_actor,requested_tenant,'retention:hold_release',requested_session,requested_mfa,true) THEN RAISE EXCEPTION 'retention hold release request denied' USING ERRCODE='RA022'; END IF;
  IF expected_hold<1 OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR length(requested_session) NOT BETWEEN 1 AND 255 OR NOT public.retention_control_mfa_valid(requested_mfa,now_at) THEN RAISE EXCEPTION 'invalid legal hold release request' USING ERRCODE='22023'; END IF;
  SELECT resource_id,reserved INTO replay,inserted FROM public.retention_control_reserve_idempotency(requested_tenant,requested_actor,'retention.hold_release.request',idem_key,idem_hash,'hold_release',requested_id,now_at);
  IF NOT inserted THEN RETURN QUERY SELECT * FROM public.retention_hold_release_requests WHERE id=replay AND tenant_id=requested_tenant; RETURN; END IF;
  SELECT * INTO held FROM public.retention_legal_holds WHERE id=requested_hold AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'legal hold not found' USING ERRCODE='P0002'; END IF;
  IF held.version<>expected_hold OR held.released_at IS NOT NULL OR held.expired_at IS NOT NULL THEN RAISE EXCEPTION 'legal hold release conflict' USING ERRCODE='40001'; END IF;
  INSERT INTO public.retention_hold_release_requests(id,tenant_id,hold_id,expected_hold_version,status,reason,requested_by,requested_session_id,requested_mfa_at,created_at,expires_at,updated_at)
  VALUES(requested_id,requested_tenant,requested_hold,expected_hold,'pending_approval',btrim(requested_reason),requested_actor,requested_session,requested_mfa,now_at,now_at+interval '30 minutes',now_at);
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_hold_release.requested','retention_hold_release',requested_id::text,btrim(requested_reason),jsonb_build_object('hold_id',requested_hold,'hold_version',expected_hold,'case_reference',held.case_reference),now_at);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_hold_release',requested_id::text,1,'platform_admin.retention_hold_release.requested',jsonb_build_object('request_id',requested_id,'hold_id',requested_hold,'tenant_id',requested_tenant,'status','pending_approval','row_version',1),now_at,now_at);
  RETURN QUERY SELECT * FROM public.retention_hold_release_requests WHERE id=requested_id;
END $$;
REVOKE ALL ON FUNCTION request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION decide_retention_hold_release(
  requested_id uuid,requested_tenant uuid,expected_row bigint,approve boolean,requested_reason text,
  requested_actor uuid,requested_session text,requested_mfa timestamptz,idem_key text,idem_hash bytea
) RETURNS SETOF retention_hold_release_requests LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); replay uuid; inserted boolean; release public.retention_hold_release_requests%ROWTYPE; held public.retention_legal_holds%ROWTYPE; prior_write text:=current_setting('app.retention_hold_write',true); action text; audit_id uuid:=gen_random_uuid(); outbox_id uuid:=gen_random_uuid();
BEGIN
  IF NOT public.retention_admin_allowed(requested_actor,requested_tenant,'retention:hold_release',requested_session,requested_mfa,true) THEN RAISE EXCEPTION 'retention hold release decision denied' USING ERRCODE='RA022'; END IF;
  IF expected_row<1 OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR length(requested_session) NOT BETWEEN 1 AND 255 OR NOT public.retention_control_mfa_valid(requested_mfa,now_at) THEN RAISE EXCEPTION 'invalid legal hold release decision' USING ERRCODE='22023'; END IF;
  action:=CASE WHEN approve THEN 'retention.hold_release.approve' ELSE 'retention.hold_release.reject' END;
  SELECT resource_id,reserved INTO replay,inserted FROM public.retention_control_reserve_idempotency(requested_tenant,requested_actor,action,idem_key,idem_hash,'hold_release',requested_id,now_at);
  IF NOT inserted THEN RETURN QUERY SELECT * FROM public.retention_hold_release_requests WHERE id=replay AND tenant_id=requested_tenant; RETURN; END IF;
  SELECT * INTO release FROM public.retention_hold_release_requests WHERE id=requested_id AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'legal hold release request not found' USING ERRCODE='P0002'; END IF;
  SELECT * INTO held FROM public.retention_legal_holds WHERE id=release.hold_id AND tenant_id=requested_tenant FOR UPDATE;
  IF release.status='pending_approval' AND release.expires_at<=now_at THEN
    UPDATE public.retention_hold_release_requests SET status='expired',decision_reason='approval_window_expired',decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
    SELECT * INTO release FROM public.retention_hold_release_requests WHERE id=requested_id;
    PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_hold_release.expired','retention_hold_release',requested_id::text,'approval window expired',jsonb_build_object('hold_id',release.hold_id,'status',release.status,'row_version',release.row_version),now_at);
    INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_hold_release',requested_id::text,release.row_version,'platform_admin.retention_hold_release.expired',jsonb_build_object('request_id',requested_id,'hold_id',release.hold_id,'tenant_id',requested_tenant,'status',release.status,'row_version',release.row_version),now_at,now_at);
    RETURN NEXT release; RETURN;
  END IF;
  IF release.status<>'pending_approval' OR release.row_version<>expected_row OR release.requested_by=requested_actor OR held.actor_id=requested_actor::text THEN RAISE EXCEPTION 'legal hold release decision conflict' USING ERRCODE='40001'; END IF;
  IF approve THEN
    IF held.version<>release.expected_hold_version OR held.released_at IS NOT NULL OR held.expired_at IS NOT NULL THEN
      UPDATE public.retention_hold_release_requests SET status='conflict',approved_by=requested_actor,approved_session_id=requested_session,approved_mfa_at=requested_mfa,decision_reason='hold_version_conflict',decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
      action:='conflict';
    ELSE
      PERFORM set_config('app.retention_hold_write','1',true);
      UPDATE public.retention_legal_holds SET released_at=now_at,released_by=requested_actor::text,release_reason=btrim(requested_reason),version=version+1 WHERE id=held.id;
      PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
      UPDATE public.retention_hold_release_requests SET status='completed',approved_by=requested_actor,approved_session_id=requested_session,approved_mfa_at=requested_mfa,decision_reason=btrim(requested_reason),decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
      action:='completed';
    END IF;
  ELSE
    UPDATE public.retention_hold_release_requests SET status='rejected',rejected_by=requested_actor,rejected_session_id=requested_session,decision_reason=btrim(requested_reason),decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=requested_id;
    action:='rejected';
  END IF;
  SELECT * INTO release FROM public.retention_hold_release_requests WHERE id=requested_id;
  PERFORM public.append_platform_admin_audit(audit_id,requested_tenant,requested_actor,requested_session,'retention_hold_release.'||action,'retention_hold_release',requested_id::text,btrim(requested_reason),jsonb_build_object('hold_id',release.hold_id,'status',release.status,'row_version',release.row_version),now_at);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(outbox_id,public.platform_scope_uuid(requested_tenant),requested_tenant,'retention_hold_release',requested_id::text,release.row_version,'platform_admin.retention_hold_release.'||action,jsonb_build_object('request_id',requested_id,'hold_id',release.hold_id,'tenant_id',requested_tenant,'status',release.status,'row_version',release.row_version),now_at,now_at);
  RETURN NEXT release;
EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  RAISE;
END $$;
REVOKE ALL ON FUNCTION decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

-- Expired holds remain active for prune admission until this explicit audited
-- transition records immutable expiry evidence.
-- The second-cycle grace transition remains owned by 000015's
-- retention_advance_prune; this replacement changes only hold admission.
CREATE OR REPLACE FUNCTION retention_prune_admitted(p_batch uuid,p_now timestamptz)
RETURNS boolean LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE batch public.retention_archive_batches%ROWTYPE; blocked boolean; item_count integer; source_count integer;
BEGIN
  SELECT * INTO batch FROM public.retention_archive_batches WHERE id=p_batch;
  IF NOT FOUND OR batch.status NOT IN ('verified','grace','leased') OR batch.verified_at IS NULL THEN RETURN false; END IF;
  IF NOT EXISTS(SELECT 1 FROM public.retention_archive_objects o WHERE o.batch_id=p_batch AND o.object_lock_mode='COMPLIANCE' AND o.retention_until>p_now AND o.object_sha256 IS NOT NULL AND o.object_version<>'') THEN RETURN false; END IF;
  SELECT EXISTS(SELECT 1 FROM public.retention_archive_batch_items i JOIN public.retention_legal_holds h
    ON h.tenant_id=i.tenant_id AND h.data_class=batch.data_class AND h.released_at IS NULL AND h.expired_at IS NULL
    AND (h.scope_type='tenant' OR h.scope_type='merchant' AND h.merchant_id=i.merchant_id OR h.scope_type='record' AND h.merchant_id=i.merchant_id AND h.source_table=i.source_table AND h.source_record_id=i.source_record_id)
    WHERE i.batch_id=p_batch) INTO blocked;
  IF blocked THEN RETURN false; END IF;
  SELECT count(*) INTO item_count FROM public.retention_archive_batch_items WHERE batch_id=p_batch;
  IF batch.data_class='published_outbox_payload' THEN
    SELECT count(*) INTO source_count FROM public.retention_archive_batch_items i JOIN public.outbox_events e ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id JOIN public.event_history h ON h.event_id=e.id AND h.tenant_id=e.tenant_id
      WHERE i.batch_id=p_batch AND e.published_at IS NOT NULL AND e.published_at<=batch.cutoff_at AND e.payload_tombstone_version=0 AND h.payload=e.payload AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest;
  ELSE RETURN false; END IF;
  RETURN item_count>0 AND source_count=item_count;
END $$;
REVOKE ALL ON FUNCTION retention_prune_admitted(uuid,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_control_advance_due(requested_worker text,requested_limit integer)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE now_at timestamptz:=clock_timestamp(); processed integer:=0; change public.retention_policy_change_requests%ROWTYPE; release public.retention_hold_release_requests%ROWTYPE; head public.retention_policy_heads%ROWTYPE; head_exists boolean; current_policy public.retention_policy_versions%ROWTYPE; next_version bigint; new_policy uuid; active_holds boolean; max_lock timestamptz; held public.retention_legal_holds%ROWTYPE; prior_write text:=current_setting('app.retention_hold_write',true); audit_id uuid; outbox_id uuid; system_actor uuid:='00000000-0000-0000-0000-000000000022'::uuid; session_name text;
BEGIN
  IF requested_worker !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$' OR requested_limit NOT BETWEEN 1 AND 100 THEN RAISE EXCEPTION 'invalid retention control worker' USING ERRCODE='22023'; END IF;
  session_name:='retention-control-worker:'||requested_worker;
  FOR change IN SELECT * FROM public.retention_policy_change_requests WHERE status='pending_approval' AND expires_at<=now_at ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT requested_limit LOOP
    UPDATE public.retention_policy_change_requests SET status='expired',decision_reason='approval_window_expired',decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=change.id RETURNING * INTO change;
    audit_id:=gen_random_uuid(); outbox_id:=gen_random_uuid();
    PERFORM public.append_platform_admin_audit(audit_id,change.tenant_id,system_actor,session_name,'retention_policy.expired','retention_policy_change',change.id::text,'approval window expired',jsonb_build_object('data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at);
    INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(change.tenant_id),change.tenant_id,'retention_policy_change',change.id::text,change.row_version,'platform_admin.retention_policy.expired',jsonb_build_object('request_id',change.id,'tenant_id',change.tenant_id,'data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at,now_at);
    processed:=processed+1;
  END LOOP;
  FOR release IN SELECT * FROM public.retention_hold_release_requests WHERE status='pending_approval' AND expires_at<=now_at ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT greatest(requested_limit-processed,0) LOOP
    UPDATE public.retention_hold_release_requests SET status='expired',decision_reason='approval_window_expired',decided_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=release.id RETURNING * INTO release;
    audit_id:=gen_random_uuid(); outbox_id:=gen_random_uuid();
    PERFORM public.append_platform_admin_audit(audit_id,release.tenant_id,system_actor,session_name,'retention_hold_release.expired','retention_hold_release',release.id::text,'approval window expired',jsonb_build_object('hold_id',release.hold_id,'status',release.status,'row_version',release.row_version),now_at);
    INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(release.tenant_id),release.tenant_id,'retention_hold_release',release.id::text,release.row_version,'platform_admin.retention_hold_release.expired',jsonb_build_object('request_id',release.id,'hold_id',release.hold_id,'tenant_id',release.tenant_id,'status',release.status,'row_version',release.row_version),now_at,now_at);
    processed:=processed+1;
  END LOOP;
  FOR change IN SELECT * FROM public.retention_policy_change_requests WHERE status='scheduled' AND scheduled_for<=now_at AND approved_at IS NOT NULL AND approved_at<=now_at ORDER BY scheduled_for,id FOR UPDATE SKIP LOCKED LIMIT greatest(requested_limit-processed,0) LOOP
    PERFORM pg_advisory_xact_lock(hashtextextended(change.tenant_id::text||chr(31)||change.data_class,0));
    SELECT * INTO head FROM public.retention_policy_heads WHERE tenant_id=change.tenant_id AND data_class=change.data_class FOR UPDATE;
    head_exists:=FOUND;
    IF (NOT head_exists AND (change.expected_policy_version<>0 OR change.expected_head_fence<>0)) OR (head_exists AND (head.policy_version<>change.expected_policy_version OR head.fence<>change.expected_head_fence)) THEN
      UPDATE public.retention_policy_change_requests SET status='conflict',decision_reason='stale_policy_head',updated_at=now_at,row_version=row_version+1 WHERE id=change.id;
    ELSE
      IF head_exists THEN SELECT * INTO current_policy FROM public.retention_policy_versions WHERE id=head.policy_id; ELSE current_policy:=NULL; END IF;
      SELECT EXISTS(SELECT 1 FROM public.retention_legal_holds WHERE tenant_id=change.tenant_id AND data_class=change.data_class AND released_at IS NULL AND expired_at IS NULL) INTO active_holds;
      SELECT max(o.retention_until) INTO max_lock FROM public.retention_archive_objects o JOIN public.retention_archive_batches b ON b.id=o.batch_id WHERE o.tenant_id=change.tenant_id AND b.data_class=change.data_class;
      IF (max_lock IS NOT NULL AND now_at+make_interval(days=>change.object_lock_days)<max_lock) OR
         (active_holds AND head_exists AND (change.archive_after_days<current_policy.archive_after_days OR change.prune_grace_days<current_policy.prune_grace_days OR change.object_lock_days<current_policy.object_lock_days OR change.prune_enabled AND NOT current_policy.prune_enabled)) THEN
        UPDATE public.retention_policy_change_requests SET status='conflict',decision_reason='retention_floor_or_hold_conflict',updated_at=now_at,row_version=row_version+1 WHERE id=change.id;
      ELSE
        SELECT coalesce(max(version),0)+1 INTO next_version FROM public.retention_policy_versions WHERE tenant_id=change.tenant_id AND data_class=change.data_class;
        new_policy:=gen_random_uuid();
        IF NOT public.create_retention_policy_version(new_policy,change.tenant_id,change.data_class,next_version,change.archive_after_days,change.prune_grace_days,change.object_lock_days,change.prune_enabled,now_at,system_actor::text,now_at) THEN RAISE EXCEPTION 'retention policy activation rejected'; END IF;
        INSERT INTO public.retention_policy_heads(tenant_id,data_class,policy_id,policy_version,fence,activated_at) VALUES(change.tenant_id,change.data_class,new_policy,next_version,1,now_at)
          ON CONFLICT(tenant_id,data_class) DO UPDATE SET policy_id=excluded.policy_id,policy_version=excluded.policy_version,fence=retention_policy_heads.fence+1,activated_at=excluded.activated_at;
        UPDATE public.retention_policy_change_requests SET status='active',activated_policy_id=new_policy,activated_at=now_at,updated_at=now_at,row_version=row_version+1 WHERE id=change.id;
      END IF;
    END IF;
    SELECT * INTO change FROM public.retention_policy_change_requests WHERE id=change.id;
    audit_id:=gen_random_uuid(); outbox_id:=gen_random_uuid();
    PERFORM public.append_platform_admin_audit(audit_id,change.tenant_id,system_actor,session_name,'retention_policy.'||change.status,'retention_policy_change',change.id::text,coalesce(change.decision_reason,change.reason),jsonb_build_object('data_class',change.data_class,'status',change.status,'activated_policy_id',change.activated_policy_id,'row_version',change.row_version),now_at);
    INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(change.tenant_id),change.tenant_id,'retention_policy_change',change.id::text,change.row_version,'platform_admin.retention_policy.'||change.status,jsonb_build_object('request_id',change.id,'tenant_id',change.tenant_id,'data_class',change.data_class,'status',change.status,'row_version',change.row_version),now_at,now_at);
    processed:=processed+1;
  END LOOP;
  FOR held IN SELECT * FROM public.retention_legal_holds WHERE released_at IS NULL AND expired_at IS NULL AND expires_at IS NOT NULL AND expires_at<=now_at ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT greatest(requested_limit-processed,0) LOOP
    PERFORM set_config('app.retention_hold_write','1',true);
    UPDATE public.retention_legal_holds SET expired_at=now_at,expired_by=system_actor::text,expiry_reason='scheduled legal hold expiry',version=version+1 WHERE id=held.id AND released_at IS NULL AND expired_at IS NULL AND expires_at<=now_at;
    PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
    IF FOUND THEN
      audit_id:=gen_random_uuid(); outbox_id:=gen_random_uuid();
      PERFORM public.append_platform_admin_audit(audit_id,held.tenant_id,system_actor,session_name,'retention_hold.expired','retention_legal_hold',held.id::text,'scheduled legal hold expiry',jsonb_build_object('hold_id',held.id,'data_class',held.data_class,'case_reference',held.case_reference,'expired_at',now_at,'version',held.version+1),now_at);
      INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at) VALUES(outbox_id,public.platform_scope_uuid(held.tenant_id),held.tenant_id,'retention_legal_hold',held.id::text,held.version+1,'platform_admin.retention_hold.expired',jsonb_build_object('hold_id',held.id,'tenant_id',held.tenant_id,'data_class',held.data_class,'case_reference',held.case_reference,'status','expired','version',held.version+1),now_at,now_at);
      processed:=processed+1;
    END IF;
  END LOOP;
  PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  INSERT INTO public.retention_control_worker_heartbeats(worker_id,last_cycle_at,processed,version)
  VALUES(requested_worker,now_at,processed,1)
  ON CONFLICT(worker_id) DO UPDATE SET last_cycle_at=excluded.last_cycle_at,processed=excluded.processed,version=retention_control_worker_heartbeats.version+1;
  RETURN processed;
EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  RAISE;
END $$;
REVOKE ALL ON FUNCTION retention_control_advance_due(text,integer) FROM PUBLIC;

CREATE FUNCTION retention_control_worker_health(stale_after_seconds integer)
RETURNS TABLE(last_cycle_at timestamptz,due_policy_changes bigint,due_hold_releases bigint,due_hold_expiries bigint,ready boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  WITH heartbeat AS (
    SELECT max(h.last_cycle_at) AS last_cycle_at FROM public.retention_control_worker_heartbeats h
  ), due AS (
    SELECT
      (SELECT count(*) FROM public.retention_policy_change_requests WHERE (status='pending_approval' AND expires_at<=clock_timestamp()) OR (status='scheduled' AND scheduled_for<=clock_timestamp())) AS due_policy_changes,
      (SELECT count(*) FROM public.retention_hold_release_requests WHERE status='pending_approval' AND expires_at<=clock_timestamp()) AS due_hold_releases,
      (SELECT count(*) FROM public.retention_legal_holds WHERE released_at IS NULL AND expired_at IS NULL AND expires_at IS NOT NULL AND expires_at<=clock_timestamp()) AS due_hold_expiries
  )
  SELECT heartbeat.last_cycle_at,due.due_policy_changes,due.due_hold_releases,due.due_hold_expiries,
    stale_after_seconds BETWEEN 5 AND 3600 AND heartbeat.last_cycle_at IS NOT NULL AND heartbeat.last_cycle_at>=clock_timestamp()-make_interval(secs=>stale_after_seconds)
  FROM heartbeat CROSS JOIN due
$$;
REVOKE ALL ON FUNCTION retention_control_worker_health(integer) FROM PUBLIC;

-- Secret-free query capabilities. Object keys, versions, source payloads and
-- archive credentials are intentionally absent from every return shape.
CREATE FUNCTION retention_control_effective_policies(requested_tenant uuid)
RETURNS TABLE(id uuid,tenant_id uuid,data_class text,version bigint,archive_after_days integer,prune_grace_days integer,object_lock_days integer,prune_enabled boolean,effective_at timestamptz,policy_digest text,head_fence bigint,last_activated_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT p.id,p.tenant_id,p.data_class,p.version,p.archive_after_days,p.prune_grace_days,p.object_lock_days,p.prune_enabled,p.effective_at,encode(p.policy_digest,'hex'),h.fence,h.activated_at FROM public.retention_policy_heads h JOIN public.retention_policy_versions p ON p.id=h.policy_id WHERE h.tenant_id=requested_tenant AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false) ORDER BY p.data_class
$$;
CREATE FUNCTION retention_control_policy_changes(requested_tenant uuid,after_id uuid,page_size integer) RETURNS SETOF retention_policy_change_requests LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$ SELECT * FROM public.retention_policy_change_requests WHERE tenant_id=requested_tenant AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false) AND (after_id IS NULL OR id>after_id) ORDER BY id LIMIT page_size $$;
CREATE FUNCTION retention_control_holds(requested_tenant uuid,after_id uuid,page_size integer)
RETURNS TABLE(id uuid,tenant_id uuid,data_class text,scope_type text,merchant_id uuid,source_table text,source_record_id uuid,reason text,actor_id text,created_at timestamptz,expires_at timestamptz,released_at timestamptz,released_by text,release_reason text,version bigint,case_reference text,created_session_id text,created_mfa_at timestamptz,expired_at timestamptz,expired_by text,expiry_reason text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT h.id,h.tenant_id,h.data_class,h.scope_type,h.merchant_id,h.source_table,h.source_record_id,h.reason,h.actor_id,h.created_at,h.expires_at,h.released_at,h.released_by,h.release_reason,h.version,
   CASE WHEN public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:hold_create',current_setting('app.retention_session_id',true),NULL,false)
          OR public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:hold_release',current_setting('app.retention_session_id',true),NULL,false)
     THEN h.case_reference ELSE NULL END,
   h.created_session_id,h.created_mfa_at,h.expired_at,h.expired_by,h.expiry_reason
 FROM public.retention_legal_holds h
 WHERE h.tenant_id=requested_tenant
   AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false)
   AND (after_id IS NULL OR h.id>after_id)
 ORDER BY h.id LIMIT page_size
$$;
CREATE FUNCTION retention_control_hold_releases(requested_tenant uuid,after_id uuid,page_size integer) RETURNS SETOF retention_hold_release_requests LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$ SELECT * FROM public.retention_hold_release_requests WHERE tenant_id=requested_tenant AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false) AND (after_id IS NULL OR id>after_id) ORDER BY id LIMIT page_size $$;
CREATE FUNCTION retention_control_batches(requested_tenant uuid,after_id uuid,page_size integer)
RETURNS TABLE(id uuid,data_class text,policy_version bigint,status text,item_count bigint,object_sha256 text,manifest_sha256 text,signing_key_id text,object_retention_until timestamptz,verified_at timestamptz,pruned_at timestamptz,created_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT b.id,b.data_class,b.policy_version,b.status,count(i.ordinal),encode(o.object_sha256,'hex'),encode(o.manifest_sha256,'hex'),o.signing_key_id,o.retention_until,b.verified_at,b.pruned_at,b.created_at FROM public.retention_archive_batches b LEFT JOIN public.retention_archive_batch_items i ON i.batch_id=b.id LEFT JOIN public.retention_archive_objects o ON o.batch_id=b.id WHERE b.tenant_id=requested_tenant AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false) AND (after_id IS NULL OR b.id>after_id) GROUP BY b.id,o.object_sha256,o.manifest_sha256,o.signing_key_id,o.retention_until ORDER BY b.id LIMIT page_size
$$;
CREATE FUNCTION retention_control_tombstones(requested_tenant uuid,after_id uuid,page_size integer)
RETURNS TABLE(data_class text,source_table text,source_record_id uuid,merchant_id uuid,original_sha256 text,batch_id uuid,archived_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT data_class,source_table,source_record_id,merchant_id,encode(original_digest,'hex'),batch_id,archived_at FROM public.retention_archive_index WHERE tenant_id=requested_tenant AND public.retention_admin_allowed(nullif(current_setting('app.retention_actor_id',true),'')::uuid,requested_tenant,'retention:read',current_setting('app.retention_session_id',true),NULL,false) AND (after_id IS NULL OR source_record_id>after_id) ORDER BY source_record_id LIMIT page_size
$$;

REVOKE ALL ON FUNCTION retention_control_effective_policies(uuid),retention_control_policy_changes(uuid,uuid,integer),retention_control_holds(uuid,uuid,integer),retention_control_hold_releases(uuid,uuid,integer),retention_control_batches(uuid,uuid,integer),retention_control_tombstones(uuid,uuid,integer) FROM PUBLIC;

INSERT INTO admin_permissions(permission_key,description) VALUES
('retention:read','Read redacted retention policy, hold and archive evidence'),
('retention:policy_request','Request a versioned retention policy change'),
('retention:policy_approve','Independently approve a retention policy change'),
('retention:hold_create','Create an immediate legal hold'),
('retention:hold_release','Request or independently approve legal hold release');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('auditor','retention:read'),('security_admin','retention:read'),('security_admin','retention:policy_request'),('security_admin','retention:hold_create'),('security_admin','retention:hold_release'),
('senior_approver','retention:read'),('senior_approver','retention:policy_approve'),('senior_approver','retention:hold_release');

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime') THEN
    REVOKE ALL PRIVILEGES ON retention_policy_heads,retention_policy_change_requests,retention_hold_release_requests,retention_control_idempotency FROM platform_admin_runtime;
    REVOKE ALL PRIVILEGES ON retention_policy_versions,retention_legal_holds,retention_archive_jobs,retention_archive_batches,retention_archive_batch_items,retention_archive_objects,retention_archive_index FROM platform_admin_runtime;
    REVOKE EXECUTE ON FUNCTION create_retention_policy_version(uuid,uuid,text,bigint,integer,integer,integer,boolean,timestamptz,text,timestamptz),
      create_retention_legal_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,timestamptz),
      release_retention_legal_hold(uuid,text,text,timestamptz) FROM platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea),
      decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
      create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea),
      request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea),
      decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),
      retention_control_effective_policies(uuid),
      retention_control_policy_changes(uuid,uuid,integer),retention_control_holds(uuid,uuid,integer),
      retention_control_hold_releases(uuid,uuid,integer),retention_control_batches(uuid,uuid,integer),
      retention_control_tombstones(uuid,uuid,integer),retention_control_worker_health(integer) TO platform_admin_runtime;
    REVOKE EXECUTE ON FUNCTION retention_control_advance_due(text,integer) FROM platform_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='retention_control_scheduler') THEN
    REVOKE ALL PRIVILEGES ON retention_policy_heads,retention_policy_change_requests,retention_hold_release_requests,
      retention_control_idempotency,retention_policy_versions,retention_legal_holds,retention_archive_jobs,
      retention_archive_batches,retention_archive_batch_items,retention_archive_objects,retention_archive_index
      FROM retention_control_scheduler;
    GRANT EXECUTE ON FUNCTION retention_control_advance_due(text,integer),retention_control_worker_health(integer) TO retention_control_scheduler;
  END IF;
END $$;

COMMIT;
