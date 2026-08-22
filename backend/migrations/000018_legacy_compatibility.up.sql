BEGIN;

CREATE TABLE legacy_compat_configs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    protocol text NOT NULL CHECK (protocol IN ('gmpay','epay')),
    pid text NOT NULL CHECK (length(pid) BETWEEN 1 AND 128),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    currency_scale smallint NOT NULL CHECK (currency_scale BETWEEN 0 AND 9),
    chain_id text NOT NULL REFERENCES chains(id),
    asset_id text NOT NULL REFERENCES assets(id),
    legacy_token text NOT NULL CHECK (legacy_token ~ '^[A-Z0-9][A-Z0-9._-]{0,63}$'),
    legacy_network text NOT NULL CHECK (legacy_network ~ '^[a-z0-9][a-z0-9._:-]{0,127}$'),
    legacy_epay_type text NOT NULL CHECK (length(legacy_epay_type) BETWEEN 1 AND 128),
    ip_allowlist cidr[] NOT NULL CHECK (cardinality(ip_allowlist) BETWEEN 1 AND 64),
    current_credential_version_id uuid,
    status text NOT NULL CHECK (status IN ('enabled','disabled')),
    sunset_at timestamptz NOT NULL,
    requested_by text NOT NULL CHECK (length(requested_by) BETWEEN 1 AND 255),
    approved_by text NOT NULL CHECK (length(approved_by) BETWEEN 1 AND 255 AND approved_by<>requested_by),
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256)=32),
    approved_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (id,tenant_id,merchant_id),
    UNIQUE (protocol,pid),
    CHECK (sunset_at>approved_at),
    CHECK (protocol='epay' OR legacy_epay_type='gmpay')
);

