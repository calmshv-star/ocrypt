BEGIN;

CREATE TABLE retention_policy_versions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    data_class text NOT NULL CHECK (data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    version bigint NOT NULL CHECK (version > 0),
    archive_after_days integer NOT NULL CHECK (archive_after_days BETWEEN 1 AND 3650),
    prune_grace_days integer NOT NULL CHECK (prune_grace_days BETWEEN 1 AND 90),
    object_lock_days integer NOT NULL CHECK (object_lock_days BETWEEN 30 AND 3650),
    prune_enabled boolean NOT NULL,
    effective_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    created_by text NOT NULL CHECK (length(created_by) BETWEEN 1 AND 255),
    policy_digest bytea NOT NULL CHECK (octet_length(policy_digest)=32),
    UNIQUE (tenant_id,data_class,version),
    UNIQUE (id,tenant_id,data_class,version),
    UNIQUE (id,tenant_id,data_class),
    CHECK (data_class NOT IN ('callback_event_body','event_history_payload') OR NOT prune_enabled),
    CHECK (object_lock_days>prune_grace_days),
    CHECK (policy_digest=digest(convert_to(concat_ws(chr(31),tenant_id::text,data_class,version::text,
      archive_after_days::text,prune_grace_days::text,object_lock_days::text,prune_enabled::text),'UTF8'),'sha256'))
);

CREATE TABLE retention_legal_holds (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    data_class text NOT NULL CHECK (data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    scope_type text NOT NULL CHECK (scope_type IN ('tenant','merchant','record')),
    merchant_id uuid,
    source_table text,
    source_record_id uuid,
    reason text NOT NULL CHECK (length(reason) BETWEEN 8 AND 2048),
    actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    released_at timestamptz,
    released_by text,
    release_reason text,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (expires_at IS NULL OR expires_at>created_at),
    CHECK (
      (scope_type='tenant' AND merchant_id IS NULL AND source_table IS NULL AND source_record_id IS NULL) OR
      (scope_type='merchant' AND merchant_id IS NOT NULL AND source_table IS NULL AND source_record_id IS NULL) OR
      (scope_type='record' AND merchant_id IS NOT NULL AND source_record_id IS NOT NULL AND
        (data_class='callback_event_body' AND source_table='callback_events' OR
         data_class='published_outbox_payload' AND source_table='outbox_events' OR
         data_class='event_history_payload' AND source_table='event_history'))
    ),
    CHECK ((released_at IS NULL AND released_by IS NULL AND release_reason IS NULL) OR
      (released_at IS NOT NULL AND released_by IS NOT NULL AND length(released_by) BETWEEN 1 AND 255
       AND release_reason IS NOT NULL AND length(release_reason) BETWEEN 8 AND 2048)),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT
);
CREATE INDEX retention_legal_holds_active_idx
  ON retention_legal_holds(tenant_id,data_class,expires_at) WHERE released_at IS NULL;

CREATE TABLE retention_archive_jobs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    data_class text NOT NULL CHECK (data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    policy_id uuid NOT NULL,
    cutoff_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('open','completed','failed')),
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    last_error text,
    CHECK ((status='completed')=(completed_at IS NOT NULL)),
    UNIQUE (id,tenant_id),
    UNIQUE (id,tenant_id,data_class),
    FOREIGN KEY(policy_id,tenant_id,data_class) REFERENCES retention_policy_versions(id,tenant_id,data_class) ON DELETE RESTRICT
);

