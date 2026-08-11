BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE DOMAIN uint256 AS numeric(78,0)
    CHECK (VALUE >= 0 AND VALUE <= 115792089237316195423570985008687907853269984665640564039457584007913129639935 AND scale(VALUE) = 0);

CREATE TYPE environment_kind AS ENUM ('test', 'live');
CREATE TYPE intent_status AS ENUM (
    'created', 'awaiting_route_selection', 'pending', 'observed',
    'partially_paid', 'confirmed', 'settled', 'expired', 'needs_review',
    'overpaid', 'reorg_review', 'reversed', 'cancelled'
);
CREATE TYPE route_status AS ENUM ('active', 'expired', 'superseded', 'settled', 'cancelled');
CREATE TYPE transfer_status AS ENUM ('observed', 'confirmed', 'finalized', 'reorged', 'invalidated');
CREATE TYPE unmatched_status AS ENUM (
    'new', 'candidates_ready', 'bound', 'approval_required',
    'verification_requested', 'verification_retry', 'verified', 'resolved',
    'ignored', 'invalid', 'conflict', 'reorged'
);
CREATE TYPE delivery_status AS ENUM ('pending', 'leased', 'retry', 'acknowledged', 'dead_letter');
CREATE TYPE ledger_direction AS ENUM ('debit', 'credit');

CREATE TABLE tenants (
    id uuid PRIMARY KEY,
    public_id text NOT NULL UNIQUE,
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    default_timezone text NOT NULL DEFAULT 'UTC',
    default_locale text NOT NULL DEFAULT 'en',
    retention_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE merchants (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    code text NOT NULL,
    display_name text NOT NULL,
    environment environment_kind NOT NULL,
    settlement_currency char(3) NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'review')),
    callback_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (tenant_id, code, environment),
    UNIQUE (id, tenant_id)
);

CREATE TABLE api_clients (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    key_id text NOT NULL UNIQUE,
    algorithm text NOT NULL CHECK (algorithm IN ('hmac-sha256', 'ed25519', 'mtls')),
    scopes text[] NOT NULL,
    encrypted_secret bytea,
    public_key bytea,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz,
    revoked_at timestamptz,
    ip_policy cidr[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (id, tenant_id),
    CHECK ((algorithm = 'hmac-sha256' AND encrypted_secret IS NOT NULL) OR
           (algorithm = 'ed25519' AND public_key IS NOT NULL) OR algorithm = 'mtls')
);

CREATE TABLE auth_nonces (
    key_id text NOT NULL REFERENCES api_clients(key_id),
    nonce text NOT NULL CHECK (length(nonce) BETWEEN 16 AND 128),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (key_id, nonce)
);

CREATE FUNCTION lookup_api_credential(requested_key_id text)
RETURNS TABLE (client_id uuid, tenant_id uuid, merchant_id uuid, key_id text, encrypted_secret bytea, scopes text[], valid_until timestamptz, version bigint)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
SET row_security = off
AS $$
    SELECT c.id,c.tenant_id,c.merchant_id,c.key_id,c.encrypted_secret,c.scopes,c.valid_until,c.version
      FROM public.api_clients c
      JOIN public.tenants t ON t.id=c.tenant_id AND t.status='active'
      JOIN public.merchants m ON m.id=c.merchant_id AND m.tenant_id=c.tenant_id AND m.status='active'
     WHERE c.key_id=requested_key_id
       AND c.algorithm='hmac-sha256'
       AND c.revoked_at IS NULL
       AND c.valid_from<=clock_timestamp()
       AND (c.valid_until IS NULL OR c.valid_until>clock_timestamp())
     LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_api_credential(text) FROM PUBLIC;
CREATE INDEX auth_nonces_expiry_idx ON auth_nonces (expires_at);

CREATE TABLE chains (
    id text PRIMARY KEY,
    family text NOT NULL CHECK (family IN ('evm', 'tron', 'solana', 'ton', 'aptos')),
    network_name text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'maintenance', 'disabled')),
    required_confirmations bigint NOT NULL CHECK (required_confirmations >= 0),
    maximum_reorg_depth bigint NOT NULL CHECK (maximum_reorg_depth >= 0),
    transaction_url_template text NOT NULL DEFAULT '' CHECK (transaction_url_template = '' OR transaction_url_template LIKE 'https://%{tx}%'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1
);

