BEGIN;

-- This aggregate binds the exact route/intent to a canonical transfer before
-- settlement. Identity/economic facts never change; a new inclusion after a
-- reorg advances generation instead of overwriting history silently.
CREATE TABLE payment_observations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    route_id uuid NOT NULL,
    transfer_event_id uuid NOT NULL UNIQUE REFERENCES transfer_events(id),
    chain_id text NOT NULL,
    asset_id text NOT NULL,
    transaction_id text NOT NULL,
    event_identity text NOT NULL,
    from_address text NOT NULL,
    to_address text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK(amount_atomic>0),
    asset_decimals smallint NOT NULL CHECK(asset_decimals BETWEEN 0 AND 77),
    block_hash text NOT NULL,
    block_height uint256 NOT NULL,
    block_time timestamptz NOT NULL,
    confirmations bigint NOT NULL CHECK(confirmations>=0),
    required_confirmations bigint NOT NULL CHECK(required_confirmations>0),
    finality text NOT NULL CHECK(finality IN ('observed','confirmed','finalized','reorged')),
    evidence_hash bytea NOT NULL CHECK(octet_length(evidence_hash)=32),
    generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    FOREIGN KEY(merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY(intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
    FOREIGN KEY(route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
    UNIQUE(id,tenant_id),
    UNIQUE(tenant_id,route_id,transfer_event_id),
    UNIQUE(chain_id,transaction_id,event_identity,asset_id,to_address)
);
CREATE INDEX payment_observations_intent_idx ON payment_observations(tenant_id,intent_id,updated_at DESC,id);
CREATE INDEX payment_observations_reorg_idx ON payment_observations(transfer_event_id) WHERE finality<>'reorged';

CREATE FUNCTION payment_observation_immutable_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
  IF ROW(OLD.tenant_id,OLD.merchant_id,OLD.intent_id,OLD.route_id,OLD.transfer_event_id,
         OLD.chain_id,OLD.asset_id,OLD.transaction_id,OLD.event_identity,
         OLD.from_address,OLD.to_address,OLD.amount_atomic,OLD.asset_decimals)
     IS DISTINCT FROM
     ROW(NEW.tenant_id,NEW.merchant_id,NEW.intent_id,NEW.route_id,NEW.transfer_event_id,
         NEW.chain_id,NEW.asset_id,NEW.transaction_id,NEW.event_identity,
         NEW.from_address,NEW.to_address,NEW.amount_atomic,NEW.asset_decimals) THEN
    RAISE EXCEPTION 'payment observation canonical facts are immutable' USING ERRCODE='23000';
  END IF;
  IF NEW.generation<OLD.generation OR NEW.version<=OLD.version THEN
    RAISE EXCEPTION 'payment observation generation/version must advance' USING ERRCODE='23000';
  END IF;
  IF NEW.generation=OLD.generation AND
     ROW(OLD.block_hash,OLD.block_height,OLD.block_time,OLD.evidence_hash)
       IS DISTINCT FROM ROW(NEW.block_hash,NEW.block_height,NEW.block_time,NEW.evidence_hash) THEN
    RAISE EXCEPTION 'inclusion facts require a new observation generation' USING ERRCODE='23000';
  END IF;
  IF NEW.generation=OLD.generation AND NEW.confirmations<OLD.confirmations THEN
    RAISE EXCEPTION 'confirmations cannot decrease inside a generation' USING ERRCODE='23000';
  END IF;
  IF NEW.generation=OLD.generation AND NOT (
       NEW.finality=OLD.finality
       OR (OLD.finality='observed' AND NEW.finality IN ('confirmed','finalized','reorged'))
       OR (OLD.finality='confirmed' AND NEW.finality IN ('finalized','reorged'))
       OR (OLD.finality='finalized' AND NEW.finality='reorged')) THEN
    RAISE EXCEPTION 'illegal payment observation finality transition' USING ERRCODE='23000';
  END IF;
  IF NEW.generation>OLD.generation AND NOT (
       NEW.generation=OLD.generation+1 AND OLD.finality='reorged'
       AND NEW.finality IN ('observed','confirmed','finalized')) THEN
    RAISE EXCEPTION 'only a reorged observation may start the next generation' USING ERRCODE='23000';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER payment_observation_immutable
BEFORE UPDATE ON payment_observations FOR EACH ROW EXECUTE FUNCTION payment_observation_immutable_guard();

-- One row proves which callback and outbox records represent a transition.
-- Generation allows observed/confirming/reorged to recur only after an honest
-- re-inclusion, never because the scanner delivered the same fact twice.
CREATE TABLE payment_observation_events (
    observation_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    generation bigint NOT NULL CHECK(generation>0),
    observation_version bigint NOT NULL CHECK(observation_version>0),
    event_type text NOT NULL CHECK(event_type IN ('payment.observed','payment.confirming','payment.settled','payment.reorged')),
    callback_event_id uuid NOT NULL,
    outbox_event_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY(observation_id,generation,event_type),
    FOREIGN KEY(observation_id,tenant_id) REFERENCES payment_observations(id,tenant_id),
    FOREIGN KEY(callback_event_id,tenant_id) REFERENCES callback_events(id,tenant_id),
    FOREIGN KEY(outbox_event_id) REFERENCES outbox_events(id),
    UNIQUE(outbox_event_id),
    UNIQUE(callback_event_id)
);

-- Manual resolution webhook production is an atomic projection of the source
-- status transition. This closes the lost-response gap without a best-effort
-- polling queue and records request/approval/retry/rejection/resolution once.
ALTER TABLE manual_resolutions
    ADD CONSTRAINT manual_resolutions_id_tenant_unique UNIQUE(id,tenant_id);

CREATE TABLE manual_resolution_events (
    resolution_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    resolution_version bigint NOT NULL CHECK(resolution_version>0),
    status unmatched_status NOT NULL,
    callback_event_id uuid NOT NULL,
    outbox_event_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY(resolution_id,resolution_version),
    FOREIGN KEY(resolution_id,tenant_id) REFERENCES manual_resolutions(id,tenant_id),
    FOREIGN KEY(callback_event_id,tenant_id) REFERENCES callback_events(id,tenant_id),
    FOREIGN KEY(outbox_event_id) REFERENCES outbox_events(id),
    UNIQUE(callback_event_id),
    UNIQUE(outbox_event_id)
);

CREATE FUNCTION emit_manual_resolution_webhook() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE
  selected_merchant uuid;
  selected_intent uuid;
  selected_order text;
  selected_amount text;
  selected_currency text;
  selected_intent_status text;
  is_live boolean;
  event_id uuid:=gen_random_uuid();
  outbox_id uuid:=gen_random_uuid();
  merchant_seq bigint;
  event_body jsonb;
  body_bytes bytea;
  event_signing_key text;
  occurred timestamptz:=clock_timestamp();
BEGIN
  IF TG_OP='UPDATE' AND NEW.status=OLD.status THEN RETURN NEW; END IF;

  SELECT r.merchant_id,r.intent_id,i.merchant_order_id,i.amount_minor::text,
         i.currency,i.status::text,(m.environment='live')
    INTO STRICT selected_merchant,selected_intent,selected_order,selected_amount,
         selected_currency,selected_intent_status,is_live
    FROM public.payment_routes r
    JOIN public.payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id
    JOIN public.merchants m ON m.id=r.merchant_id AND m.tenant_id=r.tenant_id
   WHERE r.id=NEW.target_route_id AND r.tenant_id=NEW.tenant_id;

  INSERT INTO public.merchant_event_sequences(tenant_id,merchant_id,last_sequence,updated_at)
  VALUES(NEW.tenant_id,selected_merchant,1,occurred)
  ON CONFLICT(tenant_id,merchant_id) DO UPDATE
  SET last_sequence=public.merchant_event_sequences.last_sequence+1,updated_at=EXCLUDED.updated_at
  RETURNING last_sequence INTO merchant_seq;

  event_body:=jsonb_build_object(
    'event_id',event_id::text,'event_type','payment.resolution.updated','schema_version','1',
    'sequence',merchant_seq,'occurred_at',occurred,'merchant_id',selected_merchant::text,
    'livemode',is_live,
    'payment_intent',jsonb_build_object('id',selected_intent::text,'merchant_order_id',selected_order,
      'status',selected_intent_status,'amount_minor',selected_amount,'currency',selected_currency),
    'resolution',jsonb_build_object('resolution_id',NEW.id::text,'unmatched_payment_id',NEW.unmatched_id::text,
      'transfer_event_id',NEW.event_id::text,'payment_route_id',NEW.target_route_id::text,
      'status',NEW.status::text,'version',NEW.version,
      'approval_required',(NEW.accept_shortfall OR NEW.accept_cross_asset),
      'approved',(NEW.approved_by IS NOT NULL),'accept_shortfall',NEW.accept_shortfall,
      'accept_late_payment',NEW.accept_late_payment,'accept_cross_asset',NEW.accept_cross_asset,
      'evidence_verified',(NEW.verifier_evidence_hash IS NOT NULL)));
  body_bytes:=convert_to(event_body::text,'UTF8');
  SELECT COALESCE(min(signing_key_id),'unconfigured') INTO event_signing_key
    FROM public.webhook_endpoints WHERE tenant_id=NEW.tenant_id AND merchant_id=selected_merchant AND status='active';

  INSERT INTO public.callback_events(id,tenant_id,merchant_id,intent_id,event_type,schema_version,
    canonical_payload,canonical_body,body_hash,signing_key_id,merchant_sequence,aggregate_sequence,occurred_at,created_at)
  VALUES(event_id,NEW.tenant_id,selected_merchant,selected_intent,'payment.resolution.updated','1',
    event_body,body_bytes,digest(body_bytes,'sha256'),event_signing_key,merchant_seq,NULL,occurred,occurred);

  INSERT INTO public.callback_deliveries(id,tenant_id,callback_event_id,endpoint_id,signing_key_id,status,
    attempt_count,next_attempt_at,created_at,updated_at,version)
  SELECT gen_random_uuid(),NEW.tenant_id,event_id,w.id,w.signing_key_id,'pending',0,occurred,occurred,occurred,1
    FROM public.webhook_endpoints w
   WHERE w.tenant_id=NEW.tenant_id AND w.merchant_id=selected_merchant AND w.status='active'
     AND ('payment.resolution.updated'=ANY(w.event_types) OR '*'=ANY(w.event_types))
   FOR UPDATE OF w;

  INSERT INTO public.outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,
    aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at)
  VALUES(outbox_id,NEW.tenant_id,selected_merchant,'manual_resolution',NEW.id,NEW.version,NEW.version,
    'payment.resolution.updated','1',event_body,NEW.event_id::text,occurred,occurred,occurred);
  INSERT INTO public.manual_resolution_events(resolution_id,tenant_id,resolution_version,status,
    callback_event_id,outbox_event_id,created_at)
  VALUES(NEW.id,NEW.tenant_id,NEW.version,NEW.status,event_id,outbox_id,occurred);
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION emit_manual_resolution_webhook() FROM PUBLIC;
CREATE TRIGGER manual_resolution_webhook
AFTER INSERT OR UPDATE OF status ON manual_resolutions
FOR EACH ROW EXECUTE FUNCTION emit_manual_resolution_webhook();

ALTER TABLE payment_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_observation_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE manual_resolution_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_observations FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_observation_events FORCE ROW LEVEL SECURITY;
ALTER TABLE manual_resolution_events FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_observations_tenant_policy ON payment_observations
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY payment_observation_events_tenant_policy ON payment_observation_events
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY manual_resolution_events_tenant_policy ON manual_resolution_events
  USING(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK(tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

REVOKE DELETE,TRUNCATE ON payment_observations,payment_observation_events,manual_resolution_events FROM PUBLIC;
DO $$
DECLARE role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY['merchant_scanner_worker','merchant_settlement_worker'] LOOP
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('GRANT SELECT,INSERT,UPDATE ON payment_observations TO %I',role_name);
      EXECUTE format('GRANT SELECT,INSERT ON payment_observation_events TO %I',role_name);
    END IF;
  END LOOP;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_resolution_worker') THEN
    GRANT SELECT ON manual_resolution_events TO merchant_resolution_worker;
  END IF;
END $$;

COMMIT;