CREATE TABLE retention_archive_batches (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    data_class text NOT NULL CHECK (data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version > 0),
    cutoff_at timestamptz NOT NULL,
    object_retention_until timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('leased','retry','verified','grace','pruned','archive_only','failed')),
    lease_owner text,
    lease_token uuid,
    lease_until timestamptz,
    fence bigint NOT NULL CHECK (fence > 0),
    attempt_count integer NOT NULL CHECK (attempt_count BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    verified_at timestamptz,
    first_prune_checked_at timestamptz,
    prune_not_before timestamptz,
    pruned_at timestamptz,
    last_error text,
    FOREIGN KEY(job_id,tenant_id,data_class) REFERENCES retention_archive_jobs(id,tenant_id,data_class) ON DELETE RESTRICT,
    FOREIGN KEY(policy_id,tenant_id,data_class,policy_version)
      REFERENCES retention_policy_versions(id,tenant_id,data_class,version) ON DELETE RESTRICT,
    UNIQUE(id,tenant_id),
    CHECK (object_retention_until>created_at),
    CHECK ((status='leased')=(lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK (status IN ('leased','retry','failed') OR verified_at IS NOT NULL),
    CHECK ((first_prune_checked_at IS NULL)=(prune_not_before IS NULL)),
    CHECK ((status='pruned')=(pruned_at IS NOT NULL))
);
CREATE INDEX retention_archive_batch_claim_idx ON retention_archive_batches(updated_at,id)
  WHERE status IN ('leased','retry');
CREATE INDEX retention_archive_batch_prune_idx ON retention_archive_batches(updated_at,id)
  WHERE status IN ('verified','grace');

CREATE TABLE retention_archive_batch_items (
    batch_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal>0),
    merchant_id uuid NOT NULL,
    source_table text NOT NULL CHECK (source_table IN ('callback_events','outbox_events','event_history')),
    source_record_id uuid NOT NULL,
    original_digest bytea NOT NULL CHECK (octet_length(original_digest)=32),
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY(batch_id,ordinal),
    UNIQUE(batch_id,source_table,source_record_id),
    FOREIGN KEY(batch_id,tenant_id) REFERENCES retention_archive_batches(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE retention_archive_objects (
    batch_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    object_key text NOT NULL CHECK (length(object_key) BETWEEN 32 AND 1024),
    object_version text NOT NULL CHECK (length(object_version) BETWEEN 1 AND 1024),
    byte_length bigint NOT NULL CHECK (byte_length>0),
    object_sha256 bytea NOT NULL CHECK (octet_length(object_sha256)=32),
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256)=32),
    signing_key_id text NOT NULL CHECK (length(signing_key_id) BETWEEN 1 AND 255),
    manifest_signature bytea NOT NULL CHECK (octet_length(manifest_signature)=64),
    object_lock_mode text NOT NULL CHECK (object_lock_mode='COMPLIANCE'),
    retention_until timestamptz NOT NULL,
    provider_attested_at timestamptz NOT NULL,
    verified_at timestamptz NOT NULL,
    FOREIGN KEY(batch_id,tenant_id) REFERENCES retention_archive_batches(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE(object_key,object_version),
    CHECK (retention_until>verified_at),
    CHECK (provider_attested_at<=verified_at+interval '5 minutes')
);

CREATE TABLE retention_archive_index (
    tenant_id uuid NOT NULL,
    data_class text NOT NULL CHECK (data_class IN ('callback_event_body','published_outbox_payload','event_history_payload')),
    source_table text NOT NULL CHECK (source_table IN ('callback_events','outbox_events','event_history')),
    source_record_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    original_digest bytea NOT NULL CHECK (octet_length(original_digest)=32),
    recorded_at timestamptz NOT NULL,
    archived_at timestamptz NOT NULL,
    batch_id uuid NOT NULL,
    object_key text NOT NULL,
    object_version text NOT NULL,
    PRIMARY KEY(tenant_id,data_class,source_table,source_record_id),
    FOREIGN KEY(batch_id,tenant_id) REFERENCES retention_archive_batches(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY(object_key,object_version) REFERENCES retention_archive_objects(object_key,object_version) ON DELETE RESTRICT
);
CREATE INDEX retention_archive_index_recorded_idx
  ON retention_archive_index(tenant_id,data_class,recorded_at,source_record_id);

ALTER TABLE retention_policy_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_legal_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_batch_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_index ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policy_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_legal_holds FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_batches FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_batch_items FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_objects FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_archive_index FORCE ROW LEVEL SECURITY;

CREATE POLICY retention_policy_scope ON retention_policy_versions
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_hold_scope ON retention_legal_holds
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_job_scope ON retention_archive_jobs
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_batch_scope ON retention_archive_batches
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_batch_item_scope ON retention_archive_batch_items
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_object_scope ON retention_archive_objects
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY retention_index_scope ON retention_archive_index
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE FUNCTION retention_reject_immutable_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN RAISE EXCEPTION 'retention evidence is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER retention_policy_immutable BEFORE UPDATE OR DELETE ON retention_policy_versions
  FOR EACH ROW EXECUTE FUNCTION retention_reject_immutable_mutation();
CREATE TRIGGER retention_batch_items_immutable BEFORE UPDATE OR DELETE ON retention_archive_batch_items
  FOR EACH ROW EXECUTE FUNCTION retention_reject_immutable_mutation();
CREATE TRIGGER retention_objects_immutable BEFORE UPDATE OR DELETE ON retention_archive_objects
  FOR EACH ROW EXECUTE FUNCTION retention_reject_immutable_mutation();
CREATE TRIGGER retention_index_immutable BEFORE UPDATE OR DELETE ON retention_archive_index
  FOR EACH ROW EXECUTE FUNCTION retention_reject_immutable_mutation();

CREATE FUNCTION create_retention_policy_version(
  requested_id uuid,requested_tenant uuid,requested_class text,requested_version bigint,
  requested_archive_days integer,requested_grace_days integer,requested_lock_days integer,
  requested_prune boolean,requested_effective timestamptz,requested_actor text,requested_at timestamptz
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE expected_version bigint; policy_hash bytea;
BEGIN
  IF requested_class NOT IN ('callback_event_body','published_outbox_payload','event_history_payload') OR
     requested_archive_days NOT BETWEEN 1 AND 3650 OR requested_grace_days NOT BETWEEN 1 AND 90 OR
     requested_lock_days NOT BETWEEN 30 AND 3650 OR requested_lock_days<=requested_grace_days OR
     requested_class IN ('callback_event_body','event_history_payload') AND requested_prune OR
     length(btrim(requested_actor)) NOT BETWEEN 1 AND 255 OR requested_effective<requested_at THEN RETURN false; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_tenant::text||chr(31)||requested_class,0));
  SELECT coalesce(max(version),0)+1 INTO expected_version FROM public.retention_policy_versions
    WHERE tenant_id=requested_tenant AND data_class=requested_class;
  IF requested_version<>expected_version THEN RETURN false; END IF;
  policy_hash:=digest(convert_to(concat_ws(chr(31),requested_tenant::text,requested_class,requested_version::text,
    requested_archive_days::text,requested_grace_days::text,requested_lock_days::text,requested_prune::text),'UTF8'),'sha256');
  INSERT INTO public.retention_policy_versions(id,tenant_id,data_class,version,archive_after_days,prune_grace_days,
    object_lock_days,prune_enabled,effective_at,created_at,created_by,policy_digest)
  VALUES(requested_id,requested_tenant,requested_class,requested_version,requested_archive_days,requested_grace_days,
    requested_lock_days,requested_prune,requested_effective,requested_at,btrim(requested_actor),policy_hash);
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION create_retention_policy_version(uuid,uuid,text,bigint,integer,integer,integer,boolean,timestamptz,text,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_guard_legal_hold_mutation() RETURNS trigger
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
     NEW.expires_at IS DISTINCT FROM OLD.expires_at OR OLD.released_at IS NOT NULL OR NEW.version<>OLD.version+1) THEN
    RAISE EXCEPTION 'legal hold identity is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER retention_legal_hold_guard BEFORE INSERT OR UPDATE OR DELETE ON retention_legal_holds
  FOR EACH ROW EXECUTE FUNCTION retention_guard_legal_hold_mutation();

CREATE FUNCTION create_retention_legal_hold(
  requested_id uuid,requested_tenant uuid,requested_class text,requested_scope text,
  requested_merchant uuid,requested_table text,requested_record uuid,requested_reason text,
  requested_actor text,requested_at timestamptz,requested_expires timestamptz
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE prior_write text:=current_setting('app.retention_hold_write',true);
BEGIN
  IF requested_class NOT IN ('callback_event_body','published_outbox_payload','event_history_payload') OR
     requested_scope NOT IN ('tenant','merchant','record') OR length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR
     length(btrim(requested_actor)) NOT BETWEEN 1 AND 255 OR requested_expires IS NOT NULL AND requested_expires<=requested_at THEN
    RETURN false;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(requested_tenant::text||chr(31)||requested_class,0));
  PERFORM set_config('app.retention_hold_write','1',true);
  BEGIN
    INSERT INTO public.retention_legal_holds(id,tenant_id,data_class,scope_type,merchant_id,source_table,
      source_record_id,reason,actor_id,created_at,expires_at)
    VALUES(requested_id,requested_tenant,requested_class,requested_scope,requested_merchant,requested_table,
      requested_record,btrim(requested_reason),btrim(requested_actor),requested_at,requested_expires);
    PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
    RAISE;
  END;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION create_retention_legal_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION release_retention_legal_hold(
  requested_id uuid,requested_actor text,requested_reason text,requested_at timestamptz
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE held public.retention_legal_holds%ROWTYPE; prior_write text:=current_setting('app.retention_hold_write',true);
BEGIN
  SELECT * INTO held FROM public.retention_legal_holds WHERE id=requested_id FOR UPDATE;
  IF NOT FOUND OR held.released_at IS NOT NULL OR length(btrim(requested_actor)) NOT BETWEEN 1 AND 255 OR
     length(btrim(requested_reason)) NOT BETWEEN 8 AND 2048 OR requested_at<held.created_at THEN RETURN false; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(held.tenant_id::text||chr(31)||held.data_class,0));
  PERFORM set_config('app.retention_hold_write','1',true);
  BEGIN
    UPDATE public.retention_legal_holds SET released_at=requested_at,released_by=btrim(requested_actor),
      release_reason=btrim(requested_reason),version=version+1 WHERE id=requested_id AND released_at IS NULL;
    IF NOT FOUND THEN
      PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
      RETURN false;
    END IF;
    PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
  EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.retention_hold_write',coalesce(prior_write,''),true);
    RAISE;
  END;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION release_retention_legal_hold(uuid,text,text,timestamptz) FROM PUBLIC;

ALTER TABLE outbox_events
  ADD COLUMN payload_archive_batch_id uuid,
  ADD COLUMN payload_original_digest bytea,
  ADD COLUMN payload_pruned_at timestamptz,
  ADD COLUMN payload_tombstone_version smallint NOT NULL DEFAULT 0,
  ADD CONSTRAINT outbox_payload_tombstone_shape CHECK (
    (payload_tombstone_version=0 AND payload_archive_batch_id IS NULL AND payload_original_digest IS NULL AND payload_pruned_at IS NULL) OR
    (payload_tombstone_version=1 AND payload_archive_batch_id IS NOT NULL AND octet_length(payload_original_digest)=32
      AND payload_pruned_at IS NOT NULL AND payload->>'schema'='retention-tombstone/v1'
      AND payload->>'sha256'=encode(payload_original_digest,'hex'))
  ),
  ADD CONSTRAINT outbox_payload_archive_batch_fk FOREIGN KEY(payload_archive_batch_id,tenant_id)
    REFERENCES retention_archive_batches(id,tenant_id) ON DELETE RESTRICT;

CREATE FUNCTION retention_source_candidate_exists(p_tenant uuid,p_class text,p_cutoff timestamptz)
RETURNS boolean LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF p_class='callback_event_body' THEN
    RETURN EXISTS(SELECT 1 FROM public.callback_events e
      WHERE e.tenant_id=p_tenant AND e.created_at<=p_cutoff
        AND digest(e.canonical_body,'sha256')=e.body_hash
        AND EXISTS(SELECT 1 FROM public.callback_deliveries d WHERE d.tenant_id=e.tenant_id AND d.callback_event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM public.callback_deliveries d WHERE d.tenant_id=e.tenant_id AND d.callback_event_id=e.id AND d.status<>'acknowledged')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
          WHERE bi.tenant_id=e.tenant_id AND b.data_class=p_class AND bi.source_table='callback_events' AND bi.source_record_id=e.id AND b.status<>'failed')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=p_class AND i.source_table='callback_events' AND i.source_record_id=e.id));
  ELSIF p_class='published_outbox_payload' THEN
    RETURN EXISTS(SELECT 1 FROM public.outbox_events e JOIN public.event_history h ON h.event_id=e.id AND h.tenant_id=e.tenant_id
      WHERE e.tenant_id=p_tenant AND e.published_at IS NOT NULL AND e.published_at<=p_cutoff AND e.payload_tombstone_version=0
        AND h.payload=e.payload
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
          WHERE bi.tenant_id=e.tenant_id AND b.data_class=p_class AND bi.source_table='outbox_events' AND bi.source_record_id=e.id AND b.status<>'failed')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=p_class AND i.source_table='outbox_events' AND i.source_record_id=e.id));
  ELSIF p_class='event_history_payload' THEN
    RETURN EXISTS(SELECT 1 FROM public.event_history e WHERE e.tenant_id=p_tenant AND e.published_at<=p_cutoff
      AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
        WHERE bi.tenant_id=e.tenant_id AND b.data_class=p_class AND bi.source_table='event_history' AND bi.source_record_id=e.event_id AND b.status<>'failed')
      AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=p_class AND i.source_table='event_history' AND i.source_record_id=e.event_id));
  END IF;
  RETURN false;