CREATE TABLE assets (
    id text PRIMARY KEY,
    chain_id text NOT NULL REFERENCES chains(id),
    symbol text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('native', 'fungible_token')),
    canonical_contract text NOT NULL,
    decimals smallint NOT NULL CHECK (decimals BETWEEN 0 AND 77),
    status text NOT NULL CHECK (status IN ('active', 'deposit_disabled', 'deprecated', 'scam_quarantined')),
    minimum_deposit uint256 NOT NULL DEFAULT 0,
    dust_threshold uint256 NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (chain_id, canonical_contract),
    UNIQUE (chain_id, id)
);

CREATE TABLE asset_rate_ticks (
    id uuid PRIMARY KEY,
    asset_id text NOT NULL REFERENCES assets(id),
    fiat_currency char(3) NOT NULL,
    numerator uint256 NOT NULL CHECK (numerator > 0),
    denominator uint256 NOT NULL CHECK (denominator > 0),
    spread_bps integer NOT NULL CHECK (spread_bps BETWEEN 0 AND 10000),
    source text NOT NULL,
    policy_version bigint NOT NULL CHECK (policy_version > 0),
    observed_at timestamptz NOT NULL,
    max_age_seconds integer NOT NULL CHECK (max_age_seconds BETWEEN 1 AND 3600),
    provenance_hash bytea NOT NULL CHECK (octet_length(provenance_hash) = 32),
    status text NOT NULL CHECK (status IN ('active', 'superseded', 'rejected')),
    created_at timestamptz NOT NULL
);
CREATE INDEX asset_rate_ticks_active_idx ON asset_rate_ticks (asset_id, fiat_currency, observed_at DESC) WHERE status='active';

CREATE TABLE wallets (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    merchant_id uuid,
    chain_id text NOT NULL REFERENCES chains(id),
    custody_mode text NOT NULL CHECK (custody_mode IN ('watch_only', 'hot', 'warm', 'external_custodian')),
    signer_key_reference text,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'quarantined')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id)
);

CREATE TABLE addresses (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    wallet_id uuid NOT NULL REFERENCES wallets(id),
    chain_id text NOT NULL REFERENCES chains(id),
    canonical_address text NOT NULL,
    display_address text NOT NULL,
    derivation_index uint256,
    purpose text NOT NULL CHECK (purpose IN ('deposit', 'treasury', 'fee', 'refund')),
    status text NOT NULL CHECK (status IN ('available', 'assigned', 'retired', 'quarantined')),
    first_used_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (chain_id, canonical_address),
    UNIQUE (id, tenant_id)
);

CREATE TABLE rate_quotes (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    merchant_id uuid NOT NULL,
    payment_intent_id uuid NOT NULL,
    planning_idempotency_key text,
    planning_request_hash bytea CHECK (planning_request_hash IS NULL OR octet_length(planning_request_hash) = 32),
    fiat_amount_minor uint256 NOT NULL CHECK (fiat_amount_minor > 0),
    fiat_currency char(3) NOT NULL,
    fiat_scale smallint NOT NULL CHECK (fiat_scale BETWEEN 0 AND 9),
    asset_id text NOT NULL REFERENCES assets(id),
    crypto_amount_atomic uint256 NOT NULL CHECK (crypto_amount_atomic > 0),
    reference_price text NOT NULL,
    spread_bps integer NOT NULL,
    source_tick_ids uuid[] NOT NULL DEFAULT '{}',
    policy_version bigint NOT NULL,
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    raw_provenance_hash bytea NOT NULL CHECK (octet_length(raw_provenance_hash) = 32),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (id, tenant_id)
);
CREATE UNIQUE INDEX rate_quotes_planning_key_idx ON rate_quotes (merchant_id, planning_idempotency_key) WHERE planning_idempotency_key IS NOT NULL;

