BEGIN;

ALTER TABLE payment_link_redemptions
  ADD CONSTRAINT payment_link_redemptions_hosted_job_unique UNIQUE(id,payment_link_id,tenant_id,merchant_id,intent_id);
ALTER TABLE hosted_provider_create_attempts
  ADD CONSTRAINT hosted_provider_create_attempts_job_unique UNIQUE(id,tenant_id,merchant_id,provider_id,intent_id);

CREATE TABLE hosted_payment_link_jobs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    payment_link_id uuid NOT NULL,
    redemption_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    create_attempt_id uuid NOT NULL,
    provider_id text NOT NULL,
    asset_id text NOT NULL,
    provider_idempotency_key text NOT NULL CHECK (length(provider_idempotency_key) BETWEEN 8 AND 255),
    provider_request_hash bytea NOT NULL CHECK (octet_length(provider_request_hash)=32),
    state text NOT NULL CHECK (state IN ('preparing','bound','terminal')),
    route_id uuid,
    last_error_code text CHECK (last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 64),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (redemption_id,payment_link_id,tenant_id,merchant_id,intent_id) REFERENCES payment_link_redemptions(id,payment_link_id,tenant_id,merchant_id,intent_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (payment_link_id,tenant_id) REFERENCES payment_links(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (create_attempt_id,tenant_id,merchant_id,provider_id,intent_id) REFERENCES hosted_provider_create_attempts(id,tenant_id,merchant_id,provider_id,intent_id) ON DELETE RESTRICT,
    FOREIGN KEY (provider_id,tenant_id,merchant_id) REFERENCES hosted_provider_configs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    FOREIGN KEY (route_id,intent_id,tenant_id) REFERENCES payment_routes(id,intent_id,tenant_id) ON DELETE RESTRICT,
    UNIQUE (redemption_id),
    UNIQUE (create_attempt_id),
    UNIQUE (merchant_id,provider_idempotency_key),
    UNIQUE (id,tenant_id,merchant_id),
    CHECK (
      (state='preparing' AND route_id IS NULL AND last_error_code IS NULL)
      OR (state='bound' AND route_id IS NOT NULL AND last_error_code IS NULL)
      OR (state='terminal' AND route_id IS NULL AND last_error_code IS NOT NULL)
    ),
    CHECK (expires_at>created_at)
);
CREATE INDEX hosted_payment_link_jobs_intent_idx ON hosted_payment_link_jobs(tenant_id,merchant_id,intent_id);

CREATE TABLE hosted_payment_link_incidents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    job_id uuid NOT NULL,
    incident_kind text NOT NULL CHECK (incident_kind IN ('expired_before_provider_create','provider_create_exhausted','route_bind_exhausted')),
    status text NOT NULL CHECK (status IN ('open','resolved')),
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (job_id,tenant_id,merchant_id) REFERENCES hosted_payment_link_jobs(id,tenant_id,merchant_id) ON DELETE RESTRICT,
    UNIQUE (job_id,incident_kind),
    CHECK ((status='resolved')=(resolved_at IS NOT NULL))
);

ALTER TABLE hosted_payment_link_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_payment_link_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosted_payment_link_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE hosted_payment_link_incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY hosted_payment_link_jobs_scope ON hosted_payment_link_jobs
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);
CREATE POLICY hosted_payment_link_incidents_scope ON hosted_payment_link_incidents
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid
    AND merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid);

CREATE FUNCTION hosted_payment_link_job_reject_economic_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['state','route_id','last_error_code','updated_at','version'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['state','route_id','last_error_code','updated_at','version']) THEN
    RAISE EXCEPTION 'hosted payment-link job identity and economics are immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state<>NEW.state AND NOT (OLD.state='preparing' AND NEW.state IN ('bound','terminal')) THEN
    RAISE EXCEPTION 'invalid hosted payment-link job state transition' USING ERRCODE='55000';
  END IF;
  IF OLD.state IN ('bound','terminal') AND
     (NEW.state,NEW.route_id,NEW.last_error_code) IS DISTINCT FROM
     (OLD.state,OLD.route_id,OLD.last_error_code) THEN
    RAISE EXCEPTION 'hosted payment-link job terminal state is immutable' USING ERRCODE='55000';
  END IF;
  IF NOT (
    (NEW.state='preparing' AND NEW.route_id IS NULL AND NEW.last_error_code IS NULL)
    OR (NEW.state='bound' AND NEW.route_id IS NOT NULL AND NEW.last_error_code IS NULL)
    OR (NEW.state='terminal' AND NEW.route_id IS NULL AND NEW.last_error_code IS NOT NULL
        AND length(NEW.last_error_code) BETWEEN 1 AND 64)
  ) THEN
    RAISE EXCEPTION 'invalid hosted payment-link job state facts' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER hosted_payment_link_job_economic_immutable BEFORE UPDATE ON hosted_payment_link_jobs
  FOR EACH ROW EXECUTE FUNCTION hosted_payment_link_job_reject_economic_mutation();

