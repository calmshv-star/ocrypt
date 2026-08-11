BEGIN;

-- Financial decisions are bound to immutable, merchant-scoped policy versions.
-- There is deliberately no platform-wide permissive default.
CREATE TABLE automated_matching_policy_changes (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    proposed_version bigint NOT NULL CHECK (proposed_version>0),
    accumulate_partials boolean NOT NULL DEFAULT false,
    underpayment_tolerance_bps integer NOT NULL DEFAULT 0 CHECK (underpayment_tolerance_bps BETWEEN 0 AND 10000),
    overpayment_mode text NOT NULL DEFAULT 'manual_review'
        CHECK (overpayment_mode IN ('manual_review','credit_all','credit_expected_hold_excess')),
    accept_late_within_grace boolean NOT NULL DEFAULT false,
    require_same_sender boolean NOT NULL DEFAULT true,
    gasfree_enabled boolean NOT NULL DEFAULT false,
    gasfree_fee_collectors text[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('draft','pending_approval','approved','activated','rejected')),
    created_by uuid NOT NULL REFERENCES admin_users(id),
    requested_by uuid REFERENCES admin_users(id),
    approved_by uuid REFERENCES admin_users(id),
    activated_by uuid REFERENCES admin_users(id),
    request_reason text,
    approval_reason text,
    activation_reason text,
    approved_at timestamptz,
    activated_at timestamptz,
    effective_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    UNIQUE (tenant_id,merchant_id,proposed_version),
    UNIQUE (id,tenant_id),
    CHECK (NOT gasfree_enabled OR cardinality(gasfree_fee_collectors)>0),
    CHECK (array_position(gasfree_fee_collectors,'') IS NULL),
    CHECK (approved_by IS NULL OR approved_by<>requested_by),
    CHECK (activated_by IS NULL OR activated_by<>requested_by),
    CHECK (
      (status='draft' AND requested_by IS NULL AND approved_by IS NULL AND activated_by IS NULL) OR
      (status='pending_approval' AND requested_by IS NOT NULL AND approved_by IS NULL AND activated_by IS NULL AND length(request_reason) BETWEEN 8 AND 1000) OR
      (status='approved' AND requested_by IS NOT NULL AND approved_by IS NOT NULL AND activated_by IS NULL AND approved_at IS NOT NULL AND length(approval_reason) BETWEEN 8 AND 1000) OR
      (status='activated' AND requested_by IS NOT NULL AND approved_by IS NOT NULL AND activated_by IS NOT NULL AND approved_at IS NOT NULL AND activated_at IS NOT NULL AND effective_at IS NOT NULL AND length(activation_reason) BETWEEN 8 AND 1000) OR
      (status='rejected' AND requested_by IS NOT NULL AND approved_by IS NOT NULL AND activated_by IS NULL AND approved_at IS NOT NULL AND length(approval_reason) BETWEEN 8 AND 1000)
    )
);

CREATE TABLE automated_matching_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    accumulate_partials boolean NOT NULL DEFAULT false,
    underpayment_tolerance_bps integer NOT NULL DEFAULT 0 CHECK (underpayment_tolerance_bps BETWEEN 0 AND 10000),
    overpayment_mode text NOT NULL DEFAULT 'manual_review'
        CHECK (overpayment_mode IN ('manual_review','credit_all','credit_expected_hold_excess')),
    accept_late_within_grace boolean NOT NULL DEFAULT false,
    require_same_sender boolean NOT NULL DEFAULT true,
    gasfree_enabled boolean NOT NULL DEFAULT false,
    gasfree_fee_collectors text[] NOT NULL DEFAULT '{}',
    effective_at timestamptz NOT NULL,
    change_request_id uuid NOT NULL,
    requested_by uuid NOT NULL REFERENCES admin_users(id),
    approved_by uuid NOT NULL REFERENCES admin_users(id),
    activated_by uuid NOT NULL REFERENCES admin_users(id),
    approval_reference text NOT NULL CHECK (length(approval_reference) BETWEEN 8 AND 1000),
    config_hash bytea NOT NULL CHECK (octet_length(config_hash)=32),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY (change_request_id,tenant_id) REFERENCES automated_matching_policy_changes(id,tenant_id),
    UNIQUE (tenant_id,merchant_id,version),
    UNIQUE (change_request_id),
    UNIQUE (id,tenant_id),
    CHECK (NOT gasfree_enabled OR cardinality(gasfree_fee_collectors)>0),
    CHECK (array_position(gasfree_fee_collectors,'') IS NULL)
);
CREATE INDEX automated_matching_policy_effective_idx
    ON automated_matching_policies(tenant_id,merchant_id,effective_at DESC,version DESC);

