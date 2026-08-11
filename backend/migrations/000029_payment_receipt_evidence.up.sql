BEGIN;

-- A user-supplied receipt is discovery evidence only. The image is sent to the
-- admitted analyzer and immediately discarded; PostgreSQL retains its digest,
-- the bounded structured extraction, and the independently verified proof job.
ALTER TABLE payment_proofs
    ADD CONSTRAINT payment_proofs_id_tenant_unique UNIQUE(id,tenant_id);

CREATE TABLE payment_receipt_evidence (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    route_id uuid NOT NULL,
    image_sha256 bytea NOT NULL CHECK (octet_length(image_sha256)=32),
    image_media_type text NOT NULL CHECK (image_media_type IN ('image/jpeg','image/png','image/webp')),
    image_size bigint NOT NULL CHECK (image_size BETWEEN 128 AND 5242880),
    analyzer_model text NOT NULL CHECK (analyzer_model='google/gemini-3.6-flash'),
    analysis jsonb NOT NULL CHECK (jsonb_typeof(analysis)='object' AND pg_column_size(analysis)<=8192),
    analysis_sha256 bytea NOT NULL CHECK (octet_length(analysis_sha256)=32),
    status text NOT NULL CHECK (status IN ('transaction_not_visible','proof_queued')),
    transaction_id text CHECK (transaction_id IS NULL OR length(transaction_id) BETWEEN 6 AND 256),
    chain_id text NOT NULL,
    proof_id uuid,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    response_body jsonb NOT NULL CHECK (jsonb_typeof(response_body)='object' AND pg_column_size(response_body)<=16384),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY (intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
    FOREIGN KEY (route_id,intent_id,tenant_id) REFERENCES payment_routes(id,intent_id,tenant_id),
    FOREIGN KEY (proof_id,tenant_id) REFERENCES payment_proofs(id,tenant_id),
    UNIQUE (id,tenant_id),
    UNIQUE (merchant_id,idempotency_key),
    CHECK ((status='proof_queued' AND transaction_id IS NOT NULL AND proof_id IS NOT NULL)
        OR (status='transaction_not_visible' AND transaction_id IS NULL AND proof_id IS NULL))
);
CREATE INDEX payment_receipt_evidence_intent_idx
    ON payment_receipt_evidence(tenant_id,merchant_id,intent_id,created_at DESC,id DESC);

ALTER TABLE payment_receipt_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_receipt_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_receipt_evidence_tenant_policy ON payment_receipt_evidence
    USING (tenant_id=current_setting('app.tenant_id',true)::uuid)
    WITH CHECK (tenant_id=current_setting('app.tenant_id',true)::uuid);

CREATE FUNCTION payment_receipt_evidence_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'payment receipt evidence is immutable';
END;
$$;
CREATE TRIGGER payment_receipt_evidence_no_update
    BEFORE UPDATE OR DELETE ON payment_receipt_evidence
    FOR EACH ROW EXECUTE FUNCTION payment_receipt_evidence_immutable();

REVOKE ALL ON payment_receipt_evidence FROM PUBLIC;
REVOKE ALL ON FUNCTION payment_receipt_evidence_immutable() FROM PUBLIC;
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='merchant_management_runtime') THEN
    GRANT SELECT,INSERT ON payment_receipt_evidence TO merchant_management_runtime;
    GRANT SELECT,INSERT ON payment_proofs,idempotency_records TO merchant_management_runtime;
  END IF;
END $$;

COMMIT;
