package providerops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, ErrDependency
	}
	return &PostgresRepository{pool: pool, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	return r.PingControl(ctx)
}

func (r *PostgresRepository) pingCapability(ctx context.Context, functions ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ready bool
	checks := `to_regclass('provider_operation_bindings') IS NOT NULL`
	arguments := make([]any, len(functions))
	for i, function := range functions {
		checks += fmt.Sprintf(` AND has_function_privilege(current_user,$%d,'EXECUTE')`, i+1)
		arguments[i] = function
	}
	err := r.pool.QueryRow(ctx, `SELECT `+checks, arguments...).Scan(&ready)
	if err != nil || !ready {
		if err == nil {
			err = ErrDependency
		}
		return fmt.Errorf("provider operations PostgreSQL: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PingControl(ctx context.Context) error {
	return r.pingCapability(ctx,
		"request_provider_operation_change(uuid,uuid,uuid,text,bigint,text,uuid,text,timestamptz,timestamptz)",
		"decide_provider_operation_change(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)",
		"request_hosted_provider_policy(uuid,uuid,uuid,bigint,jsonb,text,text,uuid,text,timestamptz,timestamptz)",
		"decide_hosted_provider_policy(uuid,uuid,bigint,boolean,text,uuid,text,timestamptz,timestamptz)",
	)
}

func (r *PostgresRepository) PingHealth(ctx context.Context) error {
	return r.pingCapability(ctx,
		"claim_provider_health_probes(text,integer,timestamptz)",
		"complete_provider_health_probe(uuid,text,text,bigint,bigint,boolean,text,integer,bigint,timestamptz)",
		"provider_health_worker_status(timestamptz)",
		"load_hosted_provider_health_probe(uuid,text,bigint,bigint)",
	)
}

func (r *PostgresRepository) HealthWorkerStatus(ctx context.Context) (WorkerStatus, error) {
	var result WorkerStatus
	err := r.pool.QueryRow(ctx, `SELECT ready,admissible_peer_groups,open_circuits FROM provider_health_worker_status($1)`, r.now()).Scan(&result.Ready, &result.AdmissiblePeerGroups, &result.OpenCircuits)
	if err != nil {
		return WorkerStatus{}, classify(err)
	}
	if result.AdmissiblePeerGroups < 0 || result.OpenCircuits < 0 {
		return WorkerStatus{}, ErrDependency
	}
	return result, nil
}

func (r *PostgresRepository) PingAdmission(ctx context.Context) error {
	return r.pingCapability(ctx, "provider_operation_binding_policy_current(uuid)")
}

func (r *PostgresRepository) within(ctx context.Context, scope Scope, actor, session string, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		global, tenants := "false", scope.TenantID
		if scope.TenantID == "" {
			global, tenants = "true", ""
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.platform_admin_global',$1,true),set_config('app.platform_admin_tenants',$2,true),set_config('app.provider_ops_actor_id',$3,true),set_config('app.provider_ops_session_id',$4,true)`, global, tenants, actor, session); err != nil {
			return err
		}
		return fn(tx)
	})
}

type rowScanner interface{ Scan(...any) error }

const bindingColumns = `b.id::text,b.provider_kind,b.provider_id,COALESCE(b.tenant_id::text,''),COALESCE(b.merchant_id::text,''),COALESCE(b.chain_id,''),b.status,b.version,b.updated_at`

func scanBinding(row rowScanner) (Binding, error) {
	var value Binding
	err := row.Scan(&value.ID, &value.ProviderKind, &value.ProviderID, &value.TenantID, &value.MerchantID, &value.ChainID, &value.Status, &value.Version, &value.UpdatedAt)
	return value, classify(err)
}

func (r *PostgresRepository) ListBindings(ctx context.Context, scope Scope, cursor string, limit int) (Page[Binding], error) {
	var out Page[Binding]
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+bindingColumns+` FROM provider_operation_bindings b WHERE b.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND ($2='' OR b.id>$2::uuid) ORDER BY b.id LIMIT $3`, scope.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanBinding(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return r.loadHealth(ctx, tx, out.Items)
	})
	return out, classify(err)
}