CREATE TABLE payment_intents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    merchant_order_id text NOT NULL CHECK (length(merchant_order_id) BETWEEN 1 AND 128),
    customer_reference text,
    amount_minor uint256 NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL,
    currency_scale smallint NOT NULL CHECK (currency_scale BETWEEN 0 AND 9),
    description text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (pg_column_size(metadata) <= 16384),
    allowed_routes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_routes) = 'array'),
    status intent_status NOT NULL,
    status_reason text NOT NULL DEFAULT '',
    policy_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    settled_at timestamptz,
    cancelled_at timestamptz,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (merchant_id, merchant_order_id),
    UNIQUE (id, tenant_id)
);
ALTER TABLE rate_quotes ADD CONSTRAINT rate_quotes_intent_fk FOREIGN KEY (payment_intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id);
CREATE INDEX payment_intents_search_idx ON payment_intents (tenant_id, merchant_id, status, created_at DESC, id DESC);
CREATE INDEX payment_intents_expiry_idx ON payment_intents (expires_at) WHERE status IN ('created', 'awaiting_route_selection', 'pending', 'observed', 'partially_paid');

-- Public checkout access uses only a one-way hash of 256 bits of random token
-- material. The raw bearer token exists only in the idempotent create-intent
-- response. It can be revoked independently without exposing tenant identity.
CREATE TABLE checkout_sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (intent_id, tenant_id)
);
CREATE INDEX checkout_sessions_expiry_idx ON checkout_sessions (expires_at);

CREATE FUNCTION lookup_checkout_session(requested_hash bytea)
RETURNS TABLE (tenant_id uuid, merchant_id uuid, intent_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
SET row_security = off
AS $$
    SELECT cs.tenant_id,cs.merchant_id,cs.intent_id
      FROM public.checkout_sessions cs
      JOIN public.tenants t ON t.id=cs.tenant_id AND t.status='active'
      JOIN public.merchants m ON m.id=cs.merchant_id AND m.tenant_id=cs.tenant_id AND m.status='active'
     WHERE cs.token_hash=requested_hash
       AND cs.revoked_at IS NULL
     LIMIT 1
$$;
REVOKE ALL ON FUNCTION lookup_checkout_session(bytea) FROM PUBLIC;

CREATE TABLE address_assignments (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    address_id uuid NOT NULL,
    chain_id text NOT NULL,
    lease_token_hash bytea NOT NULL CHECK (octet_length(lease_token_hash) = 32),
    status text NOT NULL CHECK (status IN ('leased', 'bound', 'released', 'retired')),
    valid_from timestamptz NOT NULL,
    valid_until timestamptz NOT NULL,
    route_id uuid,
    quote_id uuid,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (address_id, tenant_id) REFERENCES addresses(id, tenant_id),
    FOREIGN KEY (quote_id, tenant_id) REFERENCES rate_quotes(id, tenant_id),
    CHECK (valid_from < valid_until)
);
CREATE UNIQUE INDEX address_assignments_active_address_idx ON address_assignments (address_id) WHERE status IN ('leased', 'bound');
CREATE UNIQUE INDEX address_assignments_active_intent_chain_idx ON address_assignments (intent_id, chain_id) WHERE status IN ('leased', 'bound');

CREATE TABLE payment_proofs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    chain_id text NOT NULL REFERENCES chains(id),
    transaction_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'verifying', 'linked', 'not_found', 'invalid')),
    transfer_event_ids uuid[] NOT NULL DEFAULT '{}',
    next_attempt_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    last_error text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (merchant_id, chain_id, transaction_id)
);
CREATE INDEX payment_proofs_queue_idx ON payment_proofs (chain_id, next_attempt_at, id) WHERE status IN ('queued', 'verifying');

CREATE TABLE idempotency_records (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    merchant_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    response_status integer NOT NULL,
    response_body jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    PRIMARY KEY (merchant_id, operation, idempotency_key)
);
CREATE INDEX idempotency_expiry_idx ON idempotency_records (expires_at);

