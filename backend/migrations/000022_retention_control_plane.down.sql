BEGIN;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM retention_policy_change_requests) OR
     EXISTS(SELECT 1 FROM retention_hold_release_requests) OR
     EXISTS(SELECT 1 FROM retention_control_idempotency) OR
     EXISTS(SELECT 1 FROM retention_legal_holds WHERE created_session_id IS NOT NULL OR expired_at IS NOT NULL) THEN
    RAISE EXCEPTION 'cannot roll back retention control plane while policy/hold control evidence exists';
  END IF;
END $$;

DELETE FROM admin_role_permissions WHERE permission_key IN ('retention:read','retention:policy_request','retention:policy_approve','retention:hold_create','retention:hold_release');
DELETE FROM admin_permissions WHERE permission_key IN ('retention:read','retention:policy_request','retention:policy_approve','retention:hold_create','retention:hold_release');

DROP FUNCTION IF EXISTS retention_control_tombstones(uuid,uuid,integer);
DROP FUNCTION IF EXISTS retention_control_batches(uuid,uuid,integer);
DROP FUNCTION IF EXISTS retention_control_hold_releases(uuid,uuid,integer);
DROP FUNCTION IF EXISTS retention_control_holds(uuid,uuid,integer);
DROP FUNCTION IF EXISTS retention_control_policy_changes(uuid,uuid,integer);
DROP FUNCTION IF EXISTS retention_control_effective_policies(uuid);
DROP FUNCTION IF EXISTS retention_control_worker_health(integer);
DROP FUNCTION IF EXISTS retention_control_advance_due(text,integer);
DROP FUNCTION IF EXISTS decide_retention_hold_release(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea);
DROP FUNCTION IF EXISTS request_retention_hold_release(uuid,uuid,uuid,bigint,text,uuid,text,timestamptz,text,bytea);
DROP FUNCTION IF EXISTS create_retention_control_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,uuid,text,timestamptz,text,bytea);
DROP FUNCTION IF EXISTS decide_retention_policy_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea);
DROP FUNCTION IF EXISTS request_retention_policy_change(uuid,uuid,text,bigint,bigint,integer,integer,integer,boolean,timestamptz,text,uuid,text,timestamptz,text,bytea);
DROP FUNCTION IF EXISTS retention_control_record_exists(uuid,text,text,uuid,text,uuid);
DROP FUNCTION IF EXISTS retention_admin_allowed(uuid,uuid,text,text,timestamptz,boolean);
DROP FUNCTION IF EXISTS retention_control_mfa_valid(timestamptz,timestamptz);
DROP FUNCTION IF EXISTS retention_control_reserve_idempotency(uuid,uuid,text,text,bytea,text,uuid,timestamptz);

DROP TABLE IF EXISTS retention_control_idempotency;
DROP TABLE IF EXISTS retention_control_worker_heartbeats;
DROP TABLE IF EXISTS retention_hold_release_requests;
DROP TABLE IF EXISTS retention_policy_change_requests;
DROP TABLE IF EXISTS retention_policy_heads;
DROP FUNCTION IF EXISTS retention_control_immutable();

DROP INDEX IF EXISTS retention_legal_holds_active_idx;
ALTER TABLE retention_legal_holds DROP CONSTRAINT IF EXISTS retention_hold_terminal_shape;
ALTER TABLE retention_legal_holds DROP CONSTRAINT IF EXISTS retention_hold_creator_session_shape;
ALTER TABLE retention_legal_holds DROP CONSTRAINT IF EXISTS retention_hold_case_reference_shape;
ALTER TABLE retention_legal_holds DROP CONSTRAINT IF EXISTS retention_legal_holds_tenant_identity;
ALTER TABLE retention_legal_holds DROP COLUMN IF EXISTS expiry_reason,DROP COLUMN IF EXISTS expired_by,DROP COLUMN IF EXISTS expired_at,
  DROP COLUMN IF EXISTS created_mfa_at,DROP COLUMN IF EXISTS created_session_id,DROP COLUMN IF EXISTS case_reference;