END $$;
REVOKE ALL ON FUNCTION retention_source_candidate_exists(uuid,text,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_claim_archive_batch(p_worker text,p_now timestamptz,p_lease_seconds integer,p_limit integer)
RETURNS TABLE(batch_id uuid,tenant_id uuid,data_class text,policy_version bigint,cutoff_at timestamptz,
  created_at timestamptz,object_retention_until timestamptz,lease_token uuid,fence bigint,ordinal integer,
  merchant_id uuid,source_table text,source_record_id uuid,recorded_at timestamptz,original_digest bytea,canonical_data bytea)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE selected public.retention_archive_batches%ROWTYPE; policy public.retention_policy_versions%ROWTYPE;
  job_id uuid; selected_id uuid; token uuid; cutoff timestamptz; found_policy boolean:=false;
BEGIN
  IF length(p_worker) NOT BETWEEN 1 AND 255 OR p_lease_seconds NOT BETWEEN 30 AND 1800 OR p_limit NOT BETWEEN 1 AND 500 THEN RETURN; END IF;
  SELECT * INTO selected FROM public.retention_archive_batches b
   WHERE b.status='retry' OR (b.status='leased' AND b.lease_until<p_now)
   ORDER BY b.updated_at,b.id FOR UPDATE SKIP LOCKED LIMIT 1;
  token:=gen_random_uuid();
  IF FOUND THEN
    IF selected.attempt_count>=100 THEN
      UPDATE public.retention_archive_batches SET status='failed',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
        updated_at=p_now,last_error='attempt_limit' WHERE id=selected.id;
      UPDATE public.retention_archive_jobs SET status='failed',last_error='attempt_limit' WHERE id=selected.job_id;
      RETURN;
    END IF;
    UPDATE public.retention_archive_batches SET status='leased',lease_owner=p_worker,lease_token=token,
      lease_until=p_now+make_interval(secs=>p_lease_seconds),fence=fence+1,attempt_count=attempt_count+1,
      updated_at=p_now,last_error=NULL WHERE id=selected.id RETURNING * INTO selected;
  ELSE
    FOR policy IN SELECT p.* FROM public.retention_policy_versions p
      WHERE p.effective_at<=p_now AND p.version=(SELECT max(p2.version) FROM public.retention_policy_versions p2
        WHERE p2.tenant_id=p.tenant_id AND p2.data_class=p.data_class AND p2.effective_at<=p_now)
      ORDER BY p.tenant_id,p.data_class
    LOOP
      cutoff:=p_now-make_interval(days=>policy.archive_after_days);
      IF public.retention_source_candidate_exists(policy.tenant_id,policy.data_class,cutoff) THEN found_policy:=true; EXIT; END IF;
    END LOOP;
    IF NOT found_policy THEN RETURN; END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(policy.tenant_id::text||chr(31)||policy.data_class,0));
    IF NOT public.retention_source_candidate_exists(policy.tenant_id,policy.data_class,cutoff) THEN RETURN; END IF;
    job_id:=gen_random_uuid(); selected_id:=gen_random_uuid();
    INSERT INTO public.retention_archive_jobs(id,tenant_id,data_class,policy_id,cutoff_at,status,created_at)
      VALUES(job_id,policy.tenant_id,policy.data_class,policy.id,cutoff,'open',p_now);
    INSERT INTO public.retention_archive_batches(id,job_id,tenant_id,data_class,policy_id,policy_version,cutoff_at,
      object_retention_until,status,lease_owner,lease_token,lease_until,fence,attempt_count,created_at,updated_at)
      VALUES(selected_id,job_id,policy.tenant_id,policy.data_class,policy.id,policy.version,cutoff,
        p_now+make_interval(days=>policy.object_lock_days),'leased',p_worker,token,
        p_now+make_interval(secs=>p_lease_seconds),1,1,p_now,p_now) RETURNING * INTO selected;
    IF policy.data_class='callback_event_body' THEN
      INSERT INTO public.retention_archive_batch_items(batch_id,tenant_id,ordinal,merchant_id,source_table,source_record_id,original_digest,recorded_at)
      SELECT selected_id,e.tenant_id,(row_number() OVER(ORDER BY e.created_at,e.id))::integer,e.merchant_id,'callback_events',e.id,e.body_hash,e.created_at
      FROM public.callback_events e WHERE e.tenant_id=policy.tenant_id AND e.created_at<=cutoff
        AND digest(e.canonical_body,'sha256')=e.body_hash
        AND EXISTS(SELECT 1 FROM public.callback_deliveries d WHERE d.tenant_id=e.tenant_id AND d.callback_event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM public.callback_deliveries d WHERE d.tenant_id=e.tenant_id AND d.callback_event_id=e.id AND d.status<>'acknowledged')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
          WHERE bi.tenant_id=e.tenant_id AND b.data_class=policy.data_class AND bi.source_table='callback_events' AND bi.source_record_id=e.id AND b.status<>'failed')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=policy.data_class AND i.source_table='callback_events' AND i.source_record_id=e.id)
      ORDER BY e.created_at,e.id LIMIT p_limit;
    ELSIF policy.data_class='published_outbox_payload' THEN
      INSERT INTO public.retention_archive_batch_items(batch_id,tenant_id,ordinal,merchant_id,source_table,source_record_id,original_digest,recorded_at)
      SELECT selected_id,e.tenant_id,(row_number() OVER(ORDER BY e.recorded_at,e.id))::integer,e.merchant_id,'outbox_events',e.id,
        digest(convert_to(e.payload::text,'UTF8'),'sha256'),e.recorded_at
      FROM public.outbox_events e JOIN public.event_history h ON h.event_id=e.id AND h.tenant_id=e.tenant_id
      WHERE e.tenant_id=policy.tenant_id AND e.published_at IS NOT NULL AND e.published_at<=cutoff AND e.payload_tombstone_version=0 AND h.payload=e.payload
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
          WHERE bi.tenant_id=e.tenant_id AND b.data_class=policy.data_class AND bi.source_table='outbox_events' AND bi.source_record_id=e.id AND b.status<>'failed')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=policy.data_class AND i.source_table='outbox_events' AND i.source_record_id=e.id)
      ORDER BY e.recorded_at,e.id LIMIT p_limit;
    ELSE
      INSERT INTO public.retention_archive_batch_items(batch_id,tenant_id,ordinal,merchant_id,source_table,source_record_id,original_digest,recorded_at)
      SELECT selected_id,e.tenant_id,(row_number() OVER(ORDER BY e.recorded_at,e.event_id))::integer,e.merchant_id,'event_history',e.event_id,
        digest(convert_to(e.payload::text,'UTF8'),'sha256'),e.recorded_at
      FROM public.event_history e WHERE e.tenant_id=policy.tenant_id AND e.published_at<=cutoff
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_batch_items bi JOIN public.retention_archive_batches b ON b.id=bi.batch_id
          WHERE bi.tenant_id=e.tenant_id AND b.data_class=policy.data_class AND bi.source_table='event_history' AND bi.source_record_id=e.event_id AND b.status<>'failed')
        AND NOT EXISTS(SELECT 1 FROM public.retention_archive_index i WHERE i.tenant_id=e.tenant_id AND i.data_class=policy.data_class AND i.source_table='event_history' AND i.source_record_id=e.event_id)
      ORDER BY e.recorded_at,e.event_id LIMIT p_limit;
    END IF;
  END IF;
  IF selected.data_class='callback_event_body' THEN
    RETURN QUERY SELECT selected.id,selected.tenant_id,selected.data_class,selected.policy_version,selected.cutoff_at,
      selected.created_at,selected.object_retention_until,selected.lease_token,selected.fence,i.ordinal,i.merchant_id,i.source_table,
      i.source_record_id,i.recorded_at,i.original_digest,e.canonical_body
      FROM public.retention_archive_batch_items i JOIN public.callback_events e ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id
      WHERE i.batch_id=selected.id AND digest(e.canonical_body,'sha256')=i.original_digest ORDER BY i.ordinal;
  ELSIF selected.data_class='published_outbox_payload' THEN
    RETURN QUERY SELECT selected.id,selected.tenant_id,selected.data_class,selected.policy_version,selected.cutoff_at,
      selected.created_at,selected.object_retention_until,selected.lease_token,selected.fence,i.ordinal,i.merchant_id,i.source_table,
      i.source_record_id,i.recorded_at,i.original_digest,convert_to(e.payload::text,'UTF8')
      FROM public.retention_archive_batch_items i JOIN public.outbox_events e ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id
      WHERE i.batch_id=selected.id AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest ORDER BY i.ordinal;
  ELSE
    RETURN QUERY SELECT selected.id,selected.tenant_id,selected.data_class,selected.policy_version,selected.cutoff_at,
      selected.created_at,selected.object_retention_until,selected.lease_token,selected.fence,i.ordinal,i.merchant_id,i.source_table,
      i.source_record_id,i.recorded_at,i.original_digest,convert_to(e.payload::text,'UTF8')
      FROM public.retention_archive_batch_items i JOIN public.event_history e ON e.event_id=i.source_record_id AND e.tenant_id=i.tenant_id
      WHERE i.batch_id=selected.id AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest ORDER BY i.ordinal;
  END IF;