CREATE TABLE automated_matching_policy_idempotency (
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    actor_id uuid NOT NULL REFERENCES admin_users(id),
    operation text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
    resource_id uuid NOT NULL,
    response_body jsonb NOT NULL CHECK (pg_column_size(response_body)<=65536),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    PRIMARY KEY (tenant_id,merchant_id,actor_id,operation,idempotency_key)
);

-- A route keeps the policy that existed when it was issued. Later policy
-- versions never silently change an invoice already shown to a payer.
CREATE TABLE payment_route_policy_bindings (
    route_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version>0),
    policy_snapshot jsonb NOT NULL CHECK (jsonb_typeof(policy_snapshot)='object'),
    config_hash bytea NOT NULL CHECK (octet_length(config_hash)=32),
    bound_at timestamptz NOT NULL,
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY (policy_id,tenant_id) REFERENCES automated_matching_policies(id,tenant_id),
    FOREIGN KEY (tenant_id,merchant_id,policy_version)
        REFERENCES automated_matching_policies(tenant_id,merchant_id,version),
    UNIQUE (route_id,tenant_id)
);

CREATE FUNCTION bind_route_matching_policy() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,public
SET row_security=off
AS $$
DECLARE
    p automated_matching_policies%ROWTYPE;
    snapshot jsonb;
BEGIN
    SELECT * INTO p FROM automated_matching_policies
     WHERE tenant_id=NEW.tenant_id AND merchant_id=NEW.merchant_id
       AND effective_at<=NEW.created_at
     ORDER BY effective_at DESC,version DESC LIMIT 1;
    IF FOUND THEN
        snapshot := jsonb_build_object(
             'id',p.id::text,'version',p.version,
             'accumulate_partials',p.accumulate_partials,
             'underpayment_tolerance_bps',p.underpayment_tolerance_bps,
             'overpayment_mode',p.overpayment_mode,
             'accept_late_within_grace',p.accept_late_within_grace,
             'require_same_sender',p.require_same_sender,
             'gasfree_enabled',p.gasfree_enabled,
             'gasfree_fee_collectors',to_jsonb(p.gasfree_fee_collectors));
        INSERT INTO payment_route_policy_bindings
          (route_id,tenant_id,merchant_id,policy_id,policy_version,policy_snapshot,config_hash,bound_at)
        VALUES
          (NEW.id,NEW.tenant_id,NEW.merchant_id,p.id,p.version,snapshot,
           digest(convert_to(snapshot::text,'UTF8'),'sha256'),NEW.created_at);
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION bind_route_matching_policy() FROM PUBLIC;
CREATE TRIGGER payment_route_bind_matching_policy
AFTER INSERT ON payment_routes FOR EACH ROW EXECUTE FUNCTION bind_route_matching_policy();

CREATE TABLE payment_match_aggregates (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    route_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version>0),
    state text NOT NULL CHECK (state IN ('collecting','settled','review','reversed')),
    classification text NOT NULL CHECK (classification IN ('exact','partial','underpaid','overpaid','late','gasfree_policy','ambiguous','unmatched')),
    expected_atomic uint256 NOT NULL CHECK (expected_atomic>0),
    received_atomic uint256 NOT NULL,
    credited_atomic uint256 NOT NULL,
    treasury_received_atomic uint256 NOT NULL,
    gasfree_fees_atomic uint256 NOT NULL,
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence)='object'),
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash)=32),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    settled_at timestamptz,
    reversed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
    FOREIGN KEY (intent_id,tenant_id) REFERENCES payment_intents(id,tenant_id),
    FOREIGN KEY (policy_id,tenant_id) REFERENCES automated_matching_policies(id,tenant_id),
    CHECK (credited_atomic<=received_atomic),
    UNIQUE (id,tenant_id)
);
CREATE UNIQUE INDEX payment_match_aggregates_route_active_idx
    ON payment_match_aggregates(tenant_id,route_id) WHERE state<>'reversed';

ALTER TABLE payment_matches
    ADD COLUMN aggregate_id uuid,
    ADD COLUMN allocation_role text NOT NULL DEFAULT 'payment'
        CHECK (allocation_role IN ('payment','gasfree_fee')),
    ADD CONSTRAINT payment_matches_aggregate_fk
        FOREIGN KEY (aggregate_id,tenant_id) REFERENCES payment_match_aggregates(id,tenant_id);
CREATE INDEX payment_matches_aggregate_idx ON payment_matches(tenant_id,aggregate_id) WHERE state<>'reversed';