CREATE TABLE payment_routes (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    quote_id uuid,
    address_assignment_id uuid,
    chain_id text NOT NULL REFERENCES chains(id),
    asset_id text NOT NULL REFERENCES assets(id),
    provider text NOT NULL CHECK (provider IN ('on_chain', 'hosted_gateway')),
    expected_amount_atomic uint256 NOT NULL CHECK (expected_amount_atomic > 0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    display_amount text NOT NULL,
    receiving_address text NOT NULL,
    memo text,
    required_finality bigint NOT NULL CHECK (required_finality >= 0),
    status route_status NOT NULL,
    starts_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    grace_ends_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    FOREIGN KEY (quote_id, tenant_id) REFERENCES rate_quotes(id, tenant_id),
    FOREIGN KEY (address_assignment_id) REFERENCES address_assignments(id),
    UNIQUE (id, tenant_id),
    CHECK (starts_at < expires_at AND expires_at <= grace_ends_at)
);
ALTER TABLE address_assignments ADD CONSTRAINT address_assignments_route_fk FOREIGN KEY (route_id, tenant_id) REFERENCES payment_routes(id, tenant_id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX payment_routes_intent_idx ON payment_routes (tenant_id, intent_id, created_at);

CREATE TABLE amount_reservations (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    route_id uuid NOT NULL,
    chain_id text NOT NULL,
    receiving_address text NOT NULL,
    asset_id text NOT NULL,
    exact_amount_atomic uint256 NOT NULL CHECK (exact_amount_atomic > 0),
    active_window tstzrange NOT NULL,
    state text NOT NULL CHECK (state IN ('active', 'released', 'consumed')),
    release_reason text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (route_id, tenant_id) REFERENCES payment_routes(id, tenant_id),
    FOREIGN KEY (chain_id, asset_id) REFERENCES assets(chain_id, id),
    EXCLUDE USING gist (
        chain_id WITH =,
        receiving_address WITH =,
        asset_id WITH =,
        exact_amount_atomic WITH =,
        active_window WITH &&
    ) WHERE (state = 'active')
);

CREATE TABLE chain_blocks (
    chain_id text NOT NULL REFERENCES chains(id),
    height uint256 NOT NULL,
    block_hash text NOT NULL,
    parent_hash text,
    block_time timestamptz NOT NULL,
    canonical_status text NOT NULL CHECK (canonical_status IN ('observed', 'canonical', 'safe', 'finalized', 'reorged')),
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, block_hash)
);
CREATE INDEX chain_blocks_height_idx ON chain_blocks (chain_id, height DESC);

CREATE TABLE transfer_events (
    id uuid PRIMARY KEY,
    chain_id text NOT NULL REFERENCES chains(id),
    transaction_id text NOT NULL,
    event_identity text NOT NULL,
    event_kind text NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    from_address text NOT NULL,
    to_address text NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic > 0),
    asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
    block_hash text NOT NULL,
    block_height uint256 NOT NULL,
    on_chain_time timestamptz NOT NULL,
    confirmations bigint NOT NULL DEFAULT 0 CHECK (confirmations >= 0),
    status transfer_status NOT NULL,
    parser_version text NOT NULL,
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash) = 32),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (chain_id, transaction_id, event_identity, asset_id, to_address),
    UNIQUE (id, asset_id)
);
CREATE INDEX transfer_events_recipient_idx ON transfer_events (chain_id, to_address, asset_id, on_chain_time DESC);

CREATE TABLE event_observations (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL REFERENCES transfer_events(id),
    provider_endpoint_id uuid NOT NULL,
    observed_block_hash text NOT NULL,
    confirmations bigint NOT NULL CHECK (confirmations >= 0),
    validation_outcome text NOT NULL,
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash) = 32),
    observed_at timestamptz NOT NULL,
    UNIQUE (event_id, provider_endpoint_id, observed_block_hash)
);

CREATE TABLE scanner_cursors (
    chain_id text NOT NULL REFERENCES chains(id),
    scanner_shard text NOT NULL,
    capability text NOT NULL,
    cursor_height uint256,
    cursor_token text,
    cursor_hash text,
    locked_by text,
    locked_until timestamptz,
    heartbeat_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, scanner_shard, capability)
);