func (r *PostgresRepository) GetBinding(ctx context.Context, scope Scope, id string) (Binding, error) {
	var out Binding
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		var err error
		out, err = scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM provider_operation_bindings b WHERE b.id=$1 AND b.scope_id=platform_scope_uuid(NULLIF($2,'')::uuid)`, id, scope.TenantID))
		if err != nil {
			return err
		}
		items := []Binding{out}
		if err = r.loadHealth(ctx, tx, items); err == nil {
			out = items[0]
		}
		return err
	})
	return out, classify(err)
}

func (r *PostgresRepository) loadHealth(ctx context.Context, tx pgx.Tx, bindings []Binding) error {
	if len(bindings) == 0 {
		return nil
	}
	idsByIndex := make([]string, len(bindings))
	index := make(map[string]int, len(bindings))
	for i := range bindings {
		idsByIndex[i], index[bindings[i].ID] = bindings[i].ID, i
	}
	rows, err := tx.Query(ctx, `SELECT c.binding_id::text,c.operation,c.state,COALESCE(o.error_category,'none'),c.last_success_at,c.last_observed_at,o.lag_blocks,c.version
      FROM provider_circuit_states c LEFT JOIN LATERAL (
        SELECT error_category,lag_blocks FROM provider_health_observations o WHERE o.binding_id=c.binding_id AND o.operation=c.operation ORDER BY observed_at DESC,id DESC LIMIT 1
      ) o ON true WHERE c.binding_id::text=ANY($1) ORDER BY c.binding_id,c.operation`, idsByIndex)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var health HealthState
		if err = rows.Scan(&id, &health.Operation, &health.State, &health.ErrorCategory, &health.LastSuccessAt, &health.LastObservedAt, &health.LagBlocks, &health.Version); err != nil {
			return err
		}
		bindings[index[id]].Health = append(bindings[index[id]].Health, health)
	}
	return rows.Err()
}

const changeColumns = `id::text,binding_id::text,COALESCE(tenant_id::text,''),requested_status,expected_binding_version,status,reason,requested_by::text,COALESCE(approved_by::text,''),COALESCE(rejected_by::text,''),COALESCE(decision_reason,''),created_at,expires_at,decided_at,updated_at,version`

func scanChange(row rowScanner) (ChangeRequest, error) {
	var value ChangeRequest
	err := row.Scan(&value.ID, &value.BindingID, &value.TenantID, &value.RequestedStatus, &value.ExpectedBindingVersion, &value.Status, &value.Reason, &value.RequestedBy, &value.ApprovedBy, &value.RejectedBy, &value.DecisionReason, &value.CreatedAt, &value.ExpiresAt, &value.DecidedAt, &value.UpdatedAt, &value.Version)
	return value, classify(err)
}

func (r *PostgresRepository) ListChanges(ctx context.Context, scope Scope, cursor string, limit int) (Page[ChangeRequest], error) {
	var out Page[ChangeRequest]
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+changeColumns+` FROM provider_operation_change_requests WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND ($2='' OR id>$2::uuid) ORDER BY id LIMIT $3`, scope.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanChange(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return rows.Err()
	})
	return out, classify(err)
}

const hostedPolicyColumns = `id::text,binding_id::text,tenant_id::text,policy_version,policy_payload,encode(payload_hash,'hex'),status,expected_binding_version,reason,requested_by::text,COALESCE(approved_by::text,''),COALESCE(rejected_by::text,''),COALESCE(decision_reason,''),created_at,expires_at,decided_at,activated_at,updated_at,row_version`

func scanHostedPolicy(row rowScanner) (HostedPolicyVersion, error) {
	var value HostedPolicyVersion
	var payload []byte
	err := row.Scan(&value.ID, &value.BindingID, &value.TenantID, &value.PolicyVersion, &payload, &value.PayloadHash, &value.Status, &value.ExpectedBindingVersion, &value.Reason, &value.RequestedBy, &value.ApprovedBy, &value.RejectedBy, &value.DecisionReason, &value.CreatedAt, &value.ExpiresAt, &value.DecidedAt, &value.ActivatedAt, &value.UpdatedAt, &value.RowVersion)
	if err == nil && json.Unmarshal(payload, &value.Policies) != nil {
		err = ErrDependency
	}
	return value, classify(err)
}

func (r *PostgresRepository) ListHostedPolicies(ctx context.Context, scope Scope, cursor string, limit int) (Page[HostedPolicyVersion], error) {
	var out Page[HostedPolicyVersion]
	err := r.within(ctx, scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+hostedPolicyColumns+` FROM provider_hosted_policy_versions WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND ($2='' OR id>$2::uuid) ORDER BY id LIMIT $3`, scope.TenantID, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, scanErr := scanHostedPolicy(rows)
			if scanErr != nil {
				return scanErr
			}
			out.Items = append(out.Items, value)
		}
		if len(out.Items) > limit {
			out.NextCursor = out.Items[limit-1].ID
			out.Items = out.Items[:limit]
		}
		return rows.Err()
	})
	return out, classify(err)
}

func (r *PostgresRepository) mutate(ctx context.Context, principal Principal, scope Scope, operation string, idem Idempotency, out any, fn func(pgx.Tx) error) error {
	return r.within(ctx, scope, principal.ActorID, principal.SessionID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope.TenantID+"\x1f"+principal.ActorID+"\x1f"+operation+"\x1f"+idem.Key); err != nil {
			return err
		}
		var storedHash, body []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,response_body FROM provider_operation_idempotency WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND actor_id=$2 AND operation=$3 AND idempotency_key=$4 AND expires_at>clock_timestamp()`, scope.TenantID, principal.ActorID, operation, idem.Key).Scan(&storedHash, &body)
		if err == nil {
			if !bytes.Equal(storedHash, idem.Fingerprint[:]) {
				return ErrIdempotencyConflict
			}
			if json.Unmarshal(body, out) != nil {
				return ErrDependency
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = fn(tx); err != nil {
			return err
		}
		response, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO provider_operation_idempotency(scope_id,tenant_id,actor_id,operation,idempotency_key,request_hash,response_body,created_at,expires_at) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),NULLIF($1,'')::uuid,$2,$3,$4,$5,$6::jsonb,$7,$8)`, scope.TenantID, principal.ActorID, operation, idem.Key, idem.Fingerprint[:], response, r.now(), r.now().Add(24*time.Hour))
		return err
	})
}

func (r *PostgresRepository) RequestChange(ctx context.Context, principal Principal, input RequestChangeInput, idem Idempotency) (ChangeRequest, error) {
	scope := Scope{TenantID: input.TenantID}
	var out ChangeRequest
	operation := "provider.pause.request"
	if input.RequestedStatus == BindingActive {
		operation = "provider.unpause.request"
	}
	err := r.mutate(ctx, principal, scope, operation, idem, &out, func(tx pgx.Tx) error {
		id, err := ids.New()
		if err != nil {
			return err
		}
		out, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM request_provider_operation_change($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10)`, id, input.BindingID, input.TenantID, input.RequestedStatus, input.ExpectedBindingVersion, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, r.now()))
		return err
	})
	return out, classify(err)
}