CREATE FUNCTION hosted_provider_create_attempt_reject_request_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF (to_jsonb(NEW)-ARRAY['state','claim_token','claim_until','provider_order_id','provider_reference','payment_url',
      'amount_atomic','asset_decimals','quote_id','rate_numerator','rate_denominator','quote_issued_at',
      'create_response_body','create_response_digest','create_response_received_at','last_error_code','recovery_status',
      'recovery_claim_token','recovery_claim_until','recovery_attempt_count','next_recovery_at','last_recovery_error_code','updated_at','version'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['state','claim_token','claim_until','provider_order_id','provider_reference','payment_url',
      'amount_atomic','asset_decimals','quote_id','rate_numerator','rate_denominator','quote_issued_at',
      'create_response_body','create_response_digest','create_response_received_at','last_error_code','recovery_status',
      'recovery_claim_token','recovery_claim_until','recovery_attempt_count','next_recovery_at','last_recovery_error_code','updated_at','version']) THEN
    RAISE EXCEPTION 'hosted provider create request facts are immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state='completed' AND (to_jsonb(NEW)-ARRAY['recovery_status','recovery_claim_token','recovery_claim_until',
      'recovery_attempt_count','next_recovery_at','last_recovery_error_code','updated_at','version'])
     IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['recovery_status','recovery_claim_token','recovery_claim_until',
      'recovery_attempt_count','next_recovery_at','last_recovery_error_code','updated_at','version']) THEN
    RAISE EXCEPTION 'hosted provider completed create evidence is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state<>'completed' AND NEW.state<>'completed' AND
     (NEW.provider_order_id,NEW.provider_reference,NEW.payment_url,NEW.amount_atomic,NEW.asset_decimals,
      NEW.quote_id,NEW.rate_numerator,NEW.rate_denominator,NEW.quote_issued_at,NEW.create_response_body,
      NEW.create_response_digest,NEW.create_response_received_at) IS DISTINCT FROM
     (OLD.provider_order_id,OLD.provider_reference,OLD.payment_url,OLD.amount_atomic,OLD.asset_decimals,
      OLD.quote_id,OLD.rate_numerator,OLD.rate_denominator,OLD.quote_issued_at,OLD.create_response_body,
      OLD.create_response_digest,OLD.create_response_received_at) THEN
    RAISE EXCEPTION 'hosted provider create evidence requires completed transition' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER hosted_provider_create_attempt_request_immutable BEFORE UPDATE ON hosted_provider_create_attempts
  FOR EACH ROW EXECUTE FUNCTION hosted_provider_create_attempt_reject_request_mutation();

CREATE FUNCTION bind_hosted_payment_link_job() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF NEW.provider='hosted_gateway' THEN
    UPDATE public.hosted_payment_link_jobs j
       SET state='bound',route_id=NEW.id,last_error_code=NULL,updated_at=clock_timestamp(),version=j.version+1
     WHERE j.intent_id=NEW.intent_id AND j.tenant_id=NEW.tenant_id AND j.merchant_id=NEW.merchant_id
       AND j.state='preparing' AND EXISTS (
         SELECT 1 FROM public.hosted_provider_create_attempts a
          WHERE a.id=j.create_attempt_id AND a.provider_order_id=NEW.provider_order_id
            AND a.tenant_id=j.tenant_id AND a.merchant_id=j.merchant_id);
    IF FOUND THEN
      UPDATE public.checkout_sessions
         SET selected_route_id=NEW.id,version=version+1
       WHERE intent_id=NEW.intent_id AND tenant_id=NEW.tenant_id AND merchant_id=NEW.merchant_id
         AND payment_link_id IS NOT NULL AND selected_route_id IS NULL;
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER payment_route_bind_hosted_link_job
AFTER INSERT ON payment_routes FOR EACH ROW EXECUTE FUNCTION bind_hosted_payment_link_job();
REVOKE ALL ON FUNCTION bind_hosted_payment_link_job() FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_management_runtime') THEN
    GRANT SELECT,INSERT ON hosted_payment_link_jobs TO merchant_management_runtime;
    GRANT SELECT ON hosted_payment_link_incidents TO merchant_management_runtime;
    GRANT SELECT,INSERT ON hosted_provider_create_attempts TO merchant_management_runtime;
    GRANT EXECUTE ON FUNCTION admit_hosted_provider_operation(uuid,uuid,text,text,timestamptz) TO merchant_management_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_plan_worker') THEN
    GRANT SELECT,UPDATE ON hosted_payment_link_jobs TO merchant_plan_worker;
    GRANT SELECT,INSERT ON hosted_payment_link_incidents TO merchant_plan_worker;
  END IF;
END $$;

COMMIT;
