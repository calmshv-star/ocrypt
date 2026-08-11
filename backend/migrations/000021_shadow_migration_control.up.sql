BEGIN;

-- Phase 8 shadow migration and cutover control. PostgreSQL remains the
-- authoritative fence for platform create, credit and callback ownership.
CREATE TABLE migration_runs (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  source_system_id text NOT NULL CHECK(source_system_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
  profile text NOT NULL CHECK(profile IN ('generic','wallet_ledger','json_md5','form_md5')),
  state text NOT NULL CHECK(state IN ('inventory','validated','two_person_approved','importing','shadow','canary','cutover_ready','cutover','rollback_window','rollback_pending','rolled_back','decommissioned')),
  create_traffic_owner text NOT NULL CHECK(create_traffic_owner IN ('legacy','shadow','canary','platform')),
  callback_owner text NOT NULL CHECK(callback_owner IN ('legacy','shadow','canary','platform')),
  desired_action_version bigint NOT NULL DEFAULT 0 CHECK(desired_action_version>=0),
  actuator_ack_version bigint NOT NULL DEFAULT 0 CHECK(actuator_ack_version>=0 AND actuator_ack_version<=desired_action_version),
  fence_token bigint NOT NULL DEFAULT 1 CHECK(fence_token>0),
  row_version bigint NOT NULL DEFAULT 1 CHECK(row_version>0),
  rollback_deadline timestamptz,
  pending_action text CHECK(pending_action IS NULL OR pending_action IN ('activate_platform','restore_legacy')),
  pending_target_state text CHECK(pending_target_state IS NULL OR pending_target_state IN ('cutover','rolled_back')),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK((pending_action IS NULL)=(pending_target_state IS NULL)),
  UNIQUE(tenant_id,source_system_id),
  UNIQUE(id,tenant_id)
);
CREATE UNIQUE INDEX migration_one_active_source_idx ON migration_runs(tenant_id)
  WHERE state<>'decommissioned';

CREATE TABLE migration_manifest_versions (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  kind text NOT NULL CHECK(kind IN ('inventory','dry_run','canary','cutover','decommission')),
  canonical_body bytea NOT NULL CHECK(octet_length(canonical_body) BETWEEN 2 AND 524288),
  canonical_payload jsonb NOT NULL CHECK(jsonb_typeof(canonical_payload)='object' AND pg_column_size(canonical_payload)<=524288),
  payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
  signer_key_ids text[] NOT NULL CHECK(cardinality(signer_key_ids)=2 AND signer_key_ids[1]<>signer_key_ids[2]),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL,
  CHECK(payload_hash=digest(canonical_body,'sha256')),
  CHECK(convert_from(canonical_body,'UTF8')::jsonb=canonical_payload),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  UNIQUE(migration_id,kind,payload_hash),
  UNIQUE(id,migration_id,tenant_id)
);

CREATE TABLE migration_transition_requests (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  from_state text NOT NULL,
  target_state text NOT NULL,
  manifest_id uuid NOT NULL,
  expected_row_version bigint NOT NULL CHECK(expected_row_version>0),
  expected_fence_token bigint NOT NULL CHECK(expected_fence_token>0),
  status text NOT NULL CHECK(status IN ('pending_approval','approved','rejected','expired','awaiting_actuator','executed')),
  reason text NOT NULL CHECK(length(reason) BETWEEN 12 AND 1000),
  requested_by uuid NOT NULL,
  approved_by uuid,
  executed_by uuid,
  request_step_up_at timestamptz NOT NULL,
  approval_step_up_at timestamptz,
  execution_step_up_at timestamptz,
  decision_reason text,
  execution_reason text,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  decided_at timestamptz,
  executed_at timestamptz,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  CHECK(expires_at>created_at AND expires_at<=created_at+interval '30 minutes'),
  CHECK(approved_by IS NULL OR approved_by<>requested_by),
  CHECK(executed_by IS NULL OR (executed_by<>requested_by AND executed_by IS DISTINCT FROM approved_by)),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(manifest_id,migration_id,tenant_id) REFERENCES migration_manifest_versions(id,migration_id,tenant_id)
);
CREATE UNIQUE INDEX migration_one_pending_transition_idx ON migration_transition_requests(migration_id)
  WHERE status IN ('pending_approval','approved','awaiting_actuator');

CREATE TABLE migration_control_idempotency (
  tenant_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  operation text NOT NULL,
  idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 255),
  request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
  resource_id uuid NOT NULL,
  response_body jsonb NOT NULL CHECK(jsonb_typeof(response_body)='object' AND pg_column_size(response_body)<=65536),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(tenant_id,actor_id,operation,idempotency_key)
);

CREATE TABLE migration_worker_leases (
  migration_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  worker_id text NOT NULL CHECK(worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
  lease_token uuid NOT NULL,
  lease_until timestamptz NOT NULL,
  fence_token bigint NOT NULL CHECK(fence_token>0),
  claimed_at timestamptz NOT NULL,
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id)
);

CREATE TABLE migration_import_items (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  source_sequence bigint NOT NULL CHECK(source_sequence>0),
  entity_type text NOT NULL CHECK(entity_type IN ('merchant','configuration','asset','chain','rpc_provider','wallet','open_order','paid_order','expired_order','amount_reservation','incoming_transfer','unmatched_transfer','callback_backlog','scanner_cursor','provider_order','balance_observation')),
  source_id text NOT NULL CHECK(source_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
  payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND pg_column_size(payload)<=65536),
  payload_hash bytea GENERATED ALWAYS AS (digest(payload::text,'sha256')) STORED,
  classification text NOT NULL CHECK(classification IN ('staged','applied','migration_review','unsupported')),
  platform_resource_id uuid,
  created_at timestamptz NOT NULL,
  classified_at timestamptz,
  PRIMARY KEY(migration_id,source_sequence),
  UNIQUE(migration_id,entity_type,source_id),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id)
);

CREATE TABLE migration_imported_addresses (
  address_id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  source_id text NOT NULL,
  imported_at timestamptz NOT NULL,
  FOREIGN KEY(address_id,tenant_id) REFERENCES addresses(id,tenant_id),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  UNIQUE(migration_id,source_id)
);

CREATE TABLE migration_imported_orders (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  source_id text NOT NULL,
  source_status text NOT NULL CHECK(source_status IN ('open','paid','expired')),
  intent_id uuid NOT NULL,
  route_id uuid NOT NULL,
  legacy_amount_minor uint256 NOT NULL,
  legacy_amount_atomic uint256 NOT NULL,
  legacy_receiving_address text NOT NULL,
  legacy_expires_at timestamptz NOT NULL,
  finality_snapshot_id uuid NOT NULL REFERENCES platform_config_snapshots(id),
  finality_fence bigint NOT NULL CHECK(finality_fence>0),
  required_finality bigint NOT NULL CHECK(required_finality>0),
  matching_policy_id uuid NOT NULL,
  matching_policy_version bigint NOT NULL CHECK(matching_policy_version>0),
  matching_policy_hash bytea NOT NULL CHECK(octet_length(matching_policy_hash)=32),
  imported_at timestamptz NOT NULL,
  PRIMARY KEY(migration_id,source_id),
  UNIQUE(intent_id),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
  FOREIGN KEY(route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
  FOREIGN KEY(matching_policy_id,tenant_id) REFERENCES automated_matching_policies(id,tenant_id)
);

CREATE TABLE migration_verification_evidence (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  source_id text NOT NULL,
  event_identity text NOT NULL,
  transaction_id text NOT NULL,
  chain_id text NOT NULL,
  asset_id text NOT NULL,
  receiving_address text NOT NULL,
  amount_atomic uint256 NOT NULL CHECK(amount_atomic>0),
  confirmations bigint NOT NULL CHECK(confirmations>0),
  canonical_fact bytea NOT NULL CHECK(octet_length(canonical_fact) BETWEEN 2 AND 65536),
  evidence_hash bytea NOT NULL CHECK(octet_length(evidence_hash)=32 AND evidence_hash=digest(canonical_fact,'sha256')),
  verifier_key_ids text[] NOT NULL CHECK(cardinality(verifier_key_ids)>=2 AND cardinality(verifier_key_ids)<=8),
  verifier_version bigint NOT NULL CHECK(verifier_version>0),
  verified_at timestamptz NOT NULL,
  ledger_transaction_id uuid,
  FOREIGN KEY(migration_id,source_id) REFERENCES migration_imported_orders(migration_id,source_id),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  UNIQUE(migration_id,source_id),
  UNIQUE(migration_id,event_identity),
  UNIQUE(ledger_transaction_id)
);

CREATE TABLE migration_event_ownership (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  event_identity text NOT NULL,
  platform_event_id uuid,
  provider_inbox_id uuid,
  admitted_route_id uuid,
  opening_ledger_transaction_id uuid,
  owner text NOT NULL CHECK(owner IN ('legacy','shadow','platform')),
  fence_token bigint NOT NULL CHECK(fence_token>0),
  reason text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  PRIMARY KEY(migration_id,event_identity),
  UNIQUE(platform_event_id),
  UNIQUE(provider_inbox_id),
  UNIQUE(opening_ledger_transaction_id),
  CHECK(num_nonnulls(platform_event_id,provider_inbox_id)<=1),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(platform_event_id) REFERENCES transfer_events(id),
  FOREIGN KEY(provider_inbox_id) REFERENCES provider_inbox(id),
  FOREIGN KEY(admitted_route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
  FOREIGN KEY(opening_ledger_transaction_id) REFERENCES ledger_transactions(id)
);

CREATE TABLE migration_review (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  classification text NOT NULL CHECK(classification IN ('unknown_event_identity','identity_conflict','unverified_paid_order','unsupported_source_record','shadow_difference')),
  source_reference text NOT NULL,
  evidence_hash bytea NOT NULL CHECK(octet_length(evidence_hash)=32),
  status text NOT NULL CHECK(status IN ('open','explained')),
  explanation_reference text,
  created_at timestamptz NOT NULL,
  explained_at timestamptz,
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  UNIQUE(migration_id,classification,source_reference)
);

CREATE TABLE migration_shadow_comparisons (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  source_sequence bigint NOT NULL CHECK(source_sequence>0),
  entity_type text NOT NULL,
  source_id text NOT NULL,
  source_digest bytea NOT NULL CHECK(octet_length(source_digest)=32),
  platform_digest bytea NOT NULL CHECK(octet_length(platform_digest)=32),
  classification text NOT NULL CHECK(classification IN ('equal','explained','missing_platform','missing_source','value_mismatch','identity_conflict')),
  explanation_reference text,
  observation jsonb NOT NULL CHECK(jsonb_typeof(observation)='object' AND pg_column_size(observation)<=65536),
  observed_at timestamptz NOT NULL,
  PRIMARY KEY(migration_id,source_sequence),
  UNIQUE(migration_id,entity_type,source_id),
  CHECK((classification='explained')=(explanation_reference IS NOT NULL)),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id)
);

CREATE TABLE migration_callback_ownership (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  intent_id uuid NOT NULL,
  owner text NOT NULL CHECK(owner IN ('legacy','shadow','platform')),
  fence_token bigint NOT NULL CHECK(fence_token>0),
  updated_at timestamptz NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  PRIMARY KEY(migration_id,intent_id),
  UNIQUE(intent_id),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id)
);

CREATE TABLE migration_shadow_callback_comparisons (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  intent_id uuid NOT NULL,
  event_type text NOT NULL,
  legacy_body_hash bytea NOT NULL CHECK(octet_length(legacy_body_hash)=32),
  platform_body_hash bytea NOT NULL CHECK(octet_length(platform_body_hash)=32),
  classification text NOT NULL CHECK(classification IN ('equal','explained','value_mismatch','missing_legacy','missing_platform')),
  explanation_reference text,
  observed_at timestamptz NOT NULL,
  CHECK((classification='explained')=(explanation_reference IS NOT NULL)),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
  UNIQUE(migration_id,intent_id,event_type,legacy_body_hash,platform_body_hash)
);