ALTER TABLE retention_legal_holds ADD CONSTRAINT retention_legal_holds_check2 CHECK(
  (released_at IS NULL AND released_by IS NULL AND release_reason IS NULL) OR
  (released_at IS NOT NULL AND released_by IS NOT NULL AND length(released_by) BETWEEN 1 AND 255
    AND release_reason IS NOT NULL AND length(release_reason) BETWEEN 8 AND 2048)
);
CREATE INDEX retention_legal_holds_active_idx ON retention_legal_holds(tenant_id,data_class,expires_at) WHERE released_at IS NULL;

CREATE OR REPLACE FUNCTION retention_guard_legal_hold_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF current_setting('app.retention_hold_write',true)<>'1' THEN RAISE EXCEPTION 'legal holds may change only through fenced retention functions' USING ERRCODE='42501'; END IF;
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'legal holds cannot be deleted' USING ERRCODE='55000'; END IF;
  IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.data_class<>OLD.data_class OR NEW.scope_type<>OLD.scope_type OR NEW.merchant_id IS DISTINCT FROM OLD.merchant_id OR NEW.source_table IS DISTINCT FROM OLD.source_table OR NEW.source_record_id IS DISTINCT FROM OLD.source_record_id OR NEW.reason<>OLD.reason OR NEW.actor_id<>OLD.actor_id OR NEW.created_at<>OLD.created_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR OLD.released_at IS NOT NULL OR NEW.version<>OLD.version+1) THEN RAISE EXCEPTION 'legal hold identity is immutable' USING ERRCODE='55000'; END IF;
  RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION retention_prune_admitted(p_batch uuid,p_now timestamptz)
RETURNS boolean LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE batch public.retention_archive_batches%ROWTYPE; blocked boolean; item_count integer; source_count integer;
BEGIN
  SELECT * INTO batch FROM public.retention_archive_batches WHERE id=p_batch;
  IF NOT FOUND OR batch.status NOT IN ('verified','grace','leased') OR batch.verified_at IS NULL THEN RETURN false; END IF;
  IF NOT EXISTS(SELECT 1 FROM public.retention_archive_objects o WHERE o.batch_id=p_batch AND o.object_lock_mode='COMPLIANCE' AND o.retention_until>p_now AND o.object_sha256 IS NOT NULL AND o.object_version<>'') THEN RETURN false; END IF;
  SELECT EXISTS(SELECT 1 FROM public.retention_archive_batch_items i JOIN public.retention_legal_holds h ON h.tenant_id=i.tenant_id AND h.data_class=batch.data_class AND h.released_at IS NULL AND (h.expires_at IS NULL OR h.expires_at>p_now) AND (h.scope_type='tenant' OR h.scope_type='merchant' AND h.merchant_id=i.merchant_id OR h.scope_type='record' AND h.merchant_id=i.merchant_id AND h.source_table=i.source_table AND h.source_record_id=i.source_record_id) WHERE i.batch_id=p_batch) INTO blocked;
  IF blocked THEN RETURN false; END IF;
  SELECT count(*) INTO item_count FROM public.retention_archive_batch_items WHERE batch_id=p_batch;
  IF batch.data_class='published_outbox_payload' THEN SELECT count(*) INTO source_count FROM public.retention_archive_batch_items i JOIN public.outbox_events e ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id JOIN public.event_history h ON h.event_id=e.id AND h.tenant_id=e.tenant_id WHERE i.batch_id=p_batch AND e.published_at IS NOT NULL AND e.published_at<=batch.cutoff_at AND e.payload_tombstone_version=0 AND h.payload=e.payload AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest; ELSE RETURN false; END IF;
  RETURN item_count>0 AND source_count=item_count;
END $$;

COMMIT;