CREATE TABLE scanner_gaps (
    id uuid PRIMARY KEY,
    chain_id text NOT NULL REFERENCES chains(id),
    from_height uint256 NOT NULL,
    to_height uint256 NOT NULL CHECK (to_height >= from_height),
    reason text NOT NULL,
    status text NOT NULL CHECK (status IN ('open', 'healed')),
    occurrence_count bigint NOT NULL DEFAULT 1 CHECK (occurrence_count > 0),
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    healed_at timestamptz
);
CREATE UNIQUE INDEX scanner_gaps_open_idx ON scanner_gaps (chain_id, from_height, to_height) WHERE status='open';

CREATE TABLE scanner_transfer_queue (
    event_id uuid PRIMARY KEY,
    chain_id text NOT NULL REFERENCES chains(id),
    identity_key text NOT NULL,
    canonical_event jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'leased', 'retry', 'completed', 'dead_letter', 'reorged')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz NOT NULL,
    locked_by text,
    locked_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (chain_id, identity_key)
);
CREATE INDEX scanner_transfer_claim_idx ON scanner_transfer_queue (next_attempt_at, event_id) WHERE status IN ('pending', 'retry');

CREATE TABLE payment_matches (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    event_id uuid NOT NULL REFERENCES transfer_events(id),
    route_id uuid NOT NULL,
    intent_id uuid NOT NULL,
    match_kind text NOT NULL CHECK (match_kind IN ('exact', 'partial', 'gasfree_policy', 'manual', 'cross_asset_override')),
    expected_atomic uint256 NOT NULL,
    received_atomic uint256 NOT NULL CHECK (received_atomic > 0),
    credited_atomic uint256 NOT NULL,
    state text NOT NULL CHECK (state IN ('proposed', 'finalized', 'reversed')),
    evidence jsonb NOT NULL,
    policy_version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    finalized_at timestamptz,
    reversed_at timestamptz,
    FOREIGN KEY (route_id, tenant_id) REFERENCES payment_routes(id, tenant_id),
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    CHECK (credited_atomic <= received_atomic)
);
CREATE UNIQUE INDEX payment_matches_event_active_idx ON payment_matches (event_id) WHERE state <> 'reversed';

CREATE TABLE unmatched_payments (
    id uuid PRIMARY KEY,
    tenant_id uuid REFERENCES tenants(id),
    event_id uuid NOT NULL UNIQUE REFERENCES transfer_events(id),
    classification text NOT NULL,
    status unmatched_status NOT NULL,
    selected_route_id uuid,
    accepted_shortfall boolean NOT NULL DEFAULT false,
    accepted_late_payment boolean NOT NULL DEFAULT false,
    accepted_cross_asset boolean NOT NULL DEFAULT false,
    workflow_version bigint NOT NULL,
    assigned_operator_id uuid,
    severity text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (id, tenant_id)
);
CREATE INDEX unmatched_queue_idx ON unmatched_payments (status, severity, created_at);

CREATE TABLE match_candidates (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    unmatched_id uuid NOT NULL REFERENCES unmatched_payments(id),
    route_id uuid NOT NULL,
    rank integer NOT NULL CHECK (rank > 0),
    score integer NOT NULL,
    evidence jsonb NOT NULL,
    disqualifiers text[] NOT NULL DEFAULT '{}',
    candidate_set_version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (unmatched_id, tenant_id) REFERENCES unmatched_payments(id, tenant_id),
    FOREIGN KEY (route_id, tenant_id) REFERENCES payment_routes(id, tenant_id),
    UNIQUE (unmatched_id, candidate_set_version, route_id),
    UNIQUE (unmatched_id, candidate_set_version, rank)
);