CREATE TABLE migration_canary_versions (
  id uuid PRIMARY KEY,
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL CHECK(version>0),
  percentage smallint NOT NULL CHECK(percentage BETWEEN 0 AND 100),
  merchant_ids uuid[] NOT NULL,
  asset_ids text[] NOT NULL,
  manifest_id uuid NOT NULL,
  payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
  created_by uuid NOT NULL,
  created_at timestamptz NOT NULL,
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(manifest_id,migration_id,tenant_id) REFERENCES migration_manifest_versions(id,migration_id,tenant_id),
  UNIQUE(migration_id,version)
);

CREATE TABLE migration_desired_actions (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  action_version bigint NOT NULL CHECK(action_version>0),
  action text NOT NULL CHECK(action IN ('activate_platform','restore_legacy')),
  target_state text NOT NULL CHECK(target_state IN ('cutover','rolled_back')),
  fence_token bigint NOT NULL CHECK(fence_token>0),
  status text NOT NULL CHECK(status IN ('pending','acknowledged')),
  request_id uuid NOT NULL,
  acknowledged_by_key_id text,
  evidence_hash bytea,
  signature text,
  created_at timestamptz NOT NULL,
  acknowledged_at timestamptz,
  PRIMARY KEY(migration_id,action_version),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id),
  FOREIGN KEY(request_id) REFERENCES migration_transition_requests(id),
  CHECK((status='acknowledged')=(acknowledged_at IS NOT NULL))
);

CREATE TABLE migration_decommission_evidence (
  migration_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  evidence_type text NOT NULL CHECK(evidence_type IN ('final_reconciliation','encrypted_archive','restore_test','key_revocation')),
  evidence_reference text NOT NULL CHECK(evidence_reference ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,254}$'),
  evidence_hash bytea NOT NULL CHECK(octet_length(evidence_hash)=32),
  recorded_at timestamptz NOT NULL,
  PRIMARY KEY(migration_id,evidence_type),
  FOREIGN KEY(migration_id,tenant_id) REFERENCES migration_runs(id,tenant_id)
);

CREATE FUNCTION migration_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'migration evidence is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER migration_manifests_immutable BEFORE UPDATE OR DELETE ON migration_manifest_versions FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_import_items_immutable BEFORE DELETE ON migration_import_items FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_shadow_immutable BEFORE UPDATE OR DELETE ON migration_shadow_comparisons FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_callback_compare_immutable BEFORE UPDATE OR DELETE ON migration_shadow_callback_comparisons FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_canary_immutable BEFORE UPDATE OR DELETE ON migration_canary_versions FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_actions_no_delete BEFORE DELETE ON migration_desired_actions FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();
CREATE TRIGGER migration_decommission_evidence_immutable BEFORE UPDATE OR DELETE ON migration_decommission_evidence FOR EACH ROW EXECUTE FUNCTION migration_reject_mutation();

CREATE FUNCTION migration_verification_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' OR OLD.ledger_transaction_id IS NOT NULL OR (to_jsonb(NEW)-'ledger_transaction_id')<>(to_jsonb(OLD)-'ledger_transaction_id') OR NEW.ledger_transaction_id IS NULL THEN
    RAISE EXCEPTION 'migration verification evidence is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_verification_frozen BEFORE UPDATE OR DELETE ON migration_verification_evidence FOR EACH ROW EXECUTE FUNCTION migration_verification_frozen();

CREATE FUNCTION migration_admin_allowed(requested_actor uuid,requested_tenant uuid,requested_permission text,requested_session text,requested_step_up timestamptz)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT current_setting('app.migration_actor_id',true)=requested_actor::text
     AND current_setting('app.migration_session_id',true)=requested_session
     AND requested_step_up BETWEEN clock_timestamp()-interval '5 minutes' AND clock_timestamp()+interval '10 seconds'
     AND EXISTS(SELECT 1 FROM public.admin_role_bindings b
       JOIN public.admin_users u ON u.id=b.user_id
       JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key
       WHERE b.user_id=requested_actor AND u.status='active' AND rp.permission_key=requested_permission
         AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>clock_timestamp())
         AND (b.tenant_id IS NULL OR b.tenant_id=requested_tenant))
$$;
REVOKE ALL ON FUNCTION migration_admin_allowed(uuid,uuid,text,text,timestamptz) FROM PUBLIC;

CREATE FUNCTION migration_state_edge(from_state text,to_state text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT (from_state,to_state) IN (('inventory','validated'),('validated','two_person_approved'),('two_person_approved','importing'),('importing','shadow'),('shadow','canary'),('canary','cutover_ready'),('cutover_ready','cutover'),('cutover','rollback_window'),('rollback_window','decommissioned'),('rolled_back','shadow'))
   OR (to_state='rollback_pending' AND from_state IN ('canary','cutover_ready','cutover','rollback_window'))
$$;

CREATE FUNCTION migration_append_control_event(run migration_runs,actor uuid,session text,action text,reason text,details jsonb,occurred timestamptz)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE audit_id uuid:=gen_random_uuid(); event_id uuid:=gen_random_uuid();
BEGIN
  PERFORM public.append_platform_admin_audit(audit_id,run.tenant_id,actor,session,action,'migration',run.id::text,reason,details,occurred);
  INSERT INTO public.platform_admin_outbox(id,scope_id,tenant_id,aggregate_type,aggregate_id,aggregate_version,event_type,payload,occurred_at,available_at)
  VALUES(event_id,public.platform_scope_uuid(run.tenant_id),run.tenant_id,'migration',run.id::text,run.row_version,'platform_admin.'||action,
    jsonb_build_object('event_id',event_id,'event_type','platform_admin.'||action,'resource_type','migration','resource_id',run.id,'aggregate_version',run.row_version,'occurred_at',occurred,'details',details),occurred,occurred);
END $$;
REVOKE ALL ON FUNCTION migration_append_control_event(migration_runs,uuid,text,text,text,jsonb,timestamptz) FROM PUBLIC;

CREATE FUNCTION create_migration_run(requested_tenant uuid,requested_source text,requested_profile text,requested_reason text,requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_idempotency text,requested_hash bytea)
RETURNS SETOF migration_runs LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); selected_id uuid; run public.migration_runs%ROWTYPE; prior record;
BEGIN
  IF NOT public.migration_admin_allowed(requested_actor,requested_tenant,'migration:request',requested_session,requested_step_up) THEN RAISE EXCEPTION 'migration request denied' USING ERRCODE='MP003'; END IF;
  IF requested_source !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$' OR requested_profile NOT IN ('generic','wallet_ledger','json_md5','form_md5') OR length(btrim(requested_reason)) NOT BETWEEN 12 AND 1000 OR octet_length(requested_hash)<>32 THEN RAISE EXCEPTION 'invalid migration run' USING ERRCODE='MP001'; END IF;
  SELECT * INTO prior FROM public.migration_control_idempotency WHERE tenant_id=requested_tenant AND actor_id=requested_actor AND operation='create' AND idempotency_key=requested_idempotency;
  IF FOUND THEN
    IF prior.request_hash<>requested_hash THEN RAISE EXCEPTION 'idempotency conflict' USING ERRCODE='MP004'; END IF;
    RETURN NEXT jsonb_populate_record(NULL::public.migration_runs,prior.response_body); RETURN;
  END IF;
  selected_id:=gen_random_uuid();
  INSERT INTO public.migration_runs(id,tenant_id,source_system_id,profile,state,create_traffic_owner,callback_owner,created_at,updated_at)
  VALUES(selected_id,requested_tenant,requested_source,requested_profile,'inventory','legacy','legacy',authoritative_at,authoritative_at) RETURNING * INTO run;
  INSERT INTO public.migration_control_idempotency VALUES(requested_tenant,requested_actor,'create',requested_idempotency,requested_hash,selected_id,to_jsonb(run),authoritative_at,authoritative_at+interval '7 days');
  PERFORM public.migration_append_control_event(run,requested_actor,requested_session,'migration.created',btrim(requested_reason),jsonb_build_object('profile',run.profile,'source_system_id',run.source_system_id),authoritative_at);
  RETURN NEXT run;
END $$;
REVOKE ALL ON FUNCTION create_migration_run(uuid,text,text,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION attach_migration_manifest(requested_migration uuid,expected_row_version bigint,requested_manifest uuid,requested_kind text,requested_body bytea,requested_digest bytea,requested_signers text[],requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_reason text,requested_idempotency text,requested_hash bytea)
RETURNS SETOF migration_manifest_versions LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); run public.migration_runs%ROWTYPE; prior record; manifest public.migration_manifest_versions%ROWTYPE; requested_payload jsonb;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'migration not found' USING ERRCODE='P0002'; END IF;
  IF NOT public.migration_admin_allowed(requested_actor,run.tenant_id,'migration:request',requested_session,requested_step_up) THEN RAISE EXCEPTION 'manifest request denied' USING ERRCODE='MP003'; END IF;
  IF octet_length(requested_body) NOT BETWEEN 2 AND 524288 OR octet_length(requested_digest)<>32 OR requested_digest<>digest(requested_body,'sha256') THEN RAISE EXCEPTION 'manifest body conflict' USING ERRCODE='MP002'; END IF;
  BEGIN requested_payload:=convert_from(requested_body,'UTF8')::jsonb; EXCEPTION WHEN OTHERS THEN RAISE EXCEPTION 'manifest JSON rejected' USING ERRCODE='MP001'; END;
  IF requested_kind NOT IN ('inventory','dry_run','canary','cutover','decommission') OR jsonb_typeof(requested_payload)<>'object' OR pg_column_size(requested_payload)>524288 OR cardinality(requested_signers)<>2 OR requested_signers[1]=requested_signers[2] OR length(btrim(requested_reason)) NOT BETWEEN 12 AND 1000 OR octet_length(requested_hash)<>32 THEN RAISE EXCEPTION 'manifest input rejected' USING ERRCODE='MP001'; END IF;
  SELECT * INTO prior FROM public.migration_control_idempotency WHERE tenant_id=run.tenant_id AND actor_id=requested_actor AND operation='attach_manifest' AND idempotency_key=requested_idempotency;
  IF FOUND THEN
    IF prior.request_hash<>requested_hash OR prior.resource_id<>requested_manifest OR NOT EXISTS(SELECT 1 FROM public.migration_manifest_versions WHERE id=prior.resource_id AND migration_id=run.id AND tenant_id=run.tenant_id) THEN RAISE EXCEPTION 'idempotency conflict' USING ERRCODE='MP004'; END IF;
    RETURN NEXT jsonb_populate_record(NULL::public.migration_manifest_versions,prior.response_body); RETURN;
  END IF;
  IF run.row_version<>expected_row_version THEN RAISE EXCEPTION 'manifest version conflict' USING ERRCODE='MP002'; END IF;
  INSERT INTO public.migration_manifest_versions(id,migration_id,tenant_id,kind,canonical_body,canonical_payload,payload_hash,signer_key_ids,created_by,created_at)
  VALUES(requested_manifest,run.id,run.tenant_id,requested_kind,requested_body,requested_payload,requested_digest,requested_signers,requested_actor,authoritative_at) RETURNING * INTO manifest;
  UPDATE public.migration_runs SET row_version=row_version+1,updated_at=authoritative_at WHERE id=run.id RETURNING * INTO run;
  INSERT INTO public.migration_control_idempotency VALUES(run.tenant_id,requested_actor,'attach_manifest',requested_idempotency,requested_hash,requested_manifest,to_jsonb(manifest),authoritative_at,authoritative_at+interval '7 days');
  PERFORM public.migration_append_control_event(run,requested_actor,requested_session,'migration.manifest_attached',btrim(requested_reason),jsonb_build_object('manifest_id',manifest.id,'kind',manifest.kind,'payload_hash',encode(manifest.payload_hash,'hex'),'signer_key_ids',manifest.signer_key_ids),authoritative_at);
  RETURN NEXT manifest;
END $$;
REVOKE ALL ON FUNCTION attach_migration_manifest(uuid,bigint,uuid,text,bytea,bytea,text[],uuid,text,timestamptz,text,text,bytea) FROM PUBLIC;