func (r *PostgresRepository) DecideChange(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (ChangeRequest, error) {
	var out ChangeRequest
	operation := "provider.change.reject"
	if approve {
		operation = "provider.change.approve"
	}
	err := r.mutate(ctx, principal, scope, operation, idem, &out, func(tx pgx.Tx) error {
		var err error
		out, err = scanChange(tx.QueryRow(ctx, `SELECT `+changeColumns+` FROM decide_provider_operation_change($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9)`, id, scope.TenantID, input.ExpectedRequestVersion, approve, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, r.now()))
		return err
	})
	return out, classify(err)
}

func (r *PostgresRepository) RequestHostedPolicy(ctx context.Context, principal Principal, input RequestHostedPolicyInput, idem Idempotency) (HostedPolicyVersion, error) {
	scope := Scope{TenantID: input.TenantID}
	var out HostedPolicyVersion
	payload, err := json.Marshal(input.Policies)
	if err != nil {
		return out, err
	}
	err = r.mutate(ctx, principal, scope, "provider.policy.request", idem, &out, func(tx pgx.Tx) error {
		id, createErr := ids.New()
		if createErr != nil {
			return createErr
		}
		out, createErr = scanHostedPolicy(tx.QueryRow(ctx, `SELECT `+hostedPolicyColumns+` FROM request_hosted_provider_policy($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11)`, id, input.BindingID, input.TenantID, input.ExpectedBindingVersion, payload, input.BootstrapProbeReference, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, r.now()))
		return createErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) DecideHostedPolicy(ctx context.Context, principal Principal, scope Scope, id string, approve bool, input DecideInput, idem Idempotency) (HostedPolicyVersion, error) {
	var out HostedPolicyVersion
	operation := "provider.policy.reject"
	if approve {
		operation = "provider.policy.approve"
	}
	err := r.mutate(ctx, principal, scope, operation, idem, &out, func(tx pgx.Tx) error {
		var decideErr error
		out, decideErr = scanHostedPolicy(tx.QueryRow(ctx, `SELECT `+hostedPolicyColumns+` FROM decide_hosted_provider_policy($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, scope.TenantID, input.ExpectedRequestVersion, approve, input.Reason, principal.ActorID, principal.SessionID, principal.StepUpAt, r.now()))
		return decideErr
	})
	return out, classify(err)
}

func (r *PostgresRepository) AdmissionCandidates(ctx context.Context, input AdmissionRequest) ([]Candidate, error) {
	result := []Candidate{}
	err := r.within(ctx, input.Scope, "", "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT b.id::text,b.provider_id,b.provider_kind,COALESCE(b.tenant_id::text,''),COALESCE(b.merchant_id::text,''),COALESCE(b.chain_id,''),b.status,
        p.operation,p.timeout_ms,p.max_attempts,p.backoff_ms,p.rate_limit,p.rate_window_seconds,p.max_health_age_seconds,p.max_lag_blocks,p.failure_threshold,p.open_seconds,p.half_open_successes,p.priority,p.failure_domain,p.version,
        c.state,c.consecutive_failures,c.half_open_successes,c.opened_until,COALESCE(c.probe_lease_owner,''),c.probe_lease_token,c.probe_lease_until,c.last_success_at,c.last_observed_at,c.fence_token,c.version
	      FROM provider_operation_bindings b JOIN provider_operation_policies p ON p.binding_id=b.id JOIN provider_circuit_states c ON c.binding_id=p.binding_id AND c.operation=CASE WHEN b.provider_kind='hosted' THEN 'status' ELSE p.operation END
      LEFT JOIN platform_config_snapshots s ON s.id=b.platform_snapshot_id
      LEFT JOIN platform_config_heads h ON h.snapshot_id=s.id AND h.scope_id=s.scope_id
      WHERE b.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND b.provider_kind=$2 AND b.provider_id=ANY($3) AND p.operation=$4 AND p.approved_at IS NOT NULL
	        AND ((b.provider_kind='on_chain' AND p.policy_snapshot_id=b.platform_snapshot_id AND p.policy_snapshot_version=s.version AND p.policy_fence_token=h.fence_token)
	          OR (b.provider_kind='hosted' AND provider_operation_binding_policy_current(b.id)))`, input.Scope.TenantID, input.Kind, input.ProviderIDs, input.Operation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var candidate Candidate
			var timeoutMS, backoffMS, rateWindow, maxAge, openSeconds int
			var maxLag int64
			if err = rows.Scan(&candidate.BindingID, &candidate.ProviderID, &candidate.ProviderKind, &candidate.TenantID, &candidate.MerchantID, &candidate.ChainID, &candidate.Status,
				&candidate.Policy.Operation, &timeoutMS, &candidate.Policy.MaxAttempts, &backoffMS, &candidate.Policy.RateLimit, &rateWindow, &maxAge, &maxLag, &candidate.Policy.FailureThreshold, &openSeconds, &candidate.Policy.HalfOpenSuccesses, &candidate.Policy.Priority, &candidate.Policy.FailureDomain, &candidate.Policy.Version,
				&candidate.Circuit.State, &candidate.Circuit.ConsecutiveFailures, &candidate.Circuit.HalfOpenSuccesses, &candidate.Circuit.OpenedUntil, &candidate.Circuit.LeaseOwner, &candidate.Circuit.LeaseToken, &candidate.Circuit.LeaseUntil, &candidate.Circuit.LastSuccessAt, &candidate.Circuit.LastObservedAt, &candidate.Circuit.FenceToken, &candidate.Circuit.Version); err != nil {
				return err
			}
			candidate.Policy.BindingID, candidate.Policy.TenantID = candidate.BindingID, candidate.TenantID
			candidate.Policy.Timeout, candidate.Policy.Backoff = time.Duration(timeoutMS)*time.Millisecond, time.Duration(backoffMS)*time.Millisecond
			candidate.Policy.RateWindow, candidate.Policy.MaxHealthAge, candidate.Policy.OpenFor = time.Duration(rateWindow)*time.Second, time.Duration(maxAge)*time.Second, time.Duration(openSeconds)*time.Second
			candidate.Policy.MaxLagBlocks = uint64(maxLag)
			result = append(result, candidate)
		}
		return rows.Err()
	})
	return result, classify(err)
}

func (r *PostgresRepository) ClaimProbes(ctx context.Context, owner string, limit int) ([]Probe, error) {
	rows, err := r.pool.Query(ctx, `SELECT
		binding_id,scope_id,COALESCE(tenant_id::text,''),provider_kind,provider_id,
		COALESCE(merchant_id::text,''),COALESCE(chain_id,''),COALESCE(config_logical_key,''),
		COALESCE(platform_snapshot_id::text,''),operation,timeout_ms,max_attempts,backoff_ms,
		rate_limit,rate_window_seconds,max_health_age_seconds,max_lag_blocks,failure_threshold,
		open_seconds,half_open_required,priority,failure_domain,state,consecutive_failures,
		half_successes,opened_until,last_success_at,last_observed_at,lease_token,fence_token,
		circuit_version,COALESCE(probe_reference,'')
		FROM claim_provider_health_probes($1,$2,$3)`, owner, limit, r.now())
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := []Probe{}
	for rows.Next() {
		var probe Probe
		var scopeID string
		var timeoutMS, backoffMS, rateWindow, maxAge, openSeconds int
		var maxLag int64
		if err = rows.Scan(&probe.Candidate.BindingID, &scopeID, &probe.Candidate.TenantID, &probe.Candidate.ProviderKind, &probe.Candidate.ProviderID, &probe.Candidate.MerchantID, &probe.Candidate.ChainID, &probe.Candidate.ConfigLogicalKey, &probe.Candidate.PlatformSnapshotID, &probe.Candidate.Policy.Operation, &timeoutMS, &probe.Candidate.Policy.MaxAttempts, &backoffMS, &probe.Candidate.Policy.RateLimit, &rateWindow, &maxAge, &maxLag, &probe.Candidate.Policy.FailureThreshold, &openSeconds, &probe.Candidate.Policy.HalfOpenSuccesses, &probe.Candidate.Policy.Priority, &probe.Candidate.Policy.FailureDomain, &probe.Candidate.Circuit.State, &probe.Candidate.Circuit.ConsecutiveFailures, &probe.Candidate.Circuit.HalfOpenSuccesses, &probe.Candidate.Circuit.OpenedUntil, &probe.Candidate.Circuit.LastSuccessAt, &probe.Candidate.Circuit.LastObservedAt, &probe.LeaseToken, &probe.FenceToken, &probe.Candidate.Circuit.Version, &probe.Candidate.ProbeReference); err != nil {
			return nil, err
		}
		probe.LeaseOwner = owner
		probe.Candidate.Status = BindingActive
		probe.Candidate.Policy.BindingID, probe.Candidate.Policy.TenantID = probe.Candidate.BindingID, probe.Candidate.TenantID
		probe.Candidate.Policy.Timeout, probe.Candidate.Policy.Backoff = time.Duration(timeoutMS)*time.Millisecond, time.Duration(backoffMS)*time.Millisecond
		probe.Candidate.Policy.RateWindow, probe.Candidate.Policy.MaxHealthAge, probe.Candidate.Policy.OpenFor = time.Duration(rateWindow)*time.Second, time.Duration(maxAge)*time.Second, time.Duration(openSeconds)*time.Second
		probe.Candidate.Policy.MaxLagBlocks = uint64(maxLag)
		result = append(result, probe)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) LoadHostedProbe(ctx context.Context, probe Probe) (HostedProbeTarget, error) {
	var target HostedProbeTarget
	var decimals int16
	err := r.pool.QueryRow(ctx, `SELECT * FROM load_hosted_provider_health_probe($1,$2,$3,$4)`, probe.Candidate.BindingID, probe.LeaseOwner, probe.LeaseToken, probe.FenceToken).Scan(
		&target.ProviderID, &target.TenantID, &target.MerchantID, &target.AdapterKind, &target.APIOrigin,
		&target.CreatePath, &target.CancelPath, &target.StatusPath, &target.RefundPath, &target.ReconcilePath,
		&target.PaymentURLOrigins, &target.CredentialRef, &target.APIKeyID, &target.CallbackSecretRef,
		&target.CallbackKeyID, &target.SignatureScheme, &target.AssetID, &decimals, &target.Currency, &target.Status,
		&target.ProviderReference,
	)
	if err != nil {
		return HostedProbeTarget{}, classify(err)
	}
	if decimals < 0 || decimals > 255 || target.ProviderID != probe.Candidate.ProviderID || target.ProviderReference != probe.Candidate.ProbeReference {
		return HostedProbeTarget{}, ErrDependency
	}
	target.AssetDecimals = uint8(decimals)
	return target, nil
}

func (r *PostgresRepository) CompleteProbe(ctx context.Context, observation Observation) error {
	latency := int(observation.Latency / time.Millisecond)
	var lag any
	if observation.LagBlocks != nil {
		lag = *observation.LagBlocks
	}
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT complete_provider_health_probe($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, observation.BindingID, observation.Operation, observation.LeaseOwner, observation.LeaseToken, observation.FenceToken, observation.Success, observation.Error, latency, lag, observation.ObservedAt).Scan(&ok)
	if err == nil && !ok {
		return ErrLeaseLost
	}
	return classify(err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		case "42501":
			return ErrForbidden
		case "22023", "23514":
			return ErrInvalid
		case "P0002":
			return ErrNotFound
		}
	}
	return err
}