CREATE TABLE manual_resolutions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    unmatched_id uuid NOT NULL REFERENCES unmatched_payments(id),
    event_id uuid NOT NULL REFERENCES transfer_events(id),
    target_route_id uuid NOT NULL,
    candidate_set_version bigint NOT NULL CHECK (candidate_set_version > 0),
    idempotency_key text NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    requested_by uuid NOT NULL,
    approved_by uuid,
    accept_shortfall boolean NOT NULL DEFAULT false,
    accept_late_payment boolean NOT NULL DEFAULT false,
    accept_cross_asset boolean NOT NULL DEFAULT false,
    human_reason text NOT NULL,
    status unmatched_status NOT NULL,
    verifier_evidence_hash bytea CHECK (verifier_evidence_hash IS NULL OR octet_length(verifier_evidence_hash) = 32),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    next_attempt_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    last_error text,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (unmatched_id, tenant_id) REFERENCES unmatched_payments(id, tenant_id),
    FOREIGN KEY (target_route_id, tenant_id) REFERENCES payment_routes(id, tenant_id),
    FOREIGN KEY (unmatched_id, candidate_set_version, target_route_id) REFERENCES match_candidates(unmatched_id, candidate_set_version, route_id),
    FOREIGN KEY (requested_by, tenant_id) REFERENCES api_clients(id, tenant_id),
    FOREIGN KEY (approved_by, tenant_id) REFERENCES api_clients(id, tenant_id),
    UNIQUE (unmatched_id, idempotency_key),
    CHECK (approved_by IS NULL OR approved_by <> requested_by),
    CHECK (status <> 'approval_required' OR ((accept_shortfall OR accept_cross_asset) AND approved_by IS NULL)),
    CHECK (NOT (accept_shortfall OR accept_cross_asset) OR status = 'approval_required' OR approved_by IS NOT NULL)
);
CREATE INDEX manual_resolution_verify_idx ON manual_resolutions (next_attempt_at, id) WHERE status IN ('verification_requested', 'verification_retry');

CREATE TABLE ai_rank_suggestions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    unmatched_id uuid NOT NULL,
    requested_by uuid NOT NULL,
    model text NOT NULL,
    endpoint_host text NOT NULL,
    recommended_route_id uuid NOT NULL,
    confidence double precision NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    reason_codes text[] NOT NULL,
    review_required boolean NOT NULL CHECK (review_required),
    candidate_set_version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (unmatched_id, tenant_id) REFERENCES unmatched_payments(id, tenant_id),
    FOREIGN KEY (recommended_route_id, tenant_id) REFERENCES payment_routes(id, tenant_id),
    FOREIGN KEY (requested_by, tenant_id) REFERENCES api_clients(id, tenant_id)
);
CREATE INDEX ai_rank_suggestions_case_idx ON ai_rank_suggestions (tenant_id, unmatched_id, created_at DESC);

CREATE TABLE ledger_accounts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    merchant_id uuid,
    asset_id text NOT NULL REFERENCES assets(id),
    account_code text NOT NULL,
    account_type text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, merchant_id, asset_id, account_code),
    UNIQUE (id, tenant_id),
    UNIQUE (id, tenant_id, asset_id)
);

CREATE TABLE ledger_transactions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    business_type text NOT NULL,
    business_reference text NOT NULL,
    reversal_of uuid REFERENCES ledger_transactions(id),
    effective_at timestamptz NOT NULL,
    booked_at timestamptz NOT NULL,
    correlation_id text,
    policy_version bigint NOT NULL,
    UNIQUE (tenant_id, business_type, business_reference),
    UNIQUE (id, tenant_id)
);