CREATE FUNCTION request_migration_transition(requested_migration uuid,requested_tenant uuid,requested_target text,expected_row_version bigint,expected_fence bigint,requested_manifest uuid,requested_reason text,requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_idempotency text,requested_hash bytea)
RETURNS SETOF migration_transition_requests LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); run public.migration_runs%ROWTYPE; prior record; request_id uuid:=gen_random_uuid(); request public.migration_transition_requests%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'migration not found' USING ERRCODE='P0002'; END IF;
  IF NOT public.migration_admin_allowed(requested_actor,run.tenant_id,'migration:request',requested_session,requested_step_up) THEN RAISE EXCEPTION 'transition request denied' USING ERRCODE='MP003'; END IF;
  IF expected_row_version<1 OR expected_fence<1 OR length(btrim(requested_reason)) NOT BETWEEN 12 AND 1000 OR octet_length(requested_hash)<>32 OR NOT EXISTS(SELECT 1 FROM public.migration_manifest_versions WHERE id=requested_manifest AND migration_id=run.id AND tenant_id=run.tenant_id) THEN RAISE EXCEPTION 'transition input rejected' USING ERRCODE='MP001'; END IF;
  SELECT * INTO prior FROM public.migration_control_idempotency WHERE tenant_id=run.tenant_id AND actor_id=requested_actor AND operation='request_transition' AND idempotency_key=requested_idempotency;
  IF FOUND THEN
    IF prior.request_hash<>requested_hash OR NOT EXISTS(SELECT 1 FROM public.migration_transition_requests WHERE id=prior.resource_id AND migration_id=run.id AND tenant_id=run.tenant_id) THEN RAISE EXCEPTION 'idempotency conflict' USING ERRCODE='MP004'; END IF;
    RETURN NEXT jsonb_populate_record(NULL::public.migration_transition_requests,prior.response_body); RETURN;
  END IF;
  UPDATE public.migration_transition_requests SET status='expired',version=version+1,decided_at=authoritative_at WHERE migration_id=run.id AND status IN ('pending_approval','approved') AND expires_at<=authoritative_at;
  IF run.row_version<>expected_row_version OR run.fence_token<>expected_fence OR NOT public.migration_state_edge(run.state,requested_target) OR EXISTS(SELECT 1 FROM public.migration_transition_requests WHERE migration_id=run.id AND status IN ('pending_approval','approved','awaiting_actuator')) THEN RAISE EXCEPTION 'transition conflict' USING ERRCODE='MP002'; END IF;
  INSERT INTO public.migration_transition_requests(id,migration_id,tenant_id,from_state,target_state,manifest_id,expected_row_version,expected_fence_token,status,reason,requested_by,request_step_up_at,created_at,expires_at)
  VALUES(request_id,run.id,run.tenant_id,run.state,requested_target,requested_manifest,expected_row_version,expected_fence,'pending_approval',btrim(requested_reason),requested_actor,requested_step_up,authoritative_at,authoritative_at+interval '30 minutes') RETURNING * INTO request;
  INSERT INTO public.migration_control_idempotency VALUES(run.tenant_id,requested_actor,'request_transition',requested_idempotency,requested_hash,request_id,to_jsonb(request),authoritative_at,authoritative_at+interval '7 days');
  PERFORM public.migration_append_control_event(run,requested_actor,requested_session,'migration.transition_requested',btrim(requested_reason),jsonb_build_object('request_id',request.id,'from_state',request.from_state,'target_state',request.target_state,'fence_token',run.fence_token),authoritative_at);
  RETURN NEXT request;
END $$;
REVOKE ALL ON FUNCTION request_migration_transition(uuid,uuid,text,bigint,bigint,uuid,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION decide_migration_transition(requested_id uuid,requested_tenant uuid,expected_request_version bigint,approve boolean,requested_reason text,requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_idempotency text,requested_hash bytea)
RETURNS SETOF migration_transition_requests LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); request public.migration_transition_requests%ROWTYPE; run public.migration_runs%ROWTYPE; prior record; action text;
BEGIN
  SELECT * INTO request FROM public.migration_transition_requests WHERE id=requested_id AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'transition not found' USING ERRCODE='P0002'; END IF;
  SELECT * INTO run FROM public.migration_runs WHERE id=request.migration_id FOR UPDATE;
  IF NOT public.migration_admin_allowed(requested_actor,run.tenant_id,'migration:approve',requested_session,requested_step_up) THEN RAISE EXCEPTION 'transition decision denied' USING ERRCODE='MP003'; END IF;
  IF expected_request_version<1 OR length(btrim(requested_reason)) NOT BETWEEN 12 AND 1000 OR octet_length(requested_hash)<>32 THEN RAISE EXCEPTION 'transition decision input rejected' USING ERRCODE='MP001'; END IF;
  SELECT * INTO prior FROM public.migration_control_idempotency WHERE tenant_id=run.tenant_id AND actor_id=requested_actor AND operation='decide_transition' AND idempotency_key=requested_idempotency;
  IF FOUND THEN
    IF prior.request_hash<>requested_hash OR prior.resource_id<>request.id THEN RAISE EXCEPTION 'idempotency conflict' USING ERRCODE='MP004'; END IF;
    RETURN NEXT jsonb_populate_record(NULL::public.migration_transition_requests,prior.response_body); RETURN;
  END IF;
  IF request.status<>'pending_approval' OR request.version<>expected_request_version OR request.requested_by=requested_actor OR request.expires_at<=authoritative_at OR run.state<>request.from_state OR run.row_version<>request.expected_row_version OR run.fence_token<>request.expected_fence_token THEN RAISE EXCEPTION 'transition decision conflict' USING ERRCODE='MP002'; END IF;
  IF approve THEN
    UPDATE public.migration_transition_requests SET status='approved',approved_by=requested_actor,approval_step_up_at=requested_step_up,decision_reason=btrim(requested_reason),decided_at=authoritative_at,version=version+1 WHERE id=request.id RETURNING * INTO request; action:='approved';
  ELSE
    UPDATE public.migration_transition_requests SET status='rejected',decision_reason=btrim(requested_reason),decided_at=authoritative_at,version=version+1 WHERE id=request.id RETURNING * INTO request; action:='rejected';
  END IF;
  INSERT INTO public.migration_control_idempotency VALUES(run.tenant_id,requested_actor,'decide_transition',requested_idempotency,requested_hash,request.id,to_jsonb(request),authoritative_at,authoritative_at+interval '7 days');
  PERFORM public.migration_append_control_event(run,requested_actor,requested_session,'migration.transition_'||action,btrim(requested_reason),jsonb_build_object('request_id',request.id,'target_state',request.target_state,'request_version',request.version),authoritative_at);
  RETURN NEXT request;
END $$;
REVOKE ALL ON FUNCTION decide_migration_transition(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION migration_transition_blocked(run migration_runs,target text,manifest uuid,authoritative_at timestamptz) RETURNS text
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE kind text; payload jsonb;
BEGIN
  SELECT m.kind,m.canonical_payload INTO kind,payload FROM public.migration_manifest_versions m WHERE m.id=manifest AND m.migration_id=run.id;
  IF target='validated' AND (kind NOT IN ('inventory','dry_run') OR payload->>'unexplained_diff_count'<>'0') THEN RETURN 'inventory_not_validated'; END IF;
  IF target='shadow' AND EXISTS(SELECT 1 FROM public.migration_import_items WHERE migration_id=run.id AND classification='staged') THEN RETURN 'import_incomplete'; END IF;
  IF target='canary' AND kind<>'canary' THEN RETURN 'signed_canary_manifest_missing'; END IF;
  IF target IN ('canary','cutover_ready','cutover') AND (NOT EXISTS(SELECT 1 FROM public.migration_shadow_comparisons WHERE migration_id=run.id) OR EXISTS(SELECT 1 FROM public.migration_shadow_comparisons WHERE migration_id=run.id AND classification NOT IN ('equal','explained'))) THEN RETURN 'unexplained_shadow_difference'; END IF;
  IF target IN ('canary','cutover_ready','cutover') AND EXISTS(SELECT 1 FROM public.migration_review WHERE migration_id=run.id AND status='open') THEN RETURN 'migration_review_open'; END IF;
  IF target='cutover_ready' AND (kind<>'cutover' OR payload->>'unexplained_diff_count'<>'0') THEN RETURN 'cutover_manifest_missing'; END IF;
  IF target='cutover_ready' AND NOT EXISTS(SELECT 1 FROM public.migration_canary_versions WHERE migration_id=run.id AND percentage>0) THEN RETURN 'canary_evidence_missing'; END IF;
  IF target='decommissioned' AND (run.rollback_deadline IS NULL OR run.rollback_deadline>authoritative_at OR kind<>'decommission' OR (payload#>>'{decommission,backlog_count}')::bigint<>0) THEN RETURN 'decommission_manifest_incomplete'; END IF;
  IF target='decommissioned' AND (SELECT count(*) FROM public.migration_decommission_evidence WHERE migration_id=run.id)<>4 THEN RETURN 'decommission_external_evidence_incomplete'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_review WHERE migration_id=run.id AND status='open') THEN RETURN 'migration_review_open'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_imported_orders io JOIN public.payment_intents i ON i.id=io.intent_id WHERE io.migration_id=run.id AND i.status IN ('created','awaiting_route_selection','pending','observed','partially_paid','confirmed','needs_review','reorg_review')) THEN RETURN 'imported_orders_open'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_imported_orders io JOIN public.amount_reservations a ON a.route_id=io.route_id WHERE io.migration_id=run.id AND a.state='active') THEN RETURN 'imported_reservations_active'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_callback_ownership WHERE migration_id=run.id AND owner<>'platform') THEN RETURN 'callback_ownership_not_transferred'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_callback_ownership o JOIN public.callback_events e ON e.intent_id=o.intent_id JOIN public.callback_deliveries d ON d.callback_event_id=e.id WHERE o.migration_id=run.id AND d.status<>'acknowledged') THEN RETURN 'callback_delivery_backlog'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_import_items WHERE migration_id=run.id AND entity_type='callback_backlog' AND classification NOT IN ('applied','unsupported')) THEN RETURN 'source_callback_backlog'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_event_ownership o JOIN public.unmatched_payments u ON u.event_id=o.platform_event_id WHERE o.migration_id=run.id AND u.status NOT IN ('resolved','ignored','invalid')) THEN RETURN 'unmatched_backlog'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_desired_actions WHERE migration_id=run.id AND status='pending') THEN RETURN 'actuator_action_pending'; END IF;
  IF target='decommissioned' AND EXISTS(SELECT 1 FROM public.migration_worker_leases WHERE migration_id=run.id AND lease_until>authoritative_at) THEN RETURN 'migration_worker_active'; END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION execute_migration_transition(requested_id uuid,requested_tenant uuid,expected_request_version bigint,expected_row_version bigint,expected_fence bigint,requested_reason text,requested_actor uuid,requested_session text,requested_step_up timestamptz,requested_idempotency text,requested_hash bytea)