CREATE TABLE automated_matching_decisions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    aggregate_id uuid NOT NULL,
    route_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    policy_version bigint NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('collect','settle','manual_review')),
    classification text NOT NULL,
    canonical_evidence jsonb NOT NULL CHECK (jsonb_typeof(canonical_evidence)='object'),
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash)=32),
    decided_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id),
    FOREIGN KEY (aggregate_id,tenant_id) REFERENCES payment_match_aggregates(id,tenant_id),
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
    FOREIGN KEY (policy_id,tenant_id) REFERENCES automated_matching_policies(id,tenant_id),
    UNIQUE (tenant_id,aggregate_id,evidence_hash)
);

CREATE TABLE automated_matching_jobs (
    route_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','leased','retry','completed','dead_letter')),
    next_attempt_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    reschedule_requested boolean NOT NULL DEFAULT false,
    last_error text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (route_id,tenant_id) REFERENCES payment_routes(id,tenant_id),
    FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id)
);
CREATE INDEX automated_matching_jobs_claim_idx ON automated_matching_jobs(next_attempt_at,route_id)
    WHERE status IN ('pending','retry');

CREATE FUNCTION prevent_automated_matching_history_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'automated matching history is append-only'; END $$;
CREATE TRIGGER automated_matching_policies_append_only
BEFORE UPDATE OR DELETE ON automated_matching_policies FOR EACH ROW EXECUTE FUNCTION prevent_automated_matching_history_mutation();
CREATE TRIGGER payment_route_policy_bindings_append_only
BEFORE UPDATE OR DELETE ON payment_route_policy_bindings FOR EACH ROW EXECUTE FUNCTION prevent_automated_matching_history_mutation();
CREATE TRIGGER automated_matching_decisions_append_only
BEFORE UPDATE OR DELETE ON automated_matching_decisions FOR EACH ROW EXECUTE FUNCTION prevent_automated_matching_history_mutation();

ALTER TABLE automated_matching_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_policy_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_policy_idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_route_policy_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_match_aggregates ENABLE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_policy_changes FORCE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_policy_idempotency FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_route_policy_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_match_aggregates FORCE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_decisions FORCE ROW LEVEL SECURITY;
ALTER TABLE automated_matching_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY automated_matching_policies_tenant_policy ON automated_matching_policies
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY automated_matching_policy_changes_tenant_policy ON automated_matching_policy_changes
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY automated_matching_policy_idempotency_tenant_policy ON automated_matching_policy_idempotency
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY payment_route_policy_bindings_tenant_policy ON payment_route_policy_bindings
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY payment_match_aggregates_tenant_policy ON payment_match_aggregates
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY automated_matching_decisions_tenant_policy ON automated_matching_decisions
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);
CREATE POLICY automated_matching_jobs_tenant_policy ON automated_matching_jobs
    USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

INSERT INTO admin_permissions(permission_key,description) VALUES
('matching_policy:read','Read deterministic matching policy changes'),
('matching_policy:write','Create and request deterministic matching policy changes'),
('matching_policy:approve','Approve or reject deterministic matching policy changes'),
('matching_policy:activate','Activate an independently approved deterministic matching policy');

-- Extend the central audit authorization map in the same forward migration as
-- the matching permissions. Without this, append_management_audit rejects the
-- mutation and PostgreSQL rolls the complete policy transaction back.
CREATE OR REPLACE FUNCTION management_permission_for_action(p_action text)
RETURNS text LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public AS $$
    SELECT CASE
      WHEN p_action LIKE 'payment_link.%' THEN 'payment_links:write'
      WHEN p_action LIKE 'checkout.%' THEN 'checkout:write'
      WHEN p_action='webhook.secret_rotated' THEN 'webhook_settings:rotate'
      WHEN p_action='webhook.disabled' THEN 'webhook_settings:disable'
      WHEN p_action IN ('webhook.disable_requested','webhook.disable_rejected') THEN 'webhook_settings:disable'
      WHEN p_action LIKE 'webhook.%' THEN 'webhook_settings:write'
      WHEN p_action='api_client.rotated' THEN 'api_clients:rotate'
      WHEN p_action='api_client.revoked' THEN 'api_clients:revoke'
      WHEN p_action IN ('api_client.revoke_requested','api_client.revoke_rejected') THEN 'api_clients:revoke'
      WHEN p_action LIKE 'api_client.%' THEN 'api_clients:write'
      WHEN p_action='matching_policy.approved' THEN 'matching_policy:approve'
      WHEN p_action='matching_policy.activated' THEN 'matching_policy:activate'
      WHEN p_action LIKE 'matching_policy.%' THEN 'matching_policy:write'
      ELSE NULL END
$$;
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
('payment_operator','matching_policy:read'),('payment_operator','matching_policy:write'),
('senior_approver','matching_policy:read'),('senior_approver','matching_policy:approve'),('senior_approver','matching_policy:activate'),
('security_admin','matching_policy:read'),('security_admin','matching_policy:approve'),('security_admin','matching_policy:activate'),
('auditor','matching_policy:read');

COMMIT;