CREATE TABLE legacy_compat_credential_versions (
    id uuid PRIMARY KEY,
    config_id uuid NOT NULL REFERENCES legacy_compat_configs(id) ON DELETE RESTRICT,
    version bigint NOT NULL CHECK (version>0),
    legacy_secret_ref text NOT NULL CHECK (legacy_secret_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    callback_key_id text NOT NULL CHECK (length(callback_key_id) BETWEEN 1 AND 128),
    core_key_id text NOT NULL CHECK (length(core_key_id) BETWEEN 1 AND 128),
    core_secret_ref text NOT NULL CHECK (core_secret_ref ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    valid_from timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE(config_id,version),
    UNIQUE(id,config_id),
    CHECK(valid_until>valid_from)
);
ALTER TABLE legacy_compat_configs ADD CONSTRAINT legacy_config_current_credential_fk
  FOREIGN KEY(current_credential_version_id,id) REFERENCES legacy_compat_credential_versions(id,config_id) ON DELETE RESTRICT
  DEFERRABLE INITIALLY DEFERRED;

-- Admission is an identity-backed two-person control. SECURITY DEFINER
-- functions capture session_user; separately credentialed login roles inherit
-- exactly one of the two NOLOGIN capabilities in deploy bootstrap.
CREATE TABLE legacy_compat_admission_requests (
    id uuid PRIMARY KEY,
    config_id uuid NOT NULL UNIQUE,
    credential_id uuid NOT NULL UNIQUE,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    protocol text NOT NULL,
    pid text NOT NULL,
    currency text NOT NULL,
    currency_scale smallint NOT NULL,
    chain_id text NOT NULL,
    asset_id text NOT NULL,
    legacy_token text NOT NULL,
    legacy_network text NOT NULL,
    legacy_epay_type text NOT NULL,
    ip_allowlist cidr[] NOT NULL,
    legacy_secret_ref text NOT NULL,
    callback_key_id text NOT NULL,
    core_key_id text NOT NULL,
    core_secret_ref text NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    sunset_at timestamptz NOT NULL,
    manifest jsonb NOT NULL,
    manifest_sha256 bytea NOT NULL CHECK(octet_length(manifest_sha256)=32),
    requested_by text NOT NULL CHECK(length(requested_by) BETWEEN 1 AND 255),
    requested_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    approved_by text,
    approved_at timestamptz,
    status text NOT NULL CHECK(status IN ('pending','approved','expired')),
    CHECK((status='approved')=(approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK(expires_at=requested_at+interval '30 minutes'),
    CHECK(approved_by IS NULL OR approved_by<>requested_by)
);

CREATE TABLE legacy_compat_mappings (
    trade_id text PRIMARY KEY CHECK (trade_id ~ '^[A-Za-z0-9_-]{22}$'),
    config_id uuid NOT NULL REFERENCES legacy_compat_configs(id) ON DELETE RESTRICT,
    credential_version_id uuid NOT NULL,
    protocol text NOT NULL CHECK (protocol IN ('gmpay','epay')),
    legacy_order_id text NOT NULL CHECK (length(legacy_order_id) BETWEEN 1 AND 128),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    intent_id uuid NOT NULL,
    route_id uuid NOT NULL,
    notify_url text NOT NULL CHECK (notify_url ~ '^https://' AND length(notify_url)<=2048),
    return_url text NOT NULL CHECK (return_url='' OR return_url ~ '^https://' AND length(return_url)<=2048),
    order_name text NOT NULL CHECK (length(order_name)<=2048),
    epay_type text NOT NULL CHECK (length(epay_type)<=128),
    fiat_amount text NOT NULL CHECK (fiat_amount ~ '^[0-9]+([.][0-9]+)?$' AND length(fiat_amount)<=80),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    legacy_token text NOT NULL,
    legacy_network text NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY(credential_version_id,config_id) REFERENCES legacy_compat_credential_versions(id,config_id) ON DELETE RESTRICT,
    UNIQUE(config_id,legacy_order_id),
    UNIQUE(config_id,intent_id),
    UNIQUE(config_id,route_id)
);

CREATE TABLE legacy_compat_event_cursors (
    config_id uuid PRIMARY KEY REFERENCES legacy_compat_configs(id) ON DELETE RESTRICT,
    after_sequence bigint NOT NULL DEFAULT 0 CHECK(after_sequence>=0),
    updated_at timestamptz NOT NULL
);

CREATE TABLE legacy_compat_event_classifications (
    config_id uuid NOT NULL REFERENCES legacy_compat_configs(id) ON DELETE RESTRICT,
    event_sequence bigint NOT NULL CHECK(event_sequence>0),
    event_id uuid NOT NULL,
    classification text NOT NULL CHECK(classification IN ('invalid_payload','no_legacy_callback','unmapped_intent','state_mismatch')),
    classified_at timestamptz NOT NULL,
    PRIMARY KEY(config_id,event_sequence),
    UNIQUE(config_id,event_id)
);

CREATE TABLE legacy_compat_callback_deliveries (
    id uuid PRIMARY KEY,
    config_id uuid NOT NULL REFERENCES legacy_compat_configs(id) ON DELETE RESTRICT,
    event_sequence bigint NOT NULL CHECK(event_sequence>0),
    event_id uuid NOT NULL,
    trade_id text NOT NULL REFERENCES legacy_compat_mappings(trade_id) ON DELETE RESTRICT,
    credential_version_id uuid NOT NULL,
    callback_key_id text NOT NULL CHECK(length(callback_key_id) BETWEEN 1 AND 128),
    target_url text NOT NULL CHECK(target_url ~ '^https://' AND length(target_url)<=2048),
    http_method text NOT NULL CHECK(http_method IN ('GET','POST')),
    content_type text NOT NULL CHECK(content_type IN ('application/json','application/x-www-form-urlencoded')),
    frozen_body bytea NOT NULL CHECK(octet_length(frozen_body) BETWEEN 1 AND 65536),
    frozen_body_sha256 bytea NOT NULL CHECK(octet_length(frozen_body_sha256)=32 AND frozen_body_sha256=digest(frozen_body,'sha256')),
    status text NOT NULL CHECK(status IN ('pending','leased','retry','acknowledged','dead_letter')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK(attempt_count BETWEEN 0 AND 32),
    next_attempt_at timestamptz NOT NULL,
    lease_owner text,
    lease_token uuid,
    lease_until timestamptz,
    fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0),
    last_error_code text CHECK(last_error_code IS NULL OR length(last_error_code)<=64),
    acknowledged_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY(credential_version_id,config_id) REFERENCES legacy_compat_credential_versions(id,config_id) ON DELETE RESTRICT,
    UNIQUE(config_id,event_sequence),
    UNIQUE(config_id,event_id),
    CHECK((status='leased')=(lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK((status='acknowledged')=(acknowledged_at IS NOT NULL))
);
CREATE INDEX legacy_callback_claim_idx ON legacy_compat_callback_deliveries(next_attempt_at,id)
  WHERE status IN ('pending','retry','leased');

CREATE TABLE legacy_compat_callback_attempts (
    delivery_id uuid NOT NULL REFERENCES legacy_compat_callback_deliveries(id) ON DELETE RESTRICT,
    attempt_number integer NOT NULL CHECK(attempt_number>0),
    fence bigint NOT NULL CHECK(fence>0),
    outcome text NOT NULL CHECK(outcome IN ('acknowledged','failed')),
    http_status integer CHECK(http_status BETWEEN 100 AND 599),
    response_sha256 bytea CHECK(response_sha256 IS NULL OR octet_length(response_sha256)=32),
    error_code text CHECK(error_code IS NULL OR length(error_code)<=64),
    completed_at timestamptz NOT NULL,
    PRIMARY KEY(delivery_id,attempt_number)
);

CREATE FUNCTION legacy_compat_reject_mutation() RETURNS trigger LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$ BEGIN RAISE EXCEPTION 'legacy compatibility evidence is immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER legacy_credential_immutable BEFORE UPDATE OR DELETE ON legacy_compat_credential_versions FOR EACH ROW EXECUTE FUNCTION legacy_compat_reject_mutation();
CREATE TRIGGER legacy_mapping_immutable BEFORE UPDATE OR DELETE ON legacy_compat_mappings FOR EACH ROW EXECUTE FUNCTION legacy_compat_reject_mutation();
CREATE TRIGGER legacy_classification_immutable BEFORE UPDATE OR DELETE ON legacy_compat_event_classifications FOR EACH ROW EXECUTE FUNCTION legacy_compat_reject_mutation();
CREATE TRIGGER legacy_attempt_immutable BEFORE UPDATE OR DELETE ON legacy_compat_callback_attempts FOR EACH ROW EXECUTE FUNCTION legacy_compat_reject_mutation();

CREATE FUNCTION legacy_admission_request_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'legacy admission request evidence is immutable' USING ERRCODE='55000'; END IF;
 IF ROW(NEW.id,NEW.config_id,NEW.credential_id,NEW.tenant_id,NEW.merchant_id,NEW.protocol,NEW.pid,NEW.currency,
   NEW.currency_scale,NEW.chain_id,NEW.asset_id,NEW.legacy_token,NEW.legacy_network,NEW.legacy_epay_type,NEW.ip_allowlist,
   NEW.legacy_secret_ref,NEW.callback_key_id,NEW.core_key_id,NEW.core_secret_ref,NEW.valid_from,NEW.valid_until,NEW.sunset_at,
   NEW.manifest,NEW.manifest_sha256,NEW.requested_by,NEW.requested_at,NEW.expires_at)
   IS DISTINCT FROM
   ROW(OLD.id,OLD.config_id,OLD.credential_id,OLD.tenant_id,OLD.merchant_id,OLD.protocol,OLD.pid,OLD.currency,
   OLD.currency_scale,OLD.chain_id,OLD.asset_id,OLD.legacy_token,OLD.legacy_network,OLD.legacy_epay_type,OLD.ip_allowlist,
   OLD.legacy_secret_ref,OLD.callback_key_id,OLD.core_key_id,OLD.core_secret_ref,OLD.valid_from,OLD.valid_until,OLD.sunset_at,
   OLD.manifest,OLD.manifest_sha256,OLD.requested_by,OLD.requested_at,OLD.expires_at) OR
   OLD.status<>'pending' OR NEW.status NOT IN ('approved','expired') OR
   (NEW.status='expired' AND (NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL))
 THEN RAISE EXCEPTION 'legacy admission request evidence is immutable' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER legacy_admission_request_guard_trigger BEFORE UPDATE OR DELETE ON legacy_compat_admission_requests
 FOR EACH ROW EXECUTE FUNCTION legacy_admission_request_guard();

CREATE FUNCTION request_legacy_compat_config_admission(
  requested_request_id uuid,requested_config_id uuid,requested_credential_id uuid,requested_tenant uuid,requested_merchant uuid,
  requested_protocol text,requested_pid text,requested_currency text,requested_scale smallint,
  requested_chain text,requested_asset text,requested_token text,requested_network text,requested_epay_type text,
  requested_ip_allowlist cidr[],requested_legacy_secret_ref text,requested_callback_key_id text,
  requested_core_key_id text,requested_core_secret_ref text,requested_valid_from timestamptz,
  requested_valid_until timestamptz,requested_sunset timestamptz,requested_manifest jsonb,
  requested_manifest_sha256 bytea
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE canonical_manifest jsonb; authoritative_at timestamptz:=clock_timestamp();
BEGIN
  canonical_manifest:=jsonb_build_object('asset_id',requested_asset,'chain_id',requested_chain,
    'config_id',requested_config_id,'core_key_id',requested_core_key_id,'core_secret_ref',requested_core_secret_ref,
    'credential_id',requested_credential_id,'currency',requested_currency,'currency_scale',requested_scale,
    'ip_allowlist',to_jsonb(requested_ip_allowlist),'legacy_epay_type',requested_epay_type,
    'legacy_network',requested_network,'legacy_secret_ref',requested_legacy_secret_ref,'legacy_token',requested_token,
    'merchant_id',requested_merchant,'pid',requested_pid,'protocol',requested_protocol,'sunset_at',requested_sunset,
    'tenant_id',requested_tenant,'valid_from',requested_valid_from,'valid_until',requested_valid_until);
  IF NOT pg_has_role(session_user,'legacy_compat_admission_requester','member') OR
     requested_manifest<>canonical_manifest OR digest(convert_to(requested_manifest::text,'UTF8'),'sha256')<>requested_manifest_sha256 OR
     requested_protocol NOT IN ('gmpay','epay') OR cardinality(requested_ip_allowlist) NOT BETWEEN 1 AND 64 OR
	 requested_pid<>btrim(requested_pid) OR requested_currency<>upper(requested_currency) OR
	 requested_token<>upper(requested_token) OR requested_network<>lower(requested_network) OR
	 (requested_protocol='gmpay' AND requested_epay_type<>'gmpay') OR
     requested_sunset<=authoritative_at OR requested_valid_from>authoritative_at OR requested_valid_until<=authoritative_at OR requested_valid_until>requested_sunset THEN RETURN false; END IF;
  INSERT INTO public.legacy_compat_admission_requests(id,config_id,credential_id,tenant_id,merchant_id,protocol,pid,currency,
    currency_scale,chain_id,asset_id,legacy_token,legacy_network,legacy_epay_type,ip_allowlist,legacy_secret_ref,callback_key_id,
    core_key_id,core_secret_ref,valid_from,valid_until,sunset_at,manifest,manifest_sha256,requested_by,requested_at,expires_at,status)
  VALUES(requested_request_id,requested_config_id,requested_credential_id,requested_tenant,requested_merchant,requested_protocol,
    requested_pid,requested_currency,requested_scale,requested_chain,requested_asset,requested_token,
    requested_network,requested_epay_type,requested_ip_allowlist,requested_legacy_secret_ref,requested_callback_key_id,
    requested_core_key_id,requested_core_secret_ref,requested_valid_from,requested_valid_until,requested_sunset,requested_manifest,
    requested_manifest_sha256,session_user,authoritative_at,authoritative_at+interval '30 minutes','pending');
  RETURN true;
END $$;

-- The executable importer accepts one immutable JSON manifest. It derives all
-- economic fields and the PostgreSQL-canonical digest inside the trusted
-- boundary, so the requester cannot sign one document while inserting another.
CREATE FUNCTION request_legacy_compat_config_admission(requested_request_id uuid,requested_manifest jsonb)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
	IF jsonb_typeof(requested_manifest)<>'object' THEN RETURN false; END IF;
	RETURN public.request_legacy_compat_config_admission(
		requested_request_id,(requested_manifest->>'config_id')::uuid,(requested_manifest->>'credential_id')::uuid,
		(requested_manifest->>'tenant_id')::uuid,(requested_manifest->>'merchant_id')::uuid,
		requested_manifest->>'protocol',requested_manifest->>'pid',requested_manifest->>'currency',
		(requested_manifest->>'currency_scale')::smallint,requested_manifest->>'chain_id',requested_manifest->>'asset_id',
		requested_manifest->>'legacy_token',requested_manifest->>'legacy_network',requested_manifest->>'legacy_epay_type',
		ARRAY(SELECT value::cidr FROM jsonb_array_elements_text(requested_manifest->'ip_allowlist') AS value),
		requested_manifest->>'legacy_secret_ref',requested_manifest->>'callback_key_id',requested_manifest->>'core_key_id',
		requested_manifest->>'core_secret_ref',(requested_manifest->>'valid_from')::timestamptz,
		(requested_manifest->>'valid_until')::timestamptz,(requested_manifest->>'sunset_at')::timestamptz,
		requested_manifest,digest(convert_to(requested_manifest::text,'UTF8'),'sha256'));
EXCEPTION WHEN data_exception OR invalid_text_representation THEN RETURN false;
END $$;

CREATE FUNCTION approve_legacy_compat_config_admission(requested_request_id uuid,expected_manifest_sha256 bytea)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE request_row public.legacy_compat_admission_requests%ROWTYPE; authoritative_at timestamptz:=clock_timestamp();
BEGIN
  IF NOT pg_has_role(session_user,'legacy_compat_admission_approver','member') THEN RETURN false; END IF;
  SELECT * INTO request_row FROM public.legacy_compat_admission_requests WHERE id=requested_request_id FOR UPDATE;
  IF NOT FOUND OR request_row.status<>'pending' OR request_row.requested_by=session_user OR
     request_row.manifest_sha256<>expected_manifest_sha256 OR
     digest(convert_to(request_row.manifest::text,'UTF8'),'sha256')<>expected_manifest_sha256
  THEN RETURN false; END IF;
  IF authoritative_at>request_row.expires_at THEN
    UPDATE public.legacy_compat_admission_requests SET status='expired' WHERE id=requested_request_id;
    RETURN false;
  END IF;
  IF authoritative_at NOT BETWEEN request_row.valid_from AND request_row.valid_until OR authoritative_at>=request_row.sunset_at OR
     NOT EXISTS(SELECT 1 FROM public.tenants t JOIN public.merchants m ON m.tenant_id=t.id
       WHERE t.id=request_row.tenant_id AND m.id=request_row.merchant_id AND t.status='active' AND m.status='active') OR
     NOT EXISTS(SELECT 1 FROM public.assets a JOIN public.chains c ON c.id=a.chain_id
       WHERE a.id=request_row.asset_id AND a.chain_id=request_row.chain_id AND a.status='active' AND c.status='active') OR
     NOT EXISTS(SELECT 1 FROM public.api_clients k WHERE k.key_id=request_row.core_key_id AND k.tenant_id=request_row.tenant_id
       AND k.merchant_id=request_row.merchant_id AND k.algorithm='hmac-sha256' AND k.revoked_at IS NULL
       AND authoritative_at>=k.valid_from AND (k.valid_until IS NULL OR authoritative_at<k.valid_until)
	   AND k.scopes @> ARRAY['payments:read','payments:write','events:read']::text[]
	   AND ARRAY['payments:read','payments:write','events:read']::text[] @> k.scopes)
  THEN RETURN false; END IF;
  INSERT INTO public.legacy_compat_configs(id,tenant_id,merchant_id,protocol,pid,currency,currency_scale,chain_id,asset_id,
    legacy_token,legacy_network,legacy_epay_type,ip_allowlist,current_credential_version_id,status,sunset_at,
    requested_by,approved_by,manifest_sha256,approved_at,created_at,updated_at)
  VALUES(request_row.config_id,request_row.tenant_id,request_row.merchant_id,request_row.protocol,request_row.pid,request_row.currency,
    request_row.currency_scale,request_row.chain_id,request_row.asset_id,request_row.legacy_token,request_row.legacy_network,
    request_row.legacy_epay_type,request_row.ip_allowlist,request_row.credential_id,'enabled',request_row.sunset_at,
    request_row.requested_by,session_user,request_row.manifest_sha256,authoritative_at,authoritative_at,authoritative_at);
  INSERT INTO public.legacy_compat_credential_versions(id,config_id,version,legacy_secret_ref,callback_key_id,core_key_id,
    core_secret_ref,valid_from,valid_until,created_at)
  VALUES(request_row.credential_id,request_row.config_id,1,request_row.legacy_secret_ref,request_row.callback_key_id,
    request_row.core_key_id,request_row.core_secret_ref,request_row.valid_from,request_row.valid_until,authoritative_at);
  INSERT INTO public.legacy_compat_event_cursors(config_id,after_sequence,updated_at) VALUES(request_row.config_id,0,authoritative_at);
  UPDATE public.legacy_compat_admission_requests SET status='approved',approved_by=session_user,approved_at=authoritative_at
    WHERE id=requested_request_id;
  RETURN true;
END $$;

-- The second identity must independently present the same complete manifest;
-- approval by request id alone is intentionally impossible.
CREATE FUNCTION approve_legacy_compat_config_admission(requested_request_id uuid,approved_manifest jsonb)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
	IF jsonb_typeof(approved_manifest)<>'object' THEN RETURN false; END IF;
	RETURN public.approve_legacy_compat_config_admission(requested_request_id,digest(convert_to(approved_manifest::text,'UTF8'),'sha256'));
END $$;

CREATE FUNCTION legacy_lookup_credential(requested_protocol text,requested_pid text,checked_at timestamptz)
RETURNS TABLE(config_id uuid,credential_version_id uuid,credential_version bigint,protocol text,pid text,tenant_id uuid,merchant_id uuid,
 legacy_secret_ref text,callback_key_id text,core_key_id text,core_secret_ref text,currency text,currency_scale smallint,
 chain_id text,asset_id text,legacy_token text,legacy_network text,legacy_epay_type text,ip_allowlist cidr[],approved boolean,enabled boolean,sunset_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT c.id,v.id,v.version,c.protocol,c.pid,c.tenant_id,c.merchant_id,v.legacy_secret_ref,v.callback_key_id,v.core_key_id,v.core_secret_ref,
   c.currency::text,c.currency_scale,c.chain_id,c.asset_id,c.legacy_token,c.legacy_network,c.legacy_epay_type,c.ip_allowlist,true,
   c.status='enabled' AND checked_at<c.sunset_at AND checked_at BETWEEN v.valid_from AND v.valid_until,c.sunset_at
 FROM public.legacy_compat_configs c JOIN public.legacy_compat_credential_versions v ON v.id=c.current_credential_version_id AND v.config_id=c.id
 WHERE c.protocol=requested_protocol AND c.pid=requested_pid
$$;

CREATE FUNCTION legacy_lookup_credential_version(requested_id uuid)
RETURNS TABLE(config_id uuid,credential_version_id uuid,credential_version bigint,protocol text,pid text,tenant_id uuid,merchant_id uuid,
 legacy_secret_ref text,callback_key_id text,core_key_id text,core_secret_ref text,currency text,currency_scale smallint,
 chain_id text,asset_id text,legacy_token text,legacy_network text,legacy_epay_type text,ip_allowlist cidr[],approved boolean,enabled boolean,sunset_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT c.id,v.id,v.version,c.protocol,c.pid,c.tenant_id,c.merchant_id,v.legacy_secret_ref,v.callback_key_id,v.core_key_id,v.core_secret_ref,
   c.currency::text,c.currency_scale,c.chain_id,c.asset_id,c.legacy_token,c.legacy_network,c.legacy_epay_type,c.ip_allowlist,true,c.status='enabled',c.sunset_at
 FROM public.legacy_compat_credential_versions v JOIN public.legacy_compat_configs c ON c.id=v.config_id WHERE v.id=requested_id
$$;

CREATE FUNCTION legacy_record_mapping(requested_trade text,requested_config uuid,requested_credential uuid,requested_protocol text,
 requested_order text,requested_hash bytea,requested_intent uuid,requested_route uuid,requested_notify text,requested_return text,
 requested_name text,requested_epay_type text,requested_amount text,requested_currency text,requested_token text,requested_network text,requested_at timestamptz)
RETURNS SETOF legacy_compat_mappings LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  INSERT INTO public.legacy_compat_mappings(trade_id,config_id,credential_version_id,protocol,legacy_order_id,request_hash,intent_id,route_id,
    notify_url,return_url,order_name,epay_type,fiat_amount,currency,legacy_token,legacy_network,created_at)
  VALUES(requested_trade,requested_config,requested_credential,requested_protocol,requested_order,requested_hash,requested_intent,requested_route,
    requested_notify,requested_return,requested_name,requested_epay_type,requested_amount,requested_currency,requested_token,requested_network,requested_at)
  ON CONFLICT(config_id,legacy_order_id) DO NOTHING;
  RETURN QUERY SELECT * FROM public.legacy_compat_mappings WHERE config_id=requested_config AND legacy_order_id=requested_order;
END $$;

CREATE FUNCTION legacy_lookup_mapping(requested_trade text) RETURNS SETOF legacy_compat_mappings
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$ SELECT * FROM public.legacy_compat_mappings WHERE trade_id=requested_trade $$;
CREATE FUNCTION legacy_lookup_mapping_by_intent(requested_config uuid,requested_intent uuid) RETURNS SETOF legacy_compat_mappings
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$ SELECT * FROM public.legacy_compat_mappings WHERE config_id=requested_config AND intent_id=requested_intent $$;

CREATE FUNCTION legacy_list_event_sources(checked_at timestamptz)
RETURNS TABLE(config_id uuid,protocol text,pid text,core_key_id text,core_secret_ref text,after_sequence bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
 SELECT c.id,c.protocol,c.pid,v.core_key_id,v.core_secret_ref,e.after_sequence FROM public.legacy_compat_configs c
 JOIN public.legacy_compat_credential_versions v ON v.id=c.current_credential_version_id
 JOIN public.legacy_compat_event_cursors e ON e.config_id=c.id
 WHERE c.status='enabled' AND checked_at<c.sunset_at AND checked_at BETWEEN v.valid_from AND v.valid_until ORDER BY c.id
$$;

CREATE FUNCTION legacy_classify_event(requested_config uuid,requested_sequence bigint,requested_event uuid,requested_classification text,requested_at timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE current_sequence bigint; authoritative_at timestamptz:=clock_timestamp();
BEGIN
 SELECT after_sequence INTO current_sequence FROM public.legacy_compat_event_cursors WHERE config_id=requested_config FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 IF requested_sequence<=current_sequence THEN
   RETURN EXISTS(SELECT 1 FROM public.legacy_compat_event_classifications WHERE config_id=requested_config
     AND event_sequence=requested_sequence AND event_id=requested_event AND classification=requested_classification);
 END IF;
 IF requested_sequence<>current_sequence+1 THEN RETURN false; END IF;
 INSERT INTO public.legacy_compat_event_classifications(config_id,event_sequence,event_id,classification,classified_at)
 VALUES(requested_config,requested_sequence,requested_event,requested_classification,authoritative_at) ON CONFLICT DO NOTHING;
 IF NOT FOUND THEN RETURN false; END IF;
 UPDATE public.legacy_compat_event_cursors SET after_sequence=requested_sequence,updated_at=authoritative_at WHERE config_id=requested_config;
 RETURN true;
END $$;

CREATE FUNCTION legacy_enqueue_callback(requested_id uuid,requested_config uuid,requested_sequence bigint,requested_event uuid,requested_trade text,
 requested_credential uuid,requested_key_id text,requested_target text,requested_method text,requested_content_type text,requested_body bytea,requested_at timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE current_sequence bigint; authoritative_at timestamptz:=clock_timestamp();
BEGIN
 SELECT after_sequence INTO current_sequence FROM public.legacy_compat_event_cursors WHERE config_id=requested_config FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
	IF NOT EXISTS(SELECT 1 FROM public.legacy_compat_mappings m
		JOIN public.legacy_compat_credential_versions v ON v.id=m.credential_version_id AND v.config_id=m.config_id
		JOIN public.legacy_compat_configs c ON c.id=m.config_id
		WHERE m.trade_id=requested_trade AND m.config_id=requested_config AND m.credential_version_id=requested_credential
		AND m.notify_url=requested_target AND v.callback_key_id=requested_key_id
		AND ((c.protocol='gmpay' AND requested_method='POST' AND requested_content_type='application/json') OR
		     (c.protocol='epay' AND requested_method='GET' AND requested_content_type='application/x-www-form-urlencoded')))
	THEN RETURN false; END IF;
 IF requested_sequence<=current_sequence THEN
   RETURN EXISTS(SELECT 1 FROM public.legacy_compat_callback_deliveries WHERE config_id=requested_config
     AND event_sequence=requested_sequence AND event_id=requested_event AND trade_id=requested_trade
     AND credential_version_id=requested_credential AND callback_key_id=requested_key_id AND target_url=requested_target
     AND http_method=requested_method AND content_type=requested_content_type AND frozen_body=requested_body);
 END IF;
 IF requested_sequence<>current_sequence+1 THEN RETURN false; END IF;
 INSERT INTO public.legacy_compat_callback_deliveries(id,config_id,event_sequence,event_id,trade_id,credential_version_id,callback_key_id,
   target_url,http_method,content_type,frozen_body,frozen_body_sha256,status,next_attempt_at,created_at,updated_at)
 VALUES(requested_id,requested_config,requested_sequence,requested_event,requested_trade,requested_credential,requested_key_id,
   requested_target,requested_method,requested_content_type,requested_body,digest(requested_body,'sha256'),'pending',authoritative_at,authoritative_at,authoritative_at)
 ON CONFLICT(config_id,event_id) DO NOTHING;
 IF NOT FOUND THEN RETURN false; END IF;
 UPDATE public.legacy_compat_event_cursors SET after_sequence=requested_sequence,updated_at=authoritative_at WHERE config_id=requested_config;
 RETURN true;
END $$;

CREATE FUNCTION legacy_claim_callbacks(requested_worker text,requested_limit integer,requested_lease_seconds integer,requested_at timestamptz)
RETURNS TABLE(delivery_id uuid,lease_token uuid,fence bigint,protocol text,event_id uuid,target_url text,http_method text,content_type text,
 frozen_body bytea,credential_version_id uuid,callback_key_id text,attempt_count integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE authoritative_at timestamptz:=clock_timestamp();
BEGIN
	IF length(requested_worker) NOT BETWEEN 1 AND 128 OR requested_limit NOT BETWEEN 1 AND 100 OR
	   requested_lease_seconds NOT BETWEEN 5 AND 300 THEN RETURN; END IF;
	UPDATE public.legacy_compat_callback_deliveries SET status='dead_letter',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
		last_error_code='lease_expired_attempt_limit',updated_at=authoritative_at
		WHERE status='leased' AND lease_until<=authoritative_at AND attempt_count>=32;
 RETURN QUERY WITH candidates AS (
   SELECT d.id FROM public.legacy_compat_callback_deliveries d WHERE
    (d.status IN ('pending','retry') AND d.next_attempt_at<=authoritative_at OR d.status='leased' AND d.lease_until<=authoritative_at)
    AND d.attempt_count<32 ORDER BY d.next_attempt_at,d.id FOR UPDATE SKIP LOCKED LIMIT requested_limit
 ), updated AS (
   UPDATE public.legacy_compat_callback_deliveries d SET status='leased',lease_owner=requested_worker,lease_token=gen_random_uuid(),
    lease_until=authoritative_at+make_interval(secs=>requested_lease_seconds),fence=d.fence+1,attempt_count=d.attempt_count+1,updated_at=authoritative_at
   FROM candidates c WHERE d.id=c.id RETURNING d.*
 ) SELECT u.id,u.lease_token,u.fence,c.protocol,u.event_id,u.target_url,u.http_method,u.content_type,u.frozen_body,
   u.credential_version_id,u.callback_key_id,u.attempt_count FROM updated u JOIN public.legacy_compat_configs c ON c.id=u.config_id;
END $$;

CREATE FUNCTION legacy_ack_callback(requested_id uuid,requested_token uuid,requested_fence bigint,requested_status integer,requested_digest bytea,requested_at timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE attempts integer; authoritative_at timestamptz:=clock_timestamp();
BEGIN
	IF requested_status<>200 OR requested_digest IS NULL OR octet_length(requested_digest)<>32 THEN RETURN false; END IF;
 UPDATE public.legacy_compat_callback_deliveries SET status='acknowledged',lease_owner=NULL,lease_token=NULL,lease_until=NULL,
	acknowledged_at=authoritative_at,updated_at=authoritative_at WHERE id=requested_id AND status='leased' AND
	lease_token=requested_token AND fence=requested_fence AND lease_until>authoritative_at
 RETURNING attempt_count INTO attempts;
 IF NOT FOUND THEN RETURN false; END IF;
 INSERT INTO public.legacy_compat_callback_attempts(delivery_id,attempt_number,fence,outcome,http_status,response_sha256,completed_at)
 VALUES(requested_id,attempts,requested_fence,'acknowledged',requested_status,requested_digest,authoritative_at);
 RETURN true;
END $$;

CREATE FUNCTION legacy_fail_callback(requested_id uuid,requested_token uuid,requested_fence bigint,requested_error text,requested_status integer,requested_next timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE attempts integer; new_status text; authoritative_at timestamptz:=clock_timestamp();
BEGIN
	IF requested_error IS NULL OR requested_error<>'delivery_failed' OR
	   (requested_status<>0 AND requested_status NOT BETWEEN 100 AND 599) OR requested_next IS NULL OR
	   requested_next<authoritative_at OR requested_next>authoritative_at+interval '5 minutes' THEN RETURN false; END IF;
 SELECT CASE WHEN attempt_count>=32 THEN 'dead_letter' ELSE 'retry' END,attempt_count INTO new_status,attempts
 FROM public.legacy_compat_callback_deliveries WHERE id=requested_id AND status='leased' AND lease_token=requested_token AND
	 fence=requested_fence AND lease_until>authoritative_at FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 UPDATE public.legacy_compat_callback_deliveries SET status=new_status,lease_owner=NULL,lease_token=NULL,lease_until=NULL,
	last_error_code=requested_error,next_attempt_at=greatest(requested_next,authoritative_at),updated_at=authoritative_at WHERE id=requested_id;
 INSERT INTO public.legacy_compat_callback_attempts(delivery_id,attempt_number,fence,outcome,http_status,error_code,completed_at)
 VALUES(requested_id,attempts,requested_fence,'failed',nullif(requested_status,0),requested_error,authoritative_at);
 RETURN true;
END $$;

CREATE FUNCTION legacy_compat_ready(checked_at timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE
	authoritative_at timestamptz:=clock_timestamp();
	required_function text;
	table_name text;
	privilege_name text;
BEGIN
	IF NOT pg_has_role(session_user,'legacy_compat_runtime','member') OR
	   NOT EXISTS(SELECT 1 FROM public.schema_migrations WHERE filename='000018_legacy_compatibility.up.sql') OR
	   NOT EXISTS(SELECT 1 FROM public.legacy_compat_configs c JOIN public.legacy_compat_credential_versions v ON v.id=c.current_credential_version_id
	     WHERE c.status='enabled' AND authoritative_at<c.sunset_at AND authoritative_at BETWEEN v.valid_from AND v.valid_until) OR
	   EXISTS(SELECT 1 FROM public.legacy_compat_configs c JOIN public.legacy_compat_credential_versions v ON v.id=c.current_credential_version_id
	     WHERE c.status='enabled' AND (authoritative_at>=c.sunset_at OR authoritative_at NOT BETWEEN v.valid_from AND v.valid_until)) OR
	   EXISTS(SELECT 1 FROM public.legacy_compat_callback_deliveries WHERE status='leased' AND lease_until<authoritative_at-interval '5 minutes')
	THEN RETURN false; END IF;
	FOREACH required_function IN ARRAY ARRAY[
		'public.legacy_lookup_credential(text,text,timestamptz)','public.legacy_lookup_credential_version(uuid)',
		'public.legacy_record_mapping(text,uuid,uuid,text,text,bytea,uuid,uuid,text,text,text,text,text,text,text,text,timestamptz)',
		'public.legacy_lookup_mapping(text)','public.legacy_lookup_mapping_by_intent(uuid,uuid)',
		'public.legacy_list_event_sources(timestamptz)','public.legacy_classify_event(uuid,bigint,uuid,text,timestamptz)',
		'public.legacy_enqueue_callback(uuid,uuid,bigint,uuid,text,uuid,text,text,text,text,bytea,timestamptz)',
		'public.legacy_claim_callbacks(text,integer,integer,timestamptz)',
		'public.legacy_ack_callback(uuid,uuid,bigint,integer,bytea,timestamptz)',
		'public.legacy_fail_callback(uuid,uuid,bigint,text,integer,timestamptz)','public.legacy_compat_ready(timestamptz)'
	] LOOP
		IF NOT coalesce(has_function_privilege(session_user,to_regprocedure(required_function),'EXECUTE'),false) THEN RETURN false; END IF;
	END LOOP;
	IF has_function_privilege(session_user,'public.request_legacy_compat_config_admission(uuid,uuid,uuid,uuid,uuid,text,text,text,smallint,text,text,text,text,text,cidr[],text,text,text,text,timestamptz,timestamptz,timestamptz,jsonb,bytea)','EXECUTE') OR
	   has_function_privilege(session_user,'public.request_legacy_compat_config_admission(uuid,jsonb)','EXECUTE') OR
	   has_function_privilege(session_user,'public.approve_legacy_compat_config_admission(uuid,bytea)','EXECUTE') OR
	   has_function_privilege(session_user,'public.approve_legacy_compat_config_admission(uuid,jsonb)','EXECUTE')
	THEN RETURN false; END IF;
	FOREACH table_name IN ARRAY ARRAY['legacy_compat_configs','legacy_compat_credential_versions','legacy_compat_admission_requests',
		'legacy_compat_mappings','legacy_compat_event_cursors','legacy_compat_event_classifications',
		'legacy_compat_callback_deliveries','legacy_compat_callback_attempts'] LOOP
		FOREACH privilege_name IN ARRAY ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER'] LOOP
			IF has_table_privilege(session_user,'public.'||table_name,privilege_name) THEN RETURN false; END IF;
		END LOOP;
	END LOOP;
	RETURN true;
END
$$;

REVOKE ALL ON legacy_compat_configs,legacy_compat_credential_versions,legacy_compat_admission_requests,
 legacy_compat_mappings,legacy_compat_event_cursors,legacy_compat_event_classifications,
 legacy_compat_callback_deliveries,legacy_compat_callback_attempts FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION request_legacy_compat_config_admission(uuid,uuid,uuid,uuid,uuid,text,text,text,smallint,text,text,text,text,text,cidr[],text,text,text,text,timestamptz,timestamptz,timestamptz,jsonb,bytea),
 request_legacy_compat_config_admission(uuid,jsonb),approve_legacy_compat_config_admission(uuid,bytea),
 approve_legacy_compat_config_admission(uuid,jsonb),legacy_compat_reject_mutation(),legacy_admission_request_guard() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION legacy_lookup_credential(text,text,timestamptz),legacy_lookup_credential_version(uuid),
 legacy_record_mapping(text,uuid,uuid,text,text,bytea,uuid,uuid,text,text,text,text,text,text,text,text,timestamptz),
 legacy_lookup_mapping(text),legacy_lookup_mapping_by_intent(uuid,uuid),legacy_list_event_sources(timestamptz),
 legacy_classify_event(uuid,bigint,uuid,text,timestamptz),legacy_enqueue_callback(uuid,uuid,bigint,uuid,text,uuid,text,text,text,text,bytea,timestamptz),
 legacy_claim_callbacks(text,integer,integer,timestamptz),legacy_ack_callback(uuid,uuid,bigint,integer,bytea,timestamptz),
 legacy_fail_callback(uuid,uuid,bigint,text,integer,timestamptz),legacy_compat_ready(timestamptz) FROM PUBLIC;

DO $grants$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='legacy_compat_runtime') THEN
   GRANT EXECUTE ON FUNCTION legacy_lookup_credential(text,text,timestamptz),legacy_lookup_credential_version(uuid),
    legacy_record_mapping(text,uuid,uuid,text,text,bytea,uuid,uuid,text,text,text,text,text,text,text,text,timestamptz),
    legacy_lookup_mapping(text),legacy_lookup_mapping_by_intent(uuid,uuid),legacy_list_event_sources(timestamptz),
    legacy_classify_event(uuid,bigint,uuid,text,timestamptz),legacy_enqueue_callback(uuid,uuid,bigint,uuid,text,uuid,text,text,text,text,bytea,timestamptz),
    legacy_claim_callbacks(text,integer,integer,timestamptz),legacy_ack_callback(uuid,uuid,bigint,integer,bytea,timestamptz),
    legacy_fail_callback(uuid,uuid,bigint,text,integer,timestamptz),legacy_compat_ready(timestamptz) TO legacy_compat_runtime;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='legacy_compat_admission_requester') THEN
   GRANT EXECUTE ON FUNCTION request_legacy_compat_config_admission(uuid,jsonb) TO legacy_compat_admission_requester;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='legacy_compat_admission_approver') THEN
   GRANT EXECUTE ON FUNCTION approve_legacy_compat_config_admission(uuid,jsonb) TO legacy_compat_admission_approver;
 END IF;
END $grants$;

COMMIT;