RETURNS SETOF migration_runs LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); request public.migration_transition_requests%ROWTYPE; run public.migration_runs%ROWTYPE; prior record; blocker text; action_version bigint; desired_action text;
BEGIN
  SELECT * INTO request FROM public.migration_transition_requests WHERE id=requested_id AND tenant_id=requested_tenant FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'transition not found' USING ERRCODE='P0002'; END IF;
  SELECT * INTO run FROM public.migration_runs WHERE id=request.migration_id FOR UPDATE;
  IF NOT public.migration_admin_allowed(requested_actor,run.tenant_id,'migration:execute',requested_session,requested_step_up) THEN RAISE EXCEPTION 'transition execution denied' USING ERRCODE='MP003'; END IF;
  IF expected_request_version<1 OR expected_row_version<1 OR expected_fence<1 OR length(btrim(requested_reason)) NOT BETWEEN 12 AND 1000 OR octet_length(requested_hash)<>32 THEN RAISE EXCEPTION 'transition execution input rejected' USING ERRCODE='MP001'; END IF;
  SELECT * INTO prior FROM public.migration_control_idempotency WHERE tenant_id=run.tenant_id AND actor_id=requested_actor AND operation='execute_transition' AND idempotency_key=requested_idempotency;
  IF FOUND THEN
    IF prior.request_hash<>requested_hash OR prior.resource_id<>run.id THEN RAISE EXCEPTION 'idempotency conflict' USING ERRCODE='MP004'; END IF;
    RETURN NEXT jsonb_populate_record(NULL::public.migration_runs,prior.response_body); RETURN;
  END IF;
  IF request.status<>'approved' OR request.version<>expected_request_version OR request.requested_by=requested_actor OR request.approved_by=requested_actor OR request.expires_at<=authoritative_at OR run.state<>request.from_state OR run.row_version<>expected_row_version OR run.row_version<>request.expected_row_version OR run.fence_token<>expected_fence OR run.fence_token<>request.expected_fence_token THEN RAISE EXCEPTION 'transition execution conflict' USING ERRCODE='MP002'; END IF;
  blocker:=public.migration_transition_blocked(run,request.target_state,request.manifest_id,authoritative_at);
  IF blocker IS NOT NULL THEN RAISE EXCEPTION '%',blocker USING ERRCODE='MP002'; END IF;
  IF request.target_state IN ('cutover','rollback_pending') THEN
    action_version:=run.desired_action_version+1;
    desired_action:=CASE request.target_state WHEN 'cutover' THEN 'activate_platform' ELSE 'restore_legacy' END;
    UPDATE public.migration_runs SET state=CASE WHEN request.target_state='rollback_pending' THEN 'rollback_pending' ELSE state END,desired_action_version=action_version,pending_action=desired_action,pending_target_state=CASE WHEN request.target_state='cutover' THEN 'cutover' ELSE 'rolled_back' END,row_version=row_version+1,fence_token=fence_token+1,updated_at=authoritative_at WHERE id=run.id RETURNING * INTO run;
    INSERT INTO public.migration_desired_actions(migration_id,tenant_id,action_version,action,target_state,fence_token,status,request_id,created_at)
    VALUES(run.id,run.tenant_id,action_version,desired_action,CASE WHEN request.target_state='cutover' THEN 'cutover' ELSE 'rolled_back' END,run.fence_token,'pending',request.id,authoritative_at);
    UPDATE public.migration_transition_requests SET status='awaiting_actuator',executed_by=requested_actor,execution_step_up_at=requested_step_up,execution_reason=btrim(requested_reason),executed_at=authoritative_at,version=version+1 WHERE id=request.id;
  ELSE
    IF request.target_state='canary' THEN PERFORM public.record_migration_canary_version(run.id,request.manifest_id,requested_actor); END IF;
    UPDATE public.migration_runs SET state=request.target_state,
      create_traffic_owner=CASE request.target_state WHEN 'shadow' THEN 'shadow' WHEN 'canary' THEN 'canary' ELSE create_traffic_owner END,
      callback_owner=CASE request.target_state WHEN 'shadow' THEN 'shadow' WHEN 'canary' THEN 'canary' ELSE callback_owner END,
      rollback_deadline=CASE WHEN request.target_state='cutover_ready' THEN (SELECT (canonical_payload#>>'{cutover,rollback_deadline}')::timestamptz FROM public.migration_manifest_versions WHERE id=request.manifest_id) ELSE rollback_deadline END,
      row_version=row_version+1,fence_token=fence_token+1,updated_at=authoritative_at WHERE id=run.id RETURNING * INTO run;
    UPDATE public.migration_transition_requests SET status='executed',executed_by=requested_actor,execution_step_up_at=requested_step_up,execution_reason=btrim(requested_reason),executed_at=authoritative_at,version=version+1 WHERE id=request.id;
  END IF;
  INSERT INTO public.migration_control_idempotency VALUES(run.tenant_id,requested_actor,'execute_transition',requested_idempotency,requested_hash,run.id,to_jsonb(run),authoritative_at,authoritative_at+interval '7 days');
  PERFORM public.migration_append_control_event(run,requested_actor,requested_session,'migration.transition_executed',btrim(requested_reason),jsonb_build_object('request_id',request.id,'target_state',request.target_state,'state',run.state,'fence_token',run.fence_token,'desired_action_version',run.desired_action_version),authoritative_at);
  RETURN NEXT run;
END $$;
REVOKE ALL ON FUNCTION execute_migration_transition(uuid,uuid,bigint,bigint,bigint,text,uuid,text,timestamptz,text,bytea) FROM PUBLIC;

CREATE FUNCTION migration_pending_actuator_action(requested_migration uuid)
RETURNS TABLE(migration_id uuid,action_version bigint,fence_token bigint,action text,target_state text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF NOT pg_has_role(session_user,'migration_traffic_actuator','member') THEN RAISE EXCEPTION 'migration actuator read denied' USING ERRCODE='MP003'; END IF;
  RETURN QUERY SELECT d.migration_id,d.action_version,d.fence_token,d.action,d.target_state
    FROM public.migration_desired_actions d JOIN public.migration_runs r ON r.id=d.migration_id AND r.tenant_id=d.tenant_id
    WHERE d.migration_id=requested_migration AND d.status='pending'
      AND r.pending_action=d.action AND r.desired_action_version=d.action_version
      AND r.fence_token=d.fence_token;
END $$;
REVOKE ALL ON FUNCTION migration_pending_actuator_action(uuid) FROM PUBLIC;

CREATE FUNCTION acknowledge_migration_actuator(requested_migration uuid,requested_version bigint,requested_fence bigint,requested_action text,requested_evidence text,requested_key text,requested_signature text)
RETURNS SETOF migration_runs LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp(); run public.migration_runs%ROWTYPE; desired public.migration_desired_actions%ROWTYPE; request public.migration_transition_requests%ROWTYPE; system_actor uuid:='00000000-0000-0000-0000-000000000021';
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  SELECT * INTO desired FROM public.migration_desired_actions WHERE migration_id=run.id AND action_version=requested_version FOR UPDATE;
  IF NOT FOUND OR run.fence_token<>requested_fence OR desired.fence_token<>requested_fence OR desired.action<>requested_action OR requested_evidence !~ '^[a-f0-9]{64}$' OR requested_key !~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,254}$' OR length(requested_signature) NOT BETWEEN 80 AND 128 THEN RAISE EXCEPTION 'actuator acknowledgement conflict' USING ERRCODE='MP002'; END IF;
  IF desired.status='acknowledged' THEN
    IF desired.evidence_hash<>decode(requested_evidence,'hex') OR desired.acknowledged_by_key_id<>requested_key OR desired.signature<>requested_signature THEN RAISE EXCEPTION 'actuator replay conflict' USING ERRCODE='MP002'; END IF;
    RETURN NEXT run; RETURN;
  END IF;
  SELECT * INTO request FROM public.migration_transition_requests WHERE id=desired.request_id FOR UPDATE;
  IF request.status<>'awaiting_actuator' OR run.desired_action_version<>requested_version OR run.actuator_ack_version>=requested_version THEN RAISE EXCEPTION 'actuator fence conflict' USING ERRCODE='MP002'; END IF;
  UPDATE public.migration_desired_actions SET status='acknowledged',acknowledged_by_key_id=requested_key,evidence_hash=decode(requested_evidence,'hex'),signature=requested_signature,acknowledged_at=authoritative_at WHERE migration_id=run.id AND action_version=requested_version;
  UPDATE public.migration_runs SET state=desired.target_state,create_traffic_owner=CASE requested_action WHEN 'activate_platform' THEN 'platform' ELSE 'legacy' END,callback_owner=CASE requested_action WHEN 'activate_platform' THEN 'platform' ELSE 'legacy' END,actuator_ack_version=requested_version,pending_action=NULL,pending_target_state=NULL,row_version=row_version+1,fence_token=fence_token+1,updated_at=authoritative_at WHERE id=run.id RETURNING * INTO run;
  UPDATE public.migration_callback_ownership o SET owner=CASE
    WHEN requested_action='activate_platform' THEN 'platform'
    WHEN EXISTS(SELECT 1 FROM public.migration_imported_orders io WHERE io.migration_id=run.id AND io.intent_id=o.intent_id) THEN 'legacy'
    ELSE 'platform'
  END,fence_token=run.fence_token,version=version+1,updated_at=authoritative_at WHERE o.migration_id=run.id;
  IF requested_action='restore_legacy' THEN
    UPDATE public.migration_event_ownership o SET owner='shadow',fence_token=run.fence_token,reason='uncredited canary fact returned to shadow on rollback',updated_at=authoritative_at,version=version+1
    WHERE o.migration_id=run.id AND o.owner='platform' AND o.opening_ledger_transaction_id IS NULL
      AND (o.admitted_route_id IS NULL OR EXISTS(SELECT 1 FROM public.migration_imported_orders io WHERE io.migration_id=run.id AND io.route_id=o.admitted_route_id))
      AND NOT EXISTS(SELECT 1 FROM public.payment_matches m WHERE m.state<>'reversed' AND (m.event_id=o.platform_event_id OR m.provider_inbox_id=o.provider_inbox_id));
  END IF;
  UPDATE public.migration_transition_requests SET status='executed',version=version+1 WHERE id=request.id;
  PERFORM public.migration_append_control_event(run,system_actor,'actuator:'||requested_key,'migration.actuator_acknowledged','authenticated migration traffic actuator acknowledgement',jsonb_build_object('request_id',request.id,'action',requested_action,'action_version',requested_version,'evidence_hash',requested_evidence,'key_id',requested_key,'state',run.state,'fence_token',run.fence_token),authoritative_at);
  RETURN NEXT run;
END $$;
REVOKE ALL ON FUNCTION acknowledge_migration_actuator(uuid,bigint,bigint,text,text,text,text) FROM PUBLIC;

CREATE FUNCTION claim_migration_workload(requested_migration uuid,requested_worker text,requested_lease_seconds integer)
RETURNS SETOF migration_worker_leases LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run public.migration_runs%ROWTYPE; authoritative_at timestamptz:=clock_timestamp(); selected_token uuid:=gen_random_uuid();
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  IF NOT pg_has_role(session_user,'migration_control_worker','member') OR run.state NOT IN ('importing','shadow','canary','cutover_ready','cutover','rollback_window','rolled_back') OR requested_worker !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$' OR requested_lease_seconds NOT BETWEEN 5 AND 60 THEN RAISE EXCEPTION 'migration workload claim denied' USING ERRCODE='MP003'; END IF;
  INSERT INTO public.migration_worker_leases(migration_id,tenant_id,worker_id,lease_token,lease_until,fence_token,claimed_at)
  VALUES(run.id,run.tenant_id,requested_worker,selected_token,authoritative_at+make_interval(secs=>requested_lease_seconds),run.fence_token,authoritative_at)
  ON CONFLICT(migration_id) DO UPDATE SET worker_id=excluded.worker_id,lease_token=excluded.lease_token,lease_until=excluded.lease_until,fence_token=excluded.fence_token,claimed_at=excluded.claimed_at
  WHERE migration_worker_leases.lease_until<=authoritative_at OR migration_worker_leases.worker_id=requested_worker;
  IF NOT FOUND THEN RAISE EXCEPTION 'migration workload already leased' USING ERRCODE='MP002'; END IF;
  RETURN QUERY SELECT * FROM public.migration_worker_leases WHERE migration_id=run.id;
END $$;
REVOKE ALL ON FUNCTION claim_migration_workload(uuid,text,integer) FROM PUBLIC;

CREATE FUNCTION migration_worker_lease_valid(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT pg_has_role(session_user,'migration_control_worker','member') AND EXISTS(
   SELECT 1 FROM public.migration_worker_leases l JOIN public.migration_runs r ON r.id=l.migration_id
   WHERE l.migration_id=requested_migration AND l.worker_id=requested_worker AND l.lease_token=requested_token
     AND l.fence_token=requested_fence AND r.fence_token=requested_fence AND l.lease_until>clock_timestamp())
$$;
REVOKE ALL ON FUNCTION migration_worker_lease_valid(uuid,text,uuid,bigint) FROM PUBLIC;

CREATE FUNCTION stage_migration_import_item(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_sequence bigint,requested_type text,requested_source text,requested_payload jsonb)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run public.migration_runs%ROWTYPE; existing public.migration_import_items%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state<>'importing' OR requested_sequence<1 OR requested_type NOT IN ('merchant','configuration','asset','chain','rpc_provider','wallet','open_order','paid_order','expired_order','amount_reservation','incoming_transfer','unmatched_transfer','callback_backlog','scanner_cursor','provider_order','balance_observation') OR jsonb_typeof(requested_payload)<>'object' OR pg_column_size(requested_payload)>65536 THEN RAISE EXCEPTION 'import item rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO existing FROM public.migration_import_items WHERE migration_id=run.id AND source_sequence=requested_sequence FOR UPDATE;
  IF FOUND THEN
    IF existing.entity_type<>requested_type OR existing.source_id<>requested_source OR existing.payload<>requested_payload THEN RAISE EXCEPTION 'mutated import replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  INSERT INTO public.migration_import_items(migration_id,tenant_id,source_sequence,entity_type,source_id,payload,classification,created_at) VALUES(run.id,run.tenant_id,requested_sequence,requested_type,requested_source,requested_payload,'staged',clock_timestamp());
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION stage_migration_import_item(uuid,text,uuid,bigint,bigint,text,text,jsonb) FROM PUBLIC;

CREATE FUNCTION record_migration_shadow_comparison(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_sequence bigint,requested_type text,requested_source text,requested_source_digest text,requested_platform_digest text,requested_classification text,requested_explanation text,requested_observation jsonb)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run public.migration_runs%ROWTYPE; authoritative_at timestamptz:=clock_timestamp(); existing public.migration_shadow_comparisons%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state NOT IN ('shadow','canary','cutover_ready','cutover','rollback_window','rolled_back') OR requested_type NOT IN ('merchant','configuration','asset','chain','rpc_provider','wallet','open_order','paid_order','expired_order','amount_reservation','incoming_transfer','unmatched_transfer','callback_backlog','scanner_cursor','provider_order','balance_observation') OR requested_source_digest !~ '^[a-f0-9]{64}$' OR requested_platform_digest !~ '^[a-f0-9]{64}$' OR requested_classification NOT IN ('equal','explained','missing_platform','missing_source','value_mismatch','identity_conflict') OR (requested_classification='explained')<>(requested_explanation<>'') THEN RAISE EXCEPTION 'shadow comparison rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO existing FROM public.migration_shadow_comparisons WHERE migration_id=run.id AND source_sequence=requested_sequence;
  IF FOUND THEN
    IF existing.entity_type<>requested_type OR existing.source_id<>requested_source OR existing.source_digest<>decode(requested_source_digest,'hex') OR existing.platform_digest<>decode(requested_platform_digest,'hex') OR existing.classification<>requested_classification OR existing.explanation_reference IS DISTINCT FROM NULLIF(requested_explanation,'') OR existing.observation<>requested_observation THEN RAISE EXCEPTION 'mutated shadow replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  INSERT INTO public.migration_shadow_comparisons VALUES(run.id,run.tenant_id,requested_sequence,requested_type,requested_source,decode(requested_source_digest,'hex'),decode(requested_platform_digest,'hex'),requested_classification,NULLIF(requested_explanation,''),requested_observation,authoritative_at);
  IF requested_classification NOT IN ('equal','explained') THEN
    INSERT INTO public.migration_review(id,migration_id,tenant_id,classification,source_reference,evidence_hash,status,created_at)
    VALUES(gen_random_uuid(),run.id,run.tenant_id,'shadow_difference',requested_type||':'||requested_source,digest(requested_observation::text,'sha256'),'open',authoritative_at) ON CONFLICT DO NOTHING;
  END IF;
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION record_migration_shadow_comparison(uuid,text,uuid,bigint,bigint,text,text,text,text,text,text,jsonb) FROM PUBLIC;

CREATE FUNCTION record_migration_decommission_evidence(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_type text,requested_reference text,requested_hash bytea)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; existing migration_decommission_evidence%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state NOT IN ('cutover','rollback_window') OR requested_type NOT IN ('final_reconciliation','encrypted_archive','restore_test','key_revocation') OR requested_reference !~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,254}$' OR octet_length(requested_hash)<>32 THEN RAISE EXCEPTION 'decommission evidence rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO existing FROM public.migration_decommission_evidence WHERE migration_id=run.id AND evidence_type=requested_type;
  IF FOUND THEN
    IF existing.evidence_reference<>requested_reference OR existing.evidence_hash<>requested_hash THEN RAISE EXCEPTION 'mutated decommission evidence replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  INSERT INTO public.migration_decommission_evidence VALUES(run.id,run.tenant_id,requested_type,requested_reference,requested_hash,clock_timestamp());
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION record_migration_decommission_evidence(uuid,text,uuid,bigint,text,text,bytea) FROM PUBLIC;

CREATE FUNCTION record_migration_canary_version(requested_migration uuid,requested_manifest uuid,requested_actor uuid)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run public.migration_runs%ROWTYPE; selected_id uuid:=gen_random_uuid(); next_version bigint; payload jsonb; requested_percentage integer; requested_merchants uuid[]; requested_assets text[];
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  SELECT canonical_payload INTO payload FROM public.migration_manifest_versions WHERE id=requested_manifest AND migration_id=run.id AND kind='canary';
  SELECT (payload#>>'{canary,percentage}')::integer,ARRAY(SELECT jsonb_array_elements_text(payload#>'{canary,merchant_ids}')::uuid),ARRAY(SELECT jsonb_array_elements_text(payload#>'{canary,asset_ids}')) INTO requested_percentage,requested_merchants,requested_assets;
  IF run.state<>'shadow' OR requested_percentage NOT BETWEEN 1 AND 100 OR cardinality(requested_merchants)<1 OR cardinality(requested_assets)<1 OR EXISTS(SELECT 1 FROM unnest(requested_merchants) merchant WHERE NOT EXISTS(SELECT 1 FROM public.merchants m WHERE m.id=merchant AND m.tenant_id=run.tenant_id AND m.status='active')) OR EXISTS(SELECT 1 FROM unnest(requested_assets) asset WHERE NOT EXISTS(SELECT 1 FROM public.assets a JOIN public.platform_config_heads h ON h.kind='asset_contract' AND h.logical_key=a.id JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id WHERE a.id=asset AND a.status='active' AND s.payload->>'status'='active')) THEN RAISE EXCEPTION 'canary cohort rejected' USING ERRCODE='MP002'; END IF;
  SELECT COALESCE(max(version),0)+1 INTO next_version FROM public.migration_canary_versions WHERE migration_id=run.id;
  INSERT INTO public.migration_canary_versions VALUES(selected_id,run.id,run.tenant_id,next_version,requested_percentage,requested_merchants,requested_assets,requested_manifest,digest((payload->'canary')::text,'sha256'),requested_actor,clock_timestamp());
  UPDATE public.migration_callback_ownership o SET owner=CASE WHEN o.intent_id IN (SELECT io.intent_id FROM public.migration_imported_orders io JOIN public.payment_intents i ON i.id=io.intent_id JOIN public.payment_routes r ON r.id=io.route_id WHERE io.migration_id=run.id AND r.asset_id=ANY(requested_assets) AND (i.merchant_id=ANY(requested_merchants) OR mod(hashtextextended(i.merchant_id::text,0) & 9223372036854775807,100)<requested_percentage)) THEN 'platform' ELSE 'shadow' END,version=version+1,updated_at=clock_timestamp() WHERE o.migration_id=run.id;
  RETURN selected_id;
END $$;
REVOKE ALL ON FUNCTION record_migration_canary_version(uuid,uuid,uuid) FROM PUBLIC;

CREATE FUNCTION migration_platform_create_admitted(requested_tenant uuid,requested_merchant uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
  WITH run AS (SELECT * FROM public.migration_runs WHERE tenant_id=requested_tenant AND state<>'decommissioned' LIMIT 1),
  canary AS (SELECT c.* FROM public.migration_canary_versions c JOIN run r ON r.id=c.migration_id ORDER BY c.version DESC LIMIT 1)
  SELECT NOT EXISTS(SELECT 1 FROM run) OR EXISTS(SELECT 1 FROM run WHERE create_traffic_owner='platform') OR
    EXISTS(SELECT 1 FROM run JOIN canary ON true WHERE run.create_traffic_owner='canary' AND (requested_merchant=ANY(canary.merchant_ids) OR mod(hashtextextended(requested_merchant::text,0) & 9223372036854775807,100)<canary.percentage))
$$;
REVOKE ALL ON FUNCTION migration_platform_create_admitted(uuid,uuid) FROM PUBLIC;

CREATE FUNCTION migration_guard_platform_create() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF current_setting('app.migration_import_apply',true)<>'true' AND NOT public.migration_platform_create_admitted(NEW.tenant_id,NEW.merchant_id) THEN RAISE EXCEPTION 'migration create ownership denies platform create' USING ERRCODE='MP003'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_platform_create_fence BEFORE INSERT ON payment_intents FOR EACH ROW EXECUTE FUNCTION migration_guard_platform_create();

CREATE FUNCTION migration_track_callback_ownership() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE;
BEGIN
  IF current_setting('app.migration_import_apply',true)='true' THEN RETURN NEW; END IF;
  SELECT * INTO run FROM public.migration_runs WHERE tenant_id=NEW.tenant_id AND state<>'decommissioned' LIMIT 1;
  IF run.id IS NOT NULL THEN
    INSERT INTO public.migration_callback_ownership(migration_id,tenant_id,intent_id,owner,fence_token,updated_at,version)
    VALUES(run.id,run.tenant_id,NEW.id,'platform',run.fence_token,clock_timestamp(),1)
    ON CONFLICT(intent_id) DO NOTHING;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_callback_ownership_after_create AFTER INSERT ON payment_intents FOR EACH ROW EXECUTE FUNCTION migration_track_callback_ownership();

CREATE FUNCTION migration_guard_platform_route() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; canary migration_canary_versions%ROWTYPE;
BEGIN
  IF current_setting('app.migration_import_apply',true)='true' THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended('migration:'||NEW.tenant_id::text,0));
  SELECT * INTO run FROM public.migration_runs WHERE tenant_id=NEW.tenant_id AND state<>'decommissioned' LIMIT 1;
  IF run.create_traffic_owner='canary' THEN
    SELECT * INTO canary FROM public.migration_canary_versions WHERE migration_id=run.id ORDER BY version DESC LIMIT 1;
    IF canary.id IS NULL OR NOT NEW.asset_id=ANY(canary.asset_ids) OR NOT (NEW.merchant_id=ANY(canary.merchant_ids) OR mod(hashtextextended(NEW.merchant_id::text,0) & 9223372036854775807,100)<canary.percentage) THEN RAISE EXCEPTION 'migration canary asset ownership denies platform route' USING ERRCODE='MP003'; END IF;
  ELSIF run.id IS NOT NULL AND run.create_traffic_owner<>'platform' THEN RAISE EXCEPTION 'migration route ownership denies platform route' USING ERRCODE='MP003'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_platform_route_fence BEFORE INSERT ON payment_routes FOR EACH ROW EXECUTE FUNCTION migration_guard_platform_route();

CREATE FUNCTION migration_protect_watch_address() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF EXISTS(SELECT 1 FROM public.migration_imported_addresses WHERE address_id=OLD.id) THEN
    IF TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['first_used_at','last_used_at','updated_at','version'])<>(to_jsonb(OLD)-ARRAY['first_used_at','last_used_at','updated_at','version']) OR NEW.status IN ('available','retired') THEN
      RAISE EXCEPTION 'imported watch-only address identity and assignment are immutable' USING ERRCODE='MP003';
    END IF;
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER migration_imported_address_never_release BEFORE UPDATE OR DELETE ON addresses FOR EACH ROW EXECUTE FUNCTION migration_protect_watch_address();

CREATE FUNCTION migration_observe_transfer_identity() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run public.migration_runs%ROWTYPE; imported migration_imported_addresses%ROWTYPE; canary migration_canary_versions%ROWTYPE; candidate payment_routes%ROWTYPE; item payment_routes%ROWTYPE; identity text; owner text; candidate_count integer:=0; admitted_route uuid;
BEGIN
  SELECT ia.* INTO imported FROM public.migration_imported_addresses ia JOIN public.addresses a ON a.id=ia.address_id WHERE a.chain_id=NEW.chain_id AND a.canonical_address=NEW.to_address LIMIT 1;
  IF NOT FOUND THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended('migration:'||imported.tenant_id::text,0));
  SELECT * INTO run FROM public.migration_runs WHERE id=imported.migration_id;
  IF NOT FOUND THEN RETURN NEW; END IF;
  identity:=NEW.chain_id||':'||NEW.transaction_id||':'||NEW.event_identity||':'||NEW.asset_id||':'||NEW.to_address;
  owner:=CASE WHEN run.create_traffic_owner='platform' OR run.state='decommissioned' THEN 'platform' ELSE 'shadow' END;
  IF run.create_traffic_owner='canary' THEN
    SELECT * INTO canary FROM public.migration_canary_versions WHERE migration_id=run.id ORDER BY version DESC LIMIT 1 FOR SHARE;
    FOR item IN SELECT r.* FROM public.payment_routes r WHERE r.tenant_id=run.tenant_id AND r.provider='on_chain' AND r.status='active' AND r.chain_id=NEW.chain_id AND r.asset_id=NEW.asset_id AND r.receiving_address=NEW.to_address AND r.expected_amount_atomic=NEW.amount_atomic AND NEW.on_chain_time>=r.starts_at AND NEW.on_chain_time<r.grace_ends_at AND r.asset_id=ANY(canary.asset_ids) AND (r.merchant_id=ANY(canary.merchant_ids) OR mod(hashtextextended(r.merchant_id::text,0) & 9223372036854775807,100)<canary.percentage) ORDER BY r.id FOR SHARE LOOP
      candidate_count:=candidate_count+1; candidate:=item; IF candidate_count>1 THEN EXIT; END IF;
    END LOOP;
    IF candidate_count=1 THEN owner:='platform'; admitted_route:=candidate.id; END IF;
  ELSIF run.create_traffic_owner<>'platform' THEN
    -- A rollback changes admission for new traffic, never ownership of an
    -- already-created platform route. Preserve an exact unambiguous live route
    -- whose intent is still platform callback-owned.
    FOR item IN SELECT r.* FROM public.payment_routes r JOIN public.migration_callback_ownership o ON o.migration_id=run.id AND o.intent_id=r.intent_id AND o.owner='platform' WHERE r.tenant_id=run.tenant_id AND r.provider='on_chain' AND r.status='active' AND r.chain_id=NEW.chain_id AND r.asset_id=NEW.asset_id AND r.receiving_address=NEW.to_address AND r.expected_amount_atomic=NEW.amount_atomic AND NEW.on_chain_time>=r.starts_at AND NEW.on_chain_time<r.grace_ends_at ORDER BY r.id FOR SHARE OF r,o LOOP
      candidate_count:=candidate_count+1; candidate:=item; IF candidate_count>1 THEN EXIT; END IF;
    END LOOP;
    IF candidate_count=1 THEN owner:='platform'; admitted_route:=candidate.id; END IF;
  END IF;
  INSERT INTO public.migration_event_ownership(migration_id,tenant_id,event_identity,platform_event_id,admitted_route_id,owner,fence_token,reason,created_at,updated_at)
  VALUES(run.id,run.tenant_id,identity,NEW.id,admitted_route,owner,run.fence_token,CASE WHEN admitted_route IS NULL THEN 'database observed imported-address event identity' ELSE 'exact canary route admitted imported-address event' END,clock_timestamp(),clock_timestamp())
  ON CONFLICT(migration_id,event_identity) DO UPDATE SET platform_event_id=excluded.platform_event_id,admitted_route_id=COALESCE(migration_event_ownership.admitted_route_id,excluded.admitted_route_id),updated_at=excluded.updated_at,version=migration_event_ownership.version+1
    WHERE migration_event_ownership.platform_event_id IS NULL AND migration_event_ownership.provider_inbox_id IS NULL
  RETURNING migration_event_ownership.owner INTO owner;
  IF owner<>'platform' THEN INSERT INTO public.migration_review(id,migration_id,tenant_id,classification,source_reference,evidence_hash,status,created_at) VALUES(gen_random_uuid(),run.id,run.tenant_id,'unknown_event_identity',identity,digest(identity,'sha256'),'open',clock_timestamp()) ON CONFLICT DO NOTHING; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_transfer_observation AFTER INSERT ON transfer_events FOR EACH ROW EXECUTE FUNCTION migration_observe_transfer_identity();

CREATE FUNCTION migration_observe_provider_identity() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; route payment_routes%ROWTYPE; canary migration_canary_versions%ROWTYPE; identity text; selected_owner text:='shadow';
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE tenant_id=NEW.tenant_id AND state<>'decommissioned' LIMIT 1;
  IF NOT FOUND THEN RETURN NEW; END IF;
  SELECT * INTO route FROM public.payment_routes WHERE id=NEW.route_id AND tenant_id=run.tenant_id;
  IF run.create_traffic_owner='platform' THEN
    selected_owner:='platform';
  ELSIF run.create_traffic_owner='canary' THEN
    SELECT * INTO canary FROM public.migration_canary_versions WHERE migration_id=run.id ORDER BY version DESC LIMIT 1;
    IF canary.id IS NOT NULL AND route.created_at>=canary.created_at AND route.asset_id=ANY(canary.asset_ids) AND (route.merchant_id=ANY(canary.merchant_ids) OR mod(hashtextextended(route.merchant_id::text,0) & 9223372036854775807,100)<canary.percentage) THEN selected_owner:='platform'; END IF;
  ELSIF EXISTS(SELECT 1 FROM public.migration_callback_ownership o WHERE o.migration_id=run.id AND o.intent_id=route.intent_id AND o.owner='platform') THEN
    -- Rollback restores admission of new traffic to legacy, but an already
    -- created platform intent remains platform-owned until its route closes.
    selected_owner:='platform';
  END IF;
  identity:='provider:'||NEW.provider_id||':'||NEW.provider_event_id;
  INSERT INTO public.migration_event_ownership(migration_id,tenant_id,event_identity,provider_inbox_id,admitted_route_id,owner,fence_token,reason,created_at,updated_at)
  VALUES(run.id,run.tenant_id,identity,NEW.id,CASE WHEN selected_owner='platform' THEN route.id END,selected_owner,run.fence_token,'database observed hosted-provider event identity',clock_timestamp(),clock_timestamp());
  IF selected_owner<>'platform' THEN INSERT INTO public.migration_review(id,migration_id,tenant_id,classification,source_reference,evidence_hash,status,created_at) VALUES(gen_random_uuid(),run.id,run.tenant_id,'unknown_event_identity',identity,digest(identity,'sha256'),'open',clock_timestamp()) ON CONFLICT DO NOTHING; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_provider_observation AFTER INSERT ON provider_inbox FOR EACH ROW EXECUTE FUNCTION migration_observe_provider_identity();

CREATE FUNCTION migration_guard_payment_credit() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE imported_run uuid; ownership migration_event_ownership%ROWTYPE;
BEGIN
  IF NEW.event_id IS NOT NULL THEN SELECT * INTO ownership FROM public.migration_event_ownership WHERE platform_event_id=NEW.event_id FOR SHARE;
  ELSE SELECT * INTO ownership FROM public.migration_event_ownership WHERE provider_inbox_id=NEW.provider_inbox_id FOR SHARE; END IF;
  IF FOUND AND (ownership.owner<>'platform' OR ownership.opening_ledger_transaction_id IS NOT NULL OR (ownership.admitted_route_id IS NOT NULL AND ownership.admitted_route_id<>NEW.route_id)) THEN RAISE EXCEPTION 'migration event ownership denies platform credit' USING ERRCODE='MP003'; END IF;
  SELECT io.migration_id INTO imported_run FROM public.migration_imported_orders io WHERE io.intent_id=NEW.intent_id;
  IF imported_run IS NOT NULL AND ownership.migration_id IS NULL THEN RAISE EXCEPTION 'imported intent lacks migration event ownership' USING ERRCODE='MP003'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_payment_credit_fence BEFORE INSERT OR UPDATE OF state,credited_atomic ON payment_matches FOR EACH ROW EXECUTE FUNCTION migration_guard_payment_credit();

CREATE FUNCTION migration_suppress_platform_callback() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE owner text;
BEGIN
  SELECT o.owner INTO owner FROM public.callback_events e JOIN public.migration_callback_ownership o ON o.intent_id=e.intent_id WHERE e.id=NEW.callback_event_id;
  IF owner IS NOT NULL AND owner<>'platform' THEN RETURN NULL; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER migration_callback_delivery_fence BEFORE INSERT ON callback_deliveries FOR EACH ROW EXECUTE FUNCTION migration_suppress_platform_callback();

-- Import watch-only addresses and orders. Paid source orders enter review and
-- cannot post a balance until independent evidence is recorded and consumed.
CREATE FUNCTION migration_apply_watch_address(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_source text,requested_wallet uuid,requested_address uuid,requested_chain text,requested_canonical text,requested_display text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; wallet wallets%ROWTYPE; imported migration_imported_addresses%ROWTYPE; existing addresses%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  SELECT * INTO wallet FROM public.wallets WHERE id=requested_wallet AND tenant_id=run.tenant_id AND chain_id=requested_chain;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state<>'importing' OR wallet.custody_mode<>'watch_only' OR wallet.signer_key_reference IS NOT NULL THEN RAISE EXCEPTION 'watch address import rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO imported FROM public.migration_imported_addresses WHERE migration_id=run.id AND source_id=requested_source FOR UPDATE;
  IF FOUND THEN
    SELECT * INTO existing FROM public.addresses WHERE id=imported.address_id AND tenant_id=run.tenant_id;
    IF imported.address_id<>requested_address OR existing.wallet_id<>requested_wallet OR existing.chain_id<>requested_chain OR existing.canonical_address<>requested_canonical OR existing.display_address<>requested_display OR existing.purpose<>'deposit' OR existing.status<>'assigned' THEN RAISE EXCEPTION 'mutated watch address replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  IF NOT EXISTS(SELECT 1 FROM public.migration_import_items WHERE migration_id=run.id AND entity_type='wallet' AND source_id=requested_source AND classification='staged') THEN RAISE EXCEPTION 'watch address lacks staged manifest item' USING ERRCODE='MP002'; END IF;
  PERFORM set_config('app.migration_import_apply','true',true);
  INSERT INTO public.addresses(id,tenant_id,wallet_id,chain_id,canonical_address,display_address,purpose,status,created_at,updated_at)
  VALUES(requested_address,run.tenant_id,wallet.id,requested_chain,requested_canonical,requested_display,'deposit','assigned',clock_timestamp(),clock_timestamp());
  INSERT INTO public.migration_imported_addresses VALUES(requested_address,run.id,run.tenant_id,requested_source,clock_timestamp());
  UPDATE public.migration_import_items SET classification='applied',platform_resource_id=requested_address,classified_at=clock_timestamp() WHERE migration_id=run.id AND entity_type='wallet' AND source_id=requested_source AND classification='staged';
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION migration_apply_watch_address(uuid,text,uuid,bigint,text,uuid,uuid,text,text,text) FROM PUBLIC;

CREATE FUNCTION migration_apply_order(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_source text,requested_status text,requested_intent uuid,requested_route uuid,requested_reservation uuid,requested_merchant uuid,requested_order text,requested_amount_minor numeric,requested_currency text,requested_scale smallint,requested_chain text,requested_asset text,requested_amount_atomic numeric,requested_decimals smallint,requested_address text,requested_created timestamptz,requested_expires timestamptz,requested_finality_snapshot uuid,requested_finality_fence bigint,requested_required_finality bigint,requested_matching_policy uuid,requested_matching_version bigint,requested_matching_hash bytea)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; intent_status intent_status; route_state route_status; authoritative_at timestamptz:=clock_timestamp(); matching automated_matching_policies%ROWTYPE; snapshot jsonb; imported_payload jsonb; bound payment_route_policy_bindings%ROWTYPE; imported migration_imported_orders%ROWTYPE; existing_intent payment_intents%ROWTYPE; existing_route payment_routes%ROWTYPE; existing_reservation amount_reservations%ROWTYPE;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  SELECT payload INTO imported_payload FROM public.migration_import_items WHERE migration_id=run.id AND entity_type=requested_status||'_order' AND source_id=requested_source AND classification IN ('staged','applied') FOR UPDATE;
  SELECT * INTO matching FROM public.automated_matching_policies WHERE id=requested_matching_policy AND tenant_id=run.tenant_id AND merchant_id=requested_merchant AND version=requested_matching_version AND config_hash=requested_matching_hash FOR SHARE;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state<>'importing' OR requested_status NOT IN ('open','paid','expired') OR requested_amount_minor<=0 OR requested_amount_atomic<=0 OR requested_created>=requested_expires OR requested_required_finality<=0 OR (requested_status='open')<>(requested_reservation IS NOT NULL) OR matching.id IS NULL OR NOT EXISTS(SELECT 1 FROM public.merchants WHERE id=requested_merchant AND tenant_id=run.tenant_id AND status='active') OR NOT EXISTS(SELECT 1 FROM public.assets WHERE id=requested_asset AND chain_id=requested_chain AND status='active') OR NOT EXISTS(SELECT 1 FROM public.platform_config_heads h JOIN public.platform_config_snapshots s ON s.id=h.snapshot_id AND s.scope_id=h.scope_id WHERE h.scope_id=public.platform_scope_uuid(NULL) AND h.kind='finality_policy' AND h.logical_key=requested_chain AND h.snapshot_id=requested_finality_snapshot AND h.fence_token=requested_finality_fence AND s.payload->>'chain_ref'=requested_chain AND (s.payload->>'confirmations')::bigint=requested_required_finality AND requested_required_finality>0 FOR SHARE OF h,s) OR NOT EXISTS(SELECT 1 FROM public.automated_matching_policies current_policy WHERE current_policy.tenant_id=run.tenant_id AND current_policy.merchant_id=requested_merchant AND current_policy.id=requested_matching_policy AND current_policy.version=(SELECT max(p.version) FROM public.automated_matching_policies p WHERE p.tenant_id=run.tenant_id AND p.merchant_id=requested_merchant)) OR imported_payload IS NULL OR imported_payload->>'finality_snapshot_id'<>requested_finality_snapshot::text OR (imported_payload->>'finality_fence')::bigint<>requested_finality_fence OR (imported_payload->>'required_finality')::bigint<>requested_required_finality OR imported_payload->>'matching_policy_id'<>requested_matching_policy::text OR (imported_payload->>'matching_policy_version')::bigint<>requested_matching_version OR imported_payload->>'matching_policy_hash'<>encode(requested_matching_hash,'hex') THEN RAISE EXCEPTION 'order import policy evidence rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO imported FROM public.migration_imported_orders WHERE migration_id=run.id AND source_id=requested_source FOR UPDATE;
  IF FOUND THEN
    SELECT * INTO existing_intent FROM public.payment_intents WHERE id=imported.intent_id AND tenant_id=run.tenant_id;
    SELECT * INTO existing_route FROM public.payment_routes WHERE id=imported.route_id AND tenant_id=run.tenant_id;
    SELECT * INTO bound FROM public.payment_route_policy_bindings WHERE route_id=imported.route_id;
    IF requested_status='open' THEN SELECT * INTO existing_reservation FROM public.amount_reservations WHERE id=requested_reservation AND route_id=imported.route_id; END IF;
    IF imported.source_status<>requested_status OR imported.intent_id<>requested_intent OR imported.route_id<>requested_route OR imported.legacy_amount_minor<>requested_amount_minor OR imported.legacy_amount_atomic<>requested_amount_atomic OR imported.legacy_receiving_address<>requested_address OR imported.legacy_expires_at<>requested_expires OR imported.finality_snapshot_id<>requested_finality_snapshot OR imported.finality_fence<>requested_finality_fence OR imported.required_finality<>requested_required_finality OR imported.matching_policy_id<>requested_matching_policy OR imported.matching_policy_version<>requested_matching_version OR imported.matching_policy_hash<>requested_matching_hash OR existing_intent.merchant_id<>requested_merchant OR existing_intent.merchant_order_id<>requested_order OR existing_intent.amount_minor<>requested_amount_minor OR existing_intent.currency<>requested_currency OR existing_intent.currency_scale<>requested_scale OR existing_intent.created_at<>requested_created OR existing_intent.expires_at<>requested_expires OR existing_route.provider<>'on_chain' OR existing_route.chain_id<>requested_chain OR existing_route.asset_id<>requested_asset OR existing_route.expected_amount_atomic<>requested_amount_atomic OR existing_route.asset_decimals<>requested_decimals OR existing_route.receiving_address<>requested_address OR existing_route.required_finality<>requested_required_finality OR existing_route.created_at<>requested_created OR existing_route.expires_at<>requested_expires OR bound.policy_id<>requested_matching_policy OR bound.policy_version<>requested_matching_version OR bound.config_hash<>requested_matching_hash OR (requested_status='open' AND (existing_reservation.id IS NULL OR existing_reservation.chain_id<>requested_chain OR existing_reservation.receiving_address<>requested_address OR existing_reservation.asset_id<>requested_asset OR existing_reservation.exact_amount_atomic<>requested_amount_atomic OR lower(existing_reservation.active_window)<>requested_created OR upper(existing_reservation.active_window)<>requested_expires)) THEN RAISE EXCEPTION 'mutated order replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  intent_status:=CASE requested_status WHEN 'open' THEN 'pending'::intent_status WHEN 'paid' THEN 'needs_review'::intent_status ELSE 'expired'::intent_status END;
  route_state:=CASE requested_status WHEN 'open' THEN 'active'::route_status ELSE 'expired'::route_status END;
  PERFORM set_config('app.migration_import_apply','true',true);
  INSERT INTO public.payment_intents(id,tenant_id,merchant_id,merchant_order_id,amount_minor,currency,currency_scale,status,status_reason,policy_snapshot,version,created_at,updated_at,expires_at)
  VALUES(requested_intent,run.tenant_id,requested_merchant,requested_order,requested_amount_minor,requested_currency,requested_scale,intent_status,'migration_'||requested_status,jsonb_build_object('migration_id',run.id,'source_id',requested_source),1,requested_created,authoritative_at,requested_expires);
  INSERT INTO public.payment_routes(id,tenant_id,merchant_id,intent_id,chain_id,asset_id,provider,expected_amount_atomic,asset_decimals,display_amount,receiving_address,required_finality,status,starts_at,expires_at,grace_ends_at,created_at,updated_at)
  VALUES(requested_route,run.tenant_id,requested_merchant,requested_intent,requested_chain,requested_asset,'on_chain',requested_amount_atomic,requested_decimals,requested_amount_atomic::text,requested_address,requested_required_finality,route_state,requested_created,requested_expires,requested_expires,requested_created,authoritative_at);
  snapshot:=jsonb_build_object('id',matching.id::text,'version',matching.version,'accumulate_partials',matching.accumulate_partials,'underpayment_tolerance_bps',matching.underpayment_tolerance_bps,'overpayment_mode',matching.overpayment_mode,'accept_late_within_grace',matching.accept_late_within_grace,'require_same_sender',matching.require_same_sender,'gasfree_enabled',matching.gasfree_enabled,'gasfree_fee_collectors',to_jsonb(matching.gasfree_fee_collectors));
  SELECT * INTO bound FROM public.payment_route_policy_bindings WHERE route_id=requested_route;
  IF bound.route_id IS NULL THEN
    INSERT INTO public.payment_route_policy_bindings(route_id,tenant_id,merchant_id,policy_id,policy_version,policy_snapshot,config_hash,bound_at) VALUES(requested_route,run.tenant_id,requested_merchant,matching.id,matching.version,snapshot,matching.config_hash,authoritative_at);
  ELSIF bound.policy_id<>matching.id OR bound.policy_version<>matching.version OR bound.config_hash<>matching.config_hash OR bound.policy_snapshot<>snapshot THEN RAISE EXCEPTION 'implicit historical matching policy differs from signed import evidence' USING ERRCODE='MP002'; END IF;
  IF requested_status='open' THEN
    INSERT INTO public.amount_reservations(id,tenant_id,route_id,chain_id,receiving_address,asset_id,exact_amount_atomic,active_window,state,created_at,updated_at)
    VALUES(requested_reservation,run.tenant_id,requested_route,requested_chain,requested_address,requested_asset,requested_amount_atomic,tstzrange(requested_created,requested_expires,'[)'),'active',requested_created,authoritative_at);
  END IF;
  INSERT INTO public.migration_imported_orders(migration_id,tenant_id,source_id,source_status,intent_id,route_id,legacy_amount_minor,legacy_amount_atomic,legacy_receiving_address,legacy_expires_at,finality_snapshot_id,finality_fence,required_finality,matching_policy_id,matching_policy_version,matching_policy_hash,imported_at) VALUES(run.id,run.tenant_id,requested_source,requested_status,requested_intent,requested_route,requested_amount_minor,requested_amount_atomic,requested_address,requested_expires,requested_finality_snapshot,requested_finality_fence,requested_required_finality,matching.id,matching.version,matching.config_hash,authoritative_at);
  INSERT INTO public.migration_callback_ownership VALUES(run.id,run.tenant_id,requested_intent,'legacy',run.fence_token,authoritative_at,1);
  IF requested_status='paid' THEN INSERT INTO public.migration_review(id,migration_id,tenant_id,classification,source_reference,evidence_hash,status,created_at) VALUES(gen_random_uuid(),run.id,run.tenant_id,'unverified_paid_order',requested_source,digest(requested_source,'sha256'),'open',authoritative_at); END IF;
  UPDATE public.migration_import_items SET classification='applied',platform_resource_id=requested_intent,classified_at=authoritative_at WHERE migration_id=run.id AND entity_type=requested_status||'_order' AND source_id=requested_source AND classification='staged';
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION migration_apply_order(uuid,text,uuid,bigint,text,text,uuid,uuid,uuid,uuid,text,numeric,text,smallint,text,text,numeric,smallint,text,timestamptz,timestamptz,uuid,bigint,bigint,uuid,bigint,bytea) FROM PUBLIC;

CREATE FUNCTION migration_record_payment_verification(requested_id uuid,requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_source text,requested_fact bytea,requested_evidence bytea,requested_verifiers text[],requested_verifier_version bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; imported migration_imported_orders%ROWTYPE; route payment_routes%ROWTYPE; fact jsonb; existing migration_verification_evidence%ROWTYPE; canonical_identity text;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration;
  SELECT * INTO imported FROM public.migration_imported_orders WHERE migration_id=run.id AND source_id=requested_source;
  SELECT * INTO route FROM public.payment_routes WHERE id=imported.route_id AND tenant_id=run.tenant_id;
  BEGIN fact:=convert_from(requested_fact,'UTF8')::jsonb; EXCEPTION WHEN OTHERS THEN RAISE EXCEPTION 'verification fact JSON rejected' USING ERRCODE='MP001'; END;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state NOT IN ('importing','shadow') OR imported.source_status<>'paid' OR route.id IS NULL OR jsonb_typeof(fact)<>'object' OR ARRAY(SELECT jsonb_object_keys(fact) ORDER BY 1)<>ARRAY['amount_atomic','asset_id','block_hash','chain_id','confirmations','event_identity','observed_at','receiving_address','schema_version','transaction_id']::text[] OR fact->>'schema_version'<>'migration-verification-fact-v1' OR fact->>'chain_id'<>route.chain_id OR fact->>'asset_id'<>route.asset_id OR fact->>'receiving_address'<>route.receiving_address OR (fact->>'amount_atomic')::numeric<>route.expected_amount_atomic OR (fact->>'amount_atomic')::numeric<>imported.legacy_amount_atomic OR (fact->>'confirmations')::bigint<route.required_finality OR (fact->>'observed_at')::timestamptz NOT BETWEEN clock_timestamp()-interval '30 days' AND clock_timestamp()+interval '10 minutes' OR length(fact->>'transaction_id') NOT BETWEEN 1 AND 128 OR length(fact->>'event_identity') NOT BETWEEN 1 AND 128 OR length(fact->>'block_hash') NOT BETWEEN 16 AND 256 OR octet_length(requested_evidence)<>32 OR requested_evidence<>digest(requested_fact,'sha256') OR cardinality(requested_verifiers)<2 OR cardinality(ARRAY(SELECT DISTINCT verifier FROM unnest(requested_verifiers) verifier))<>cardinality(requested_verifiers) OR requested_verifier_version<1 THEN RAISE EXCEPTION 'independent quorum verification rejected' USING ERRCODE='MP002'; END IF;
  canonical_identity:=(fact->>'chain_id')||':'||(fact->>'transaction_id')||':'||(fact->>'event_identity')||':'||(fact->>'asset_id')||':'||(fact->>'receiving_address');
  SELECT * INTO existing FROM public.migration_verification_evidence WHERE migration_id=run.id AND source_id=requested_source FOR UPDATE;
  IF FOUND THEN
    IF existing.canonical_fact<>requested_fact OR existing.evidence_hash<>requested_evidence OR existing.verifier_key_ids<>requested_verifiers OR existing.verifier_version<>requested_verifier_version THEN RAISE EXCEPTION 'mutated verification replay' USING ERRCODE='MP004'; END IF;
    RETURN true;
  END IF;
  INSERT INTO public.migration_verification_evidence(id,migration_id,tenant_id,source_id,event_identity,transaction_id,chain_id,asset_id,receiving_address,amount_atomic,confirmations,canonical_fact,evidence_hash,verifier_key_ids,verifier_version,verified_at)
  VALUES(requested_id,run.id,run.tenant_id,requested_source,canonical_identity,fact->>'transaction_id',fact->>'chain_id',fact->>'asset_id',fact->>'receiving_address',(fact->>'amount_atomic')::numeric,(fact->>'confirmations')::bigint,requested_fact,requested_evidence,requested_verifiers,requested_verifier_version,clock_timestamp());
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION migration_record_payment_verification(uuid,uuid,text,uuid,bigint,text,bytea,bytea,text[],bigint) FROM PUBLIC;

CREATE FUNCTION migration_post_verified_opening(requested_migration uuid,requested_worker text,requested_token uuid,requested_fence bigint,requested_source text,requested_ledger uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE run migration_runs%ROWTYPE; imported migration_imported_orders%ROWTYPE; evidence migration_verification_evidence%ROWTYPE; route payment_routes%ROWTYPE; intent payment_intents%ROWTYPE; ownership migration_event_ownership%ROWTYPE; authoritative_at timestamptz:=clock_timestamp(); debit_account uuid; credit_account uuid;
BEGIN
  SELECT * INTO run FROM public.migration_runs WHERE id=requested_migration FOR UPDATE;
  SELECT * INTO imported FROM public.migration_imported_orders WHERE migration_id=run.id AND source_id=requested_source;
  SELECT * INTO evidence FROM public.migration_verification_evidence WHERE migration_id=run.id AND source_id=requested_source FOR UPDATE;
  SELECT * INTO route FROM public.payment_routes WHERE id=imported.route_id AND tenant_id=run.tenant_id;
  SELECT * INTO intent FROM public.payment_intents WHERE id=imported.intent_id AND tenant_id=run.tenant_id FOR UPDATE;
  IF evidence.ledger_transaction_id=requested_ledger THEN RETURN true; END IF;
  IF NOT public.migration_worker_lease_valid(run.id,requested_worker,requested_token,requested_fence) OR run.state NOT IN ('importing','shadow') OR evidence.id IS NULL OR evidence.ledger_transaction_id IS NOT NULL OR intent.status<>'needs_review' OR route.chain_id<>evidence.chain_id OR route.asset_id<>evidence.asset_id OR route.receiving_address<>evidence.receiving_address OR route.expected_amount_atomic<>evidence.amount_atomic OR imported.legacy_amount_atomic<>evidence.amount_atomic OR evidence.confirmations<route.required_finality THEN RAISE EXCEPTION 'verified opening rejected' USING ERRCODE='MP002'; END IF;
  SELECT * INTO ownership FROM public.migration_event_ownership WHERE migration_id=run.id AND event_identity=evidence.event_identity FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO public.migration_event_ownership(migration_id,tenant_id,event_identity,owner,fence_token,reason,created_at,updated_at)
    VALUES(run.id,run.tenant_id,evidence.event_identity,'shadow',run.fence_token,'independently verified imported paid order pending opening',authoritative_at,authoritative_at) RETURNING * INTO ownership;
  END IF;
  IF ownership.opening_ledger_transaction_id IS NOT NULL OR EXISTS(SELECT 1 FROM public.payment_matches WHERE state<>'reversed' AND (event_id=ownership.platform_event_id OR provider_inbox_id=ownership.provider_inbox_id)) THEN RAISE EXCEPTION 'verified event already credited' USING ERRCODE='MP002'; END IF;
  INSERT INTO public.ledger_accounts(id,tenant_id,merchant_id,asset_id,account_code,account_type,created_at) VALUES
    (gen_random_uuid(),run.tenant_id,intent.merchant_id,evidence.asset_id,'treasury_asset','asset',authoritative_at),
    (gen_random_uuid(),run.tenant_id,intent.merchant_id,evidence.asset_id,'merchant_settlement_liability','liability',authoritative_at)
  ON CONFLICT(tenant_id,merchant_id,asset_id,account_code) DO NOTHING;
  SELECT id INTO debit_account FROM public.ledger_accounts WHERE tenant_id=run.tenant_id AND merchant_id=intent.merchant_id AND asset_id=evidence.asset_id AND account_code='treasury_asset';
  SELECT id INTO credit_account FROM public.ledger_accounts WHERE tenant_id=run.tenant_id AND merchant_id=intent.merchant_id AND asset_id=evidence.asset_id AND account_code='merchant_settlement_liability';
  INSERT INTO public.ledger_transactions(id,tenant_id,business_type,business_reference,effective_at,booked_at,correlation_id,policy_version) VALUES(requested_ledger,run.tenant_id,'migration_opening','migration:'||run.id||':'||requested_source,evidence.verified_at,authoritative_at,evidence.transaction_id,1);
  INSERT INTO public.ledger_entries(transaction_id,tenant_id,sequence,account_id,asset_id,direction,amount_atomic,created_at) VALUES
    (requested_ledger,run.tenant_id,1,debit_account,evidence.asset_id,'debit',evidence.amount_atomic,authoritative_at),
    (requested_ledger,run.tenant_id,2,credit_account,evidence.asset_id,'credit',evidence.amount_atomic,authoritative_at);
  UPDATE public.migration_event_ownership SET owner='platform',admitted_route_id=route.id,opening_ledger_transaction_id=requested_ledger,fence_token=run.fence_token,reason='independently verified imported paid order',updated_at=authoritative_at,version=version+1 WHERE migration_id=run.id AND event_identity=evidence.event_identity;
  UPDATE public.migration_verification_evidence SET ledger_transaction_id=requested_ledger WHERE id=evidence.id;
  UPDATE public.payment_intents SET status='settled',status_reason='migration_independently_verified',settled_at=authoritative_at,updated_at=authoritative_at,version=version+1 WHERE id=intent.id AND status='needs_review';
  UPDATE public.migration_review SET status='explained',explanation_reference='ledger:'||requested_ledger,explained_at=authoritative_at WHERE migration_id=run.id AND classification='unverified_paid_order' AND source_reference=requested_source AND status='open';
  UPDATE public.migration_review SET status='explained',explanation_reference='ledger:'||requested_ledger,explained_at=authoritative_at WHERE migration_id=run.id AND classification='unknown_event_identity' AND source_reference=evidence.event_identity AND status='open';
  RETURN true;
END $$;
REVOKE ALL ON FUNCTION migration_post_verified_opening(uuid,text,uuid,bigint,text,uuid) FROM PUBLIC;

INSERT INTO admin_permissions(permission_key,description) VALUES
('migration:read','Read secret-free migration state and evidence'),
('migration:request','Prepare manifests and request migration transitions'),
('migration:approve','Independently approve migration transitions'),
('migration:execute','Execute an approved transition as a distinct senior actor');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('auditor','migration:read'),('security_admin','migration:read'),('security_admin','migration:request'),
('senior_approver','migration:read'),('senior_approver','migration:approve'),('senior_approver','migration:execute');

DO $$ DECLARE table_name text; BEGIN
  FOREACH table_name IN ARRAY ARRAY['migration_runs','migration_manifest_versions','migration_transition_requests','migration_control_idempotency','migration_worker_leases','migration_import_items','migration_imported_addresses','migration_imported_orders','migration_verification_evidence','migration_event_ownership','migration_review','migration_shadow_comparisons','migration_callback_ownership','migration_shadow_callback_comparisons','migration_canary_versions','migration_desired_actions','migration_decommission_evidence'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',table_name);
    EXECUTE format('CREATE POLICY %I_tenant_scope ON %I USING (tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'',''))) WITH CHECK (tenant_id::text = ANY(string_to_array(current_setting(''app.platform_admin_tenants'',true),'','')))',table_name,table_name);
    EXECUTE format('REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON %I FROM PUBLIC',table_name);
  END LOOP;
END $$;

REVOKE ALL ON FUNCTION migration_state_edge(text,text),migration_transition_blocked(migration_runs,text,uuid,timestamptz),migration_platform_create_admitted(uuid,uuid) FROM PUBLIC;
REVOKE UPDATE,DELETE,TRUNCATE ON migration_manifest_versions,migration_shadow_comparisons,migration_shadow_callback_comparisons,migration_canary_versions,migration_verification_evidence FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='platform_admin_runtime') THEN
    GRANT SELECT ON migration_runs,migration_manifest_versions,migration_transition_requests,migration_import_items,migration_imported_addresses,migration_imported_orders,migration_verification_evidence,migration_event_ownership,migration_review,migration_shadow_comparisons,migration_callback_ownership,migration_shadow_callback_comparisons,migration_canary_versions,migration_desired_actions,migration_decommission_evidence TO platform_admin_runtime;
    GRANT EXECUTE ON FUNCTION create_migration_run(uuid,text,text,text,uuid,text,timestamptz,text,bytea),attach_migration_manifest(uuid,bigint,uuid,text,bytea,bytea,text[],uuid,text,timestamptz,text,text,bytea),request_migration_transition(uuid,uuid,text,bigint,bigint,uuid,text,uuid,text,timestamptz,text,bytea),decide_migration_transition(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,text,bytea),execute_migration_transition(uuid,uuid,bigint,bigint,bigint,text,uuid,text,timestamptz,text,bytea) TO platform_admin_runtime;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='migration_control_worker') THEN
    GRANT EXECUTE ON FUNCTION claim_migration_workload(uuid,text,integer),stage_migration_import_item(uuid,text,uuid,bigint,bigint,text,text,jsonb),record_migration_shadow_comparison(uuid,text,uuid,bigint,bigint,text,text,text,text,text,text,jsonb),record_migration_decommission_evidence(uuid,text,uuid,bigint,text,text,bytea),migration_apply_watch_address(uuid,text,uuid,bigint,text,uuid,uuid,text,text,text),migration_apply_order(uuid,text,uuid,bigint,text,text,uuid,uuid,uuid,uuid,text,numeric,text,smallint,text,text,numeric,smallint,text,timestamptz,timestamptz,uuid,bigint,bigint,uuid,bigint,bytea),migration_record_payment_verification(uuid,uuid,text,uuid,bigint,text,bytea,bytea,text[],bigint),migration_post_verified_opening(uuid,text,uuid,bigint,text,uuid) TO migration_control_worker;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='migration_traffic_actuator') THEN
    GRANT EXECUTE ON FUNCTION migration_pending_actuator_action(uuid),acknowledge_migration_actuator(uuid,bigint,bigint,text,text,text,text) TO migration_traffic_actuator;
  END IF;
END $$;

COMMIT;