CREATE TABLE ledger_entries (
    transaction_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    sequence integer NOT NULL CHECK (sequence > 0),
    account_id uuid NOT NULL,
    asset_id text NOT NULL REFERENCES assets(id),
    direction ledger_direction NOT NULL,
    amount_atomic uint256 NOT NULL CHECK (amount_atomic > 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (transaction_id, sequence),
    FOREIGN KEY (transaction_id, tenant_id) REFERENCES ledger_transactions(id, tenant_id),
    FOREIGN KEY (account_id, tenant_id, asset_id) REFERENCES ledger_accounts(id, tenant_id, asset_id)
);

CREATE FUNCTION assert_ledger_transaction_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    bad_asset text;
    entry_count integer;
BEGIN
    SELECT count(*) INTO entry_count FROM ledger_entries WHERE transaction_id = COALESCE(NEW.transaction_id, OLD.transaction_id);
    IF entry_count < 2 THEN
        RAISE EXCEPTION 'ledger transaction % requires at least two entries', COALESCE(NEW.transaction_id, OLD.transaction_id);
    END IF;
    SELECT asset_id INTO bad_asset
      FROM ledger_entries
     WHERE transaction_id = COALESCE(NEW.transaction_id, OLD.transaction_id)
     GROUP BY asset_id
    HAVING sum(amount_atomic) FILTER (WHERE direction = 'debit') IS DISTINCT FROM
           sum(amount_atomic) FILTER (WHERE direction = 'credit')
     LIMIT 1;
    IF bad_asset IS NOT NULL THEN
        RAISE EXCEPTION 'ledger transaction % is not balanced for asset %', COALESCE(NEW.transaction_id, OLD.transaction_id), bad_asset;
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER ledger_entries_balanced
AFTER INSERT OR UPDATE OR DELETE ON ledger_entries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION assert_ledger_transaction_balanced();

CREATE TABLE webhook_endpoints (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    endpoint_url text NOT NULL,
    event_types text[] NOT NULL,
    encrypted_signing_secret bytea NOT NULL,
    signing_key_id text NOT NULL,
    timeout_ms integer NOT NULL CHECK (timeout_ms BETWEEN 100 AND 30000),
    max_concurrency integer NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    status text NOT NULL CHECK (status IN ('unverified', 'active', 'disabled')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (id, tenant_id)
);

CREATE TABLE callback_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    intent_id uuid,
    event_type text NOT NULL,
    schema_version text NOT NULL,
    canonical_payload jsonb NOT NULL,
    canonical_body bytea NOT NULL,
    body_hash bytea NOT NULL CHECK (octet_length(body_hash) = 32),
    signing_key_id text NOT NULL,
    merchant_sequence bigint NOT NULL CHECK (merchant_sequence > 0),
    aggregate_sequence bigint,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    FOREIGN KEY (intent_id, tenant_id) REFERENCES payment_intents(id, tenant_id),
    UNIQUE (merchant_id, merchant_sequence),
    UNIQUE (intent_id, aggregate_sequence),
    UNIQUE (id, tenant_id)
);

CREATE TABLE callback_deliveries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    callback_event_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    status delivery_status NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz NOT NULL,
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    last_http_status integer,
    last_error_category text,
    acknowledged_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (callback_event_id, endpoint_id),
    UNIQUE (id, tenant_id),
    FOREIGN KEY (callback_event_id, tenant_id) REFERENCES callback_events(id, tenant_id),
    FOREIGN KEY (endpoint_id, tenant_id) REFERENCES webhook_endpoints(id, tenant_id)
);
CREATE INDEX callback_claim_idx ON callback_deliveries (next_attempt_at, id) WHERE status IN ('pending', 'retry');

CREATE TABLE callback_attempts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    attempt_number integer NOT NULL,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    duration_ms integer,
    http_status integer,
    response_body_hash bytea,
    response_snippet text,
    error_category text,
    UNIQUE (delivery_id, attempt_number),
    FOREIGN KEY (delivery_id, tenant_id) REFERENCES callback_deliveries(id, tenant_id)
);

CREATE TABLE outbox_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    aggregate_sequence bigint NOT NULL,
    event_type text NOT NULL,
    schema_version text NOT NULL,
    payload jsonb NOT NULL,
    correlation_id text,
    causation_id uuid,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    published_at timestamptz,
    locked_by text,
    locked_until timestamptz,
    lease_token uuid,
    attempt_count integer NOT NULL DEFAULT 0,
    last_error text,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (aggregate_type, aggregate_id, aggregate_sequence)
);
CREATE INDEX outbox_publish_idx ON outbox_events (available_at, id) WHERE published_at IS NULL;