END $$;
REVOKE ALL ON FUNCTION retention_claim_archive_batch(text,timestamptz,integer,integer) FROM PUBLIC;

CREATE FUNCTION retention_acknowledge_archive(
 p_batch uuid,p_lease uuid,p_fence bigint,p_object_key text,p_object_version text,p_bytes bigint,
 p_object_sha bytea,p_manifest_sha bytea,p_signing_key text,p_signature bytea,p_lock_mode text,
 p_retention_until timestamptz,p_attested_at timestamptz,p_now timestamptz
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE batch public.retention_archive_batches%ROWTYPE; expected_count integer; actual_count integer;
BEGIN
  SELECT * INTO batch FROM public.retention_archive_batches WHERE id=p_batch AND status='leased' AND lease_token=p_lease
    AND fence=p_fence AND lease_until>=p_now FOR UPDATE;
  IF NOT FOUND OR p_bytes<1 OR octet_length(p_object_sha)<>32 OR octet_length(p_manifest_sha)<>32 OR
     octet_length(p_signature)<>64 OR p_lock_mode<>'COMPLIANCE' OR p_retention_until<batch.object_retention_until OR
     p_attested_at NOT BETWEEN p_now-interval '5 minutes' AND p_now+interval '5 minutes' OR
     length(p_object_key) NOT BETWEEN 32 AND 1024 OR length(p_object_version) NOT BETWEEN 1 AND 1024 THEN RETURN false; END IF;
  IF p_object_key<>'retention/v1/'||batch.tenant_id::text||'/'||batch.data_class||'/'||batch.id::text||'.json' THEN RETURN false; END IF;
  SELECT count(*) INTO expected_count FROM public.retention_archive_batch_items WHERE batch_id=p_batch;
  IF batch.data_class='callback_event_body' THEN
    SELECT count(*) INTO actual_count FROM public.retention_archive_batch_items i JOIN public.callback_events e
      ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id WHERE i.batch_id=p_batch AND digest(e.canonical_body,'sha256')=i.original_digest;
  ELSIF batch.data_class='published_outbox_payload' THEN
    SELECT count(*) INTO actual_count FROM public.retention_archive_batch_items i JOIN public.outbox_events e
      ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id WHERE i.batch_id=p_batch AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest;
  ELSE
    SELECT count(*) INTO actual_count FROM public.retention_archive_batch_items i JOIN public.event_history e
      ON e.event_id=i.source_record_id AND e.tenant_id=i.tenant_id WHERE i.batch_id=p_batch AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest;
  END IF;
  IF expected_count=0 OR actual_count<>expected_count THEN RETURN false; END IF;
  INSERT INTO public.retention_archive_objects(batch_id,tenant_id,object_key,object_version,byte_length,object_sha256,
    manifest_sha256,signing_key_id,manifest_signature,object_lock_mode,retention_until,provider_attested_at,verified_at)
    VALUES(p_batch,batch.tenant_id,p_object_key,p_object_version,p_bytes,p_object_sha,p_manifest_sha,p_signing_key,
      p_signature,p_lock_mode,p_retention_until,p_attested_at,p_now);
  INSERT INTO public.retention_archive_index(tenant_id,data_class,source_table,source_record_id,merchant_id,original_digest,
    recorded_at,archived_at,batch_id,object_key,object_version)
    SELECT i.tenant_id,batch.data_class,i.source_table,i.source_record_id,i.merchant_id,i.original_digest,
      i.recorded_at,p_now,p_batch,p_object_key,p_object_version FROM public.retention_archive_batch_items i WHERE i.batch_id=p_batch;
  UPDATE public.retention_archive_batches SET status=CASE WHEN data_class='published_outbox_payload' THEN 'verified' ELSE 'archive_only' END,
    lease_owner=NULL,lease_token=NULL,lease_until=NULL,verified_at=p_now,updated_at=p_now,last_error=NULL WHERE id=p_batch;
  UPDATE public.retention_archive_jobs SET status='completed',completed_at=p_now,last_error=NULL WHERE id=batch.job_id;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION retention_acknowledge_archive(uuid,uuid,bigint,text,text,bigint,bytea,bytea,text,bytea,text,timestamptz,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_fail_archive(p_batch uuid,p_lease uuid,p_fence bigint,p_reason text,p_now timestamptz)
RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  UPDATE public.retention_archive_batches SET status=CASE WHEN attempt_count>=100 THEN 'failed' ELSE 'retry' END,
    lease_owner=NULL,lease_token=NULL,lease_until=NULL,updated_at=p_now,last_error=left(p_reason,240)
  WHERE id=p_batch AND status='leased' AND lease_token=p_lease AND fence=p_fence RETURNING true
$$;
REVOKE ALL ON FUNCTION retention_fail_archive(uuid,uuid,bigint,text,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_prune_admitted(p_batch uuid,p_now timestamptz)
RETURNS boolean LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE batch public.retention_archive_batches%ROWTYPE; blocked boolean; item_count integer; source_count integer;
BEGIN
  SELECT * INTO batch FROM public.retention_archive_batches WHERE id=p_batch;
  IF NOT FOUND OR batch.status NOT IN ('verified','grace','leased') OR batch.verified_at IS NULL THEN RETURN false; END IF;
  IF NOT EXISTS(SELECT 1 FROM public.retention_archive_objects o WHERE o.batch_id=p_batch AND o.object_lock_mode='COMPLIANCE'
    AND o.retention_until>p_now AND o.object_sha256 IS NOT NULL AND o.object_version<>'') THEN RETURN false; END IF;
  SELECT EXISTS(SELECT 1 FROM public.retention_archive_batch_items i JOIN public.retention_legal_holds h
    ON h.tenant_id=i.tenant_id AND h.data_class=batch.data_class AND h.released_at IS NULL AND (h.expires_at IS NULL OR h.expires_at>p_now)
    AND (h.scope_type='tenant' OR h.scope_type='merchant' AND h.merchant_id=i.merchant_id OR
      h.scope_type='record' AND h.merchant_id=i.merchant_id AND h.source_table=i.source_table AND h.source_record_id=i.source_record_id)
    WHERE i.batch_id=p_batch) INTO blocked;
  IF blocked THEN RETURN false; END IF;
  SELECT count(*) INTO item_count FROM public.retention_archive_batch_items WHERE batch_id=p_batch;
  IF batch.data_class='published_outbox_payload' THEN
    SELECT count(*) INTO source_count FROM public.retention_archive_batch_items i JOIN public.outbox_events e
      ON e.id=i.source_record_id AND e.tenant_id=i.tenant_id JOIN public.event_history h ON h.event_id=e.id AND h.tenant_id=e.tenant_id
      WHERE i.batch_id=p_batch AND e.published_at IS NOT NULL AND e.published_at<=batch.cutoff_at AND e.payload_tombstone_version=0
        AND h.payload=e.payload AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest;
  ELSE RETURN false;
  END IF;
  RETURN item_count>0 AND source_count=item_count;
END $$;
REVOKE ALL ON FUNCTION retention_prune_admitted(uuid,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_prune_published_outbox_payload(p_batch uuid,p_now timestamptz)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE changed integer; expected integer;
BEGIN
  IF NOT public.retention_prune_admitted(p_batch,p_now) THEN RAISE EXCEPTION 'outbox payload prune is not admitted'; END IF;
  UPDATE public.outbox_events e SET payload=jsonb_build_object('schema','retention-tombstone/v1','sha256',encode(i.original_digest,'hex'),
      'object_key',o.object_key,'object_version',o.object_version,'batch_id',p_batch::text),
    payload_archive_batch_id=p_batch,payload_original_digest=i.original_digest,payload_pruned_at=p_now,payload_tombstone_version=1
  FROM public.retention_archive_batch_items i JOIN public.retention_archive_objects o ON o.batch_id=i.batch_id
  WHERE i.batch_id=p_batch AND i.source_table='outbox_events' AND e.id=i.source_record_id AND e.tenant_id=i.tenant_id
    AND digest(convert_to(e.payload::text,'UTF8'),'sha256')=i.original_digest;
  GET DIAGNOSTICS changed=ROW_COUNT;
  SELECT count(*) INTO expected FROM public.retention_archive_batch_items WHERE batch_id=p_batch;
  IF changed<>expected THEN RAISE EXCEPTION 'outbox payload prune count mismatch'; END IF;
  RETURN changed;
END $$;
REVOKE ALL ON FUNCTION retention_prune_published_outbox_payload(uuid,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_claim_prune(p_worker text,p_now timestamptz,p_lease_seconds integer)
RETURNS TABLE(batch_id uuid,tenant_id uuid,data_class text,lease_token uuid,fence bigint,not_before timestamptz,first_check boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE selected public.retention_archive_batches%ROWTYPE; token uuid:=gen_random_uuid();
BEGIN
  IF length(p_worker) NOT BETWEEN 1 AND 255 OR p_lease_seconds NOT BETWEEN 30 AND 1800 THEN RETURN; END IF;
  SELECT b.* INTO selected FROM public.retention_archive_batches b JOIN public.retention_policy_versions p ON p.id=b.policy_id
    WHERE p.prune_enabled AND (b.status='verified' OR b.status='grace' AND b.prune_not_before<=p_now)
    ORDER BY b.updated_at,b.id FOR UPDATE OF b SKIP LOCKED LIMIT 1;
  IF NOT FOUND THEN RETURN; END IF;
  UPDATE public.retention_archive_batches SET status='leased',lease_owner=p_worker,lease_token=token,
    lease_until=p_now+make_interval(secs=>p_lease_seconds),fence=fence+1,updated_at=p_now
    WHERE id=selected.id RETURNING * INTO selected;
  RETURN QUERY SELECT selected.id,selected.tenant_id,selected.data_class,selected.lease_token,selected.fence,
    selected.prune_not_before,selected.first_prune_checked_at IS NULL;
END $$;
REVOKE ALL ON FUNCTION retention_claim_prune(text,timestamptz,integer) FROM PUBLIC;

CREATE FUNCTION retention_advance_prune(p_batch uuid,p_lease uuid,p_fence bigint,p_now timestamptz)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE batch public.retention_archive_batches%ROWTYPE; policy public.retention_policy_versions%ROWTYPE; count_pruned integer;
BEGIN
  SELECT * INTO batch FROM public.retention_archive_batches WHERE id=p_batch AND status='leased' AND lease_token=p_lease
    AND fence=p_fence AND lease_until>=p_now FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'retention prune lease is stale' USING ERRCODE='40001'; END IF;
  SELECT * INTO policy FROM public.retention_policy_versions WHERE id=batch.policy_id;
  PERFORM pg_advisory_xact_lock(hashtextextended(batch.tenant_id::text||chr(31)||batch.data_class,0));
  IF NOT public.retention_prune_admitted(p_batch,p_now) THEN
    UPDATE public.retention_archive_batches SET status='verified',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
      first_prune_checked_at=NULL,prune_not_before=NULL,updated_at=p_now,last_error='prune_admission_blocked' WHERE id=p_batch;
    RETURN 'blocked';
  END IF;
  IF batch.first_prune_checked_at IS NULL THEN
    UPDATE public.retention_archive_batches SET status='grace',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
      first_prune_checked_at=p_now,prune_not_before=p_now+make_interval(days=>policy.prune_grace_days),updated_at=p_now,last_error=NULL WHERE id=p_batch;
    RETURN 'grace_started';
  END IF;
  IF batch.prune_not_before>p_now THEN RAISE EXCEPTION 'retention prune grace has not elapsed'; END IF;
  IF batch.data_class='published_outbox_payload' THEN count_pruned:=public.retention_prune_published_outbox_payload(p_batch,p_now);
  ELSE RAISE EXCEPTION 'archive-only class cannot be pruned'; END IF;
  UPDATE public.retention_archive_batches SET status='pruned',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
    pruned_at=p_now,updated_at=p_now,last_error=NULL WHERE id=p_batch;
  RETURN 'pruned';
END $$;
REVOKE ALL ON FUNCTION retention_advance_prune(uuid,uuid,bigint,timestamptz) FROM PUBLIC;

CREATE FUNCTION retention_worker_health(p_now timestamptz,p_stale_seconds integer)
RETURNS TABLE(last_cycle_at timestamptz,pending_batches bigint,stale_leases bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT max(updated_at),count(*) FILTER(WHERE status IN ('retry','verified','grace')),
    count(*) FILTER(WHERE status='leased' AND lease_until<p_now-make_interval(secs=>p_stale_seconds))
  FROM public.retention_archive_batches
$$;
REVOKE ALL ON FUNCTION retention_worker_health(timestamptz,integer) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='retention_archive_worker') THEN
    GRANT SELECT ON retention_policy_versions,retention_legal_holds,retention_archive_jobs,retention_archive_batches,
      retention_archive_batch_items,retention_archive_objects,retention_archive_index TO retention_archive_worker;
    GRANT EXECUTE ON FUNCTION retention_claim_archive_batch(text,timestamptz,integer,integer),
      retention_acknowledge_archive(uuid,uuid,bigint,text,text,bigint,bytea,bytea,text,bytea,text,timestamptz,timestamptz,timestamptz),
      retention_fail_archive(uuid,uuid,bigint,text,timestamptz),retention_claim_prune(text,timestamptz,integer),
      retention_advance_prune(uuid,uuid,bigint,timestamptz),retention_worker_health(timestamptz,integer)
    TO retention_archive_worker;
  END IF;
END $$;

COMMIT;