-- Immutable merchant-visible history is advanced only after an external sink
-- acknowledges the stable event ID, and atomically with the fenced local lease.
CREATE TABLE event_history (
    event_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    aggregate_sequence bigint NOT NULL,
    event_type text NOT NULL,
    schema_version text NOT NULL,
    payload jsonb NOT NULL,
    correlation_id text,
    causation_id uuid,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL,
    published_at timestamptz NOT NULL,
    FOREIGN KEY (merchant_id, tenant_id) REFERENCES merchants(id, tenant_id),
    UNIQUE (tenant_id, event_id)
);
CREATE INDEX event_history_merchant_cursor_idx ON event_history (tenant_id, merchant_id, occurred_at DESC, event_id DESC);

CREATE TABLE consumer_inbox (
    consumer_name text NOT NULL,
    event_id uuid NOT NULL,
    processed_at timestamptz NOT NULL,
    result_hash bytea,
    PRIMARY KEY (consumer_name, event_id)
);

ALTER TABLE payment_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkout_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE rate_quotes ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE callback_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE address_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_proofs ENABLE ROW LEVEL SECURITY;
ALTER TABLE merchants ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE wallets ENABLE ROW LEVEL SECURITY;
ALTER TABLE addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_matches ENABLE ROW LEVEL SECURITY;
ALTER TABLE unmatched_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE match_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE manual_resolutions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_rank_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE callback_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE callback_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_intents FORCE ROW LEVEL SECURITY;
ALTER TABLE checkout_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE rate_quotes FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE callback_events FORCE ROW LEVEL SECURITY;
ALTER TABLE address_assignments FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_proofs FORCE ROW LEVEL SECURITY;
ALTER TABLE merchants FORCE ROW LEVEL SECURITY;
ALTER TABLE wallets FORCE ROW LEVEL SECURITY;
ALTER TABLE addresses FORCE ROW LEVEL SECURITY;
ALTER TABLE idempotency_records FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_matches FORCE ROW LEVEL SECURITY;
ALTER TABLE unmatched_payments FORCE ROW LEVEL SECURITY;
ALTER TABLE match_candidates FORCE ROW LEVEL SECURITY;
ALTER TABLE manual_resolutions FORCE ROW LEVEL SECURITY;
ALTER TABLE ai_rank_suggestions FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_transactions FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_entries FORCE ROW LEVEL SECURITY;
ALTER TABLE callback_deliveries FORCE ROW LEVEL SECURITY;
ALTER TABLE callback_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox_events FORCE ROW LEVEL SECURITY;
ALTER TABLE event_history FORCE ROW LEVEL SECURITY;

CREATE POLICY payment_intents_tenant_policy ON payment_intents
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY checkout_sessions_tenant_policy ON checkout_sessions
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY payment_routes_tenant_policy ON payment_routes
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY rate_quotes_tenant_policy ON rate_quotes
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY webhook_endpoints_tenant_policy ON webhook_endpoints
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY callback_events_tenant_policy ON callback_events
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY address_assignments_tenant_policy ON address_assignments
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY payment_proofs_tenant_policy ON payment_proofs
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY merchants_tenant_policy ON merchants
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY api_clients_tenant_policy ON api_clients
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY wallets_tenant_policy ON wallets
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY addresses_tenant_policy ON addresses
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY idempotency_records_tenant_policy ON idempotency_records
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY payment_matches_tenant_policy ON payment_matches
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY unmatched_payments_tenant_policy ON unmatched_payments
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY match_candidates_tenant_policy ON match_candidates
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY manual_resolutions_tenant_policy ON manual_resolutions
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY ai_rank_suggestions_tenant_policy ON ai_rank_suggestions
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY ledger_accounts_tenant_policy ON ledger_accounts
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY ledger_transactions_tenant_policy ON ledger_transactions
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY ledger_entries_tenant_policy ON ledger_entries
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY callback_deliveries_tenant_policy ON callback_deliveries
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY callback_attempts_tenant_policy ON callback_attempts
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY outbox_events_tenant_policy ON outbox_events
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY event_history_tenant_policy ON event_history
    USING (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);

COMMIT;
