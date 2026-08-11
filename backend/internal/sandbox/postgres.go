package sandbox

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRepository is the only supported executable sandbox persistence
// runtime. Every transaction verifies a test-environment merchant and
// test-only API client before touching sandbox data.
type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("sandbox PostgreSQL pool is required")
	}
	return &PostgresRepository{pool: pool}, nil
}

// Ping verifies both connectivity and the complete sandbox schema. A sandbox
// process must not report ready when only the production schema was migrated.
func (r *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ready bool
	err := r.pool.QueryRow(ctx, `SELECT
  to_regclass('public.sandbox_workspaces') IS NOT NULL AND
  to_regclass('public.sandbox_scenarios') IS NOT NULL AND
  to_regclass('public.sandbox_events') IS NOT NULL AND
  to_regclass('public.sandbox_callbacks') IS NOT NULL AND
  to_regclass('public.sandbox_callback_attempts') IS NOT NULL AND
  to_regclass('public.sandbox_idempotency') IS NOT NULL AND
  to_regprocedure('public.sandbox_test_credential_admitted(uuid,uuid,text)') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("sandbox readiness query: %w", err)
	}
	if !ready {
		return fmt.Errorf("sandbox schema migration 000013 is incomplete")
	}
	return nil
}

func (r *PostgresRepository) Workspace(ctx context.Context, principal application.Principal) (result Workspace, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		var loadErr error
		result, loadErr = r.ensureWorkspace(ctx, tx, principal)
		return loadErr
	})
	return result, err
}

func (r *PostgresRepository) CreateScenario(ctx context.Context, principal application.Principal, command CreateScenario, key, requestHash string) (result Scenario, replay bool, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		workspace, err := r.ensureWorkspace(ctx, tx, principal)
		if err != nil {
			return err
		}
		operation := "scenario:create"
		if err := sandboxIdempotencyLock(ctx, tx, principal, operation, key); err != nil {
			return err
		}
		body, found, err := findSandboxReplay(ctx, tx, principal, operation, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := json.Unmarshal(body, &result); err != nil {
				return fmt.Errorf("decode sandbox replay: %w", err)
			}
			replay = true
			return nil
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sandbox_scenarios WHERE tenant_id=$1 AND merchant_id=$2 AND merchant_order_id=$3)`, principal.TenantID, principal.MerchantID, command.MerchantOrderID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: sandbox merchant_order_id already exists", domain.ErrStateConflict)
		}
		fixture := trustedMemory(principal, workspace)
		result, _, err = fixture.CreateScenario(ctx, principal, command, key, requestHash)
		if err != nil {
			return err
		}
		updatedWorkspace := fixture.workspaces[merchantScope(principal)]
		if err := updateWorkspace(ctx, tx, principal, updatedWorkspace); err != nil {
			return err
		}
		if err := insertScenario(ctx, tx, principal, result); err != nil {
			return err
		}
		for _, event := range result.Events {
			if err := insertEvent(ctx, tx, principal, result.ID, event); err != nil {
				return err
			}
		}
		for _, callback := range fixture.callbacks {
			if err := upsertCallback(ctx, tx, principal, callback); err != nil {
				return err
			}
		}
		return insertSandboxReplay(ctx, tx, principal, operation, key, requestHash, "scenario", result.ID, result, workspace.Clock)
	})
	return result, replay, err
}

func (r *PostgresRepository) GetScenario(ctx context.Context, principal application.Principal, id string) (result Scenario, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		var loadErr error
		result, loadErr = loadScenario(ctx, tx, principal, id, false)
		return loadErr
	})
	return result, err
}

func (r *PostgresRepository) FindScenarioByIntent(ctx context.Context, principal application.Principal, intentID string) (result Scenario, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		var id string
		queryErr := tx.QueryRow(ctx, `SELECT id::text FROM sandbox_scenarios WHERE tenant_id=$1 AND merchant_id=$2 AND payment_intent_id=$3`, principal.TenantID, principal.MerchantID, intentID).Scan(&id)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		result, queryErr = loadScenario(ctx, tx, principal, id, false)
		return queryErr
	})
	return result, err
}

func (r *PostgresRepository) ListScenarios(ctx context.Context, principal application.Principal, after string, limit int) (result Page[Scenario], err error) {
	result.Items = []Scenario{}
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		created, cursorID := "", ""
		if after != "" {
			created, cursorID, _ = decodeCursor(after)
		}
		rows, queryErr := tx.Query(ctx, `SELECT id::text,created_at FROM sandbox_scenarios
WHERE tenant_id=$1 AND merchant_id=$2
  AND (NULLIF($3,'') IS NULL OR (created_at,id)<(NULLIF($3,'')::timestamptz,NULLIF($4,'')::uuid))
ORDER BY created_at DESC,id DESC LIMIT $5`, principal.TenantID, principal.MerchantID, created, cursorID, limit+1)
		if queryErr != nil {
			return queryErr
		}
		var idsList []string
		var times []time.Time
		for rows.Next() {
			var id string
			var createdAt time.Time
			if err := rows.Scan(&id, &createdAt); err != nil {
				rows.Close()
				return err
			}
			idsList, times = append(idsList, id), append(times, createdAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(idsList) > limit {
			lastID, lastTime := idsList[limit-1], times[limit-1]
			result.NextCursor = cursorFor(lastTime.UTC().Format(time.RFC3339Nano), lastID)
			idsList = idsList[:limit]
		}
		for _, id := range idsList {
			item, err := loadScenario(ctx, tx, principal, id, false)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		return nil
	})
	return result, err
}

func (r *PostgresRepository) ApplyAction(ctx context.Context, principal application.Principal, scenarioID string, action Action, key, requestHash string) (result Scenario, replay bool, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		operation := "scenario:action"
		if err := sandboxIdempotencyLock(ctx, tx, principal, operation, key); err != nil {
			return err
		}
		body, found, err := findSandboxReplay(ctx, tx, principal, operation, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := json.Unmarshal(body, &result); err != nil {
				return fmt.Errorf("decode sandbox replay: %w", err)
			}
			replay = true
			return nil
		}
		workspace, err := r.lockWorkspace(ctx, tx, principal)
		if err != nil {
			return err
		}
		before, err := loadScenario(ctx, tx, principal, scenarioID, true)
		if err != nil {
			return err
		}
		callbacks, err := loadScenarioCallbacks(ctx, tx, principal, scenarioID)
		if err != nil {
			return err
		}
		fixture := trustedMemory(principal, workspace)
		fixture.scenarios[before.ID] = before
		fixture.intentIndex[merchantScope(principal)+"\x1f"+before.PaymentIntent.ID] = before.ID
		for _, callback := range callbacks {
			fixture.callbacks[callback.ID] = callback
		}
		result, _, err = fixture.ApplyAction(ctx, principal, scenarioID, action, key, requestHash)
		if err != nil {
			return err
		}
		updatedWorkspace := fixture.workspaces[merchantScope(principal)]
		if err := updateWorkspace(ctx, tx, principal, updatedWorkspace); err != nil {
			return err
		}
		if err := updateScenario(ctx, tx, principal, result); err != nil {
			return err
		}
		for _, event := range result.Events {
			if event.Sequence > before.LastEventSequence {
				if err := insertEvent(ctx, tx, principal, result.ID, event); err != nil {
					return err
				}
			}
		}
		for _, callback := range fixture.callbacks {
			if err := upsertCallback(ctx, tx, principal, callback); err != nil {
				return err
			}
		}
		return insertSandboxReplay(ctx, tx, principal, operation, key, requestHash, "scenario", result.ID, result, updatedWorkspace.Clock)
	})
	return result, replay, err
}

func (r *PostgresRepository) RunScenario(ctx context.Context, principal application.Principal, scenarioID, key, requestHash string) (result Scenario, replay bool, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		operation := "scenario:run"
		if err := sandboxIdempotencyLock(ctx, tx, principal, operation, key); err != nil {
			return err
		}
		body, found, err := findSandboxReplay(ctx, tx, principal, operation, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := json.Unmarshal(body, &result); err != nil {
				return err
			}
			replay = true
			return nil
		}
		workspace, err := r.lockWorkspace(ctx, tx, principal)
		if err != nil {
			return err
		}
		before, err := loadScenario(ctx, tx, principal, scenarioID, true)
		if err != nil {
			return err
		}
		callbacks, err := loadScenarioCallbacks(ctx, tx, principal, scenarioID)
		if err != nil {
			return err
		}
		fixture := trustedMemory(principal, workspace)
		fixture.scenarios[before.ID] = before
		fixture.intentIndex[merchantScope(principal)+"\x1f"+before.PaymentIntent.ID] = before.ID
		for _, callback := range callbacks {
			fixture.callbacks[callback.ID] = callback
		}
		result, _, err = fixture.RunScenario(ctx, principal, scenarioID, key, requestHash)
		if err != nil {
			return err
		}
		updatedWorkspace := fixture.workspaces[merchantScope(principal)]
		if err := updateWorkspace(ctx, tx, principal, updatedWorkspace); err != nil {
			return err
		}
		if err := updateScenario(ctx, tx, principal, result); err != nil {
			return err
		}
		for _, event := range result.Events {
			if event.Sequence > before.LastEventSequence {
				if err := insertEvent(ctx, tx, principal, result.ID, event); err != nil {
					return err
				}
			}
		}
		for _, callback := range fixture.callbacks {
			if err := upsertCallback(ctx, tx, principal, callback); err != nil {
				return err
			}
		}
		return insertSandboxReplay(ctx, tx, principal, operation, key, requestHash, "scenario", result.ID, result, updatedWorkspace.Clock)
	})
	return result, replay, err
}

func (r *PostgresRepository) ListCallbacks(ctx context.Context, principal application.Principal, scenarioID, after string, limit int) (result Page[Callback], err error) {
	result.Items = []Callback{}
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		created, cursorID := "", ""
		if after != "" {
			created, cursorID, _ = decodeCursor(after)
		}
		rows, queryErr := tx.Query(ctx, `SELECT id::text,created_at FROM sandbox_callbacks
WHERE tenant_id=$1 AND merchant_id=$2 AND (NULLIF($3,'') IS NULL OR scenario_id=NULLIF($3,'')::uuid)
  AND (NULLIF($4,'') IS NULL OR (created_at,id)<(NULLIF($4,'')::timestamptz,NULLIF($5,'')::uuid))
ORDER BY created_at DESC,id DESC LIMIT $6`, principal.TenantID, principal.MerchantID, scenarioID, created, cursorID, limit+1)
		if queryErr != nil {
			return queryErr
		}
		var idsList []string
		var times []time.Time
		for rows.Next() {
			var id string
			var createdAt time.Time
			if err := rows.Scan(&id, &createdAt); err != nil {
				rows.Close()
				return err
			}
			idsList, times = append(idsList, id), append(times, createdAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(idsList) > limit {
			result.NextCursor = cursorFor(times[limit-1].UTC().Format(time.RFC3339Nano), idsList[limit-1])
			idsList = idsList[:limit]
		}
		for _, id := range idsList {
			callback, err := loadCallback(ctx, tx, principal, id)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, callback)
		}
		return nil
	})
	return result, err
}

func (r *PostgresRepository) AdvanceClock(ctx context.Context, principal application.Principal, seconds, expectedVersion int64, key, requestHash string) (result Workspace, replay bool, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		workspace, err := r.ensureWorkspace(ctx, tx, principal)
		if err != nil {
			return err
		}
		operation := "clock:advance"
		if err := sandboxIdempotencyLock(ctx, tx, principal, operation, key); err != nil {
			return err
		}
		body, found, err := findSandboxReplay(ctx, tx, principal, operation, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := json.Unmarshal(body, &result); err != nil {
				return err
			}
			replay = true
			return nil
		}
		if workspace.Version != expectedVersion {
			return domain.ErrVersionConflict
		}
		workspace.Clock = workspace.Clock.Add(time.Duration(seconds) * time.Second)
		workspace.Version++
		if err := updateWorkspace(ctx, tx, principal, workspace); err != nil {
			return err
		}
		result = workspace
		return insertSandboxReplay(ctx, tx, principal, operation, key, requestHash, "workspace", "", result, result.Clock)
	})
	return result, replay, err
}

func (r *PostgresRepository) Reset(ctx context.Context, principal application.Principal, expectedVersion int64, _ string, key, requestHash string) (result ResetResult, replay bool, err error) {
	err = r.withTestMerchant(ctx, principal, func(tx pgx.Tx) error {
		workspace, err := r.ensureWorkspace(ctx, tx, principal)
		if err != nil {
			return err
		}
		operation := "workspace:reset"
		if err := sandboxIdempotencyLock(ctx, tx, principal, operation, key); err != nil {
			return err
		}
		body, found, err := findSandboxReplay(ctx, tx, principal, operation, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			if err := json.Unmarshal(body, &result); err != nil {
				return err
			}
			replay = true
			return nil
		}
		if workspace.Version != expectedVersion {
			return domain.ErrVersionConflict
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM sandbox_callbacks WHERE tenant_id=$1 AND merchant_id=$2`, principal.TenantID, principal.MerchantID).Scan(&result.DeletedCallbacks); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `DELETE FROM sandbox_scenarios WHERE tenant_id=$1 AND merchant_id=$2`, principal.TenantID, principal.MerchantID)
		if err != nil {
			return err
		}
		result.DeletedScenarios = command.RowsAffected()
		if _, err := tx.Exec(ctx, `DELETE FROM sandbox_idempotency WHERE tenant_id=$1 AND merchant_id=$2 AND operation<>'workspace:reset'`, principal.TenantID, principal.MerchantID); err != nil {
			return err
		}
		workspace.Clock, _ = time.Parse(time.RFC3339, DefaultClock)
		workspace.Version++
		if err := updateWorkspace(ctx, tx, principal, workspace); err != nil {
			return err
		}
		result.WorkspaceVersion, result.Clock = workspace.Version, workspace.Clock
		return insertSandboxReplay(ctx, tx, principal, operation, key, requestHash, "reset", "", result, result.Clock)
	})
	return result, replay, err
}

func (r *PostgresRepository) withTestMerchant(ctx context.Context, principal application.Principal, fn func(pgx.Tx) error) error {
	if !ids.Valid(principal.TenantID) || !ids.Valid(principal.MerchantID) || !strings.HasPrefix(principal.KeyID, "mk_test_") {
		return fmt.Errorf("forbidden: live tenant, merchant, or credential rejected by sandbox repository")
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true)`, principal.TenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, principal.MerchantID); err != nil {
				return err
			}
			var admitted bool
			if err := tx.QueryRow(ctx, `SELECT sandbox_test_credential_admitted($1,$2,$3)`, principal.TenantID, principal.MerchantID, principal.KeyID).Scan(&admitted); err != nil {
				return err
			}
			if !admitted {
				return fmt.Errorf("forbidden: principal is not an active test merchant credential")
			}
			return fn(tx)
		})
		if !serializationFailure(err) || attempt == 2 {
			return err
		}
	}
	return fmt.Errorf("unreachable sandbox transaction state")
}

func (r *PostgresRepository) ensureWorkspace(ctx context.Context, tx pgx.Tx, principal application.Principal) (Workspace, error) {
	workspace, err := r.lockWorkspace(ctx, tx, principal)
	if err == nil {
		return workspace, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return Workspace{}, err
	}
	fixture := NewMemoryRepository()
	fixture.trustedTestPrincipal = true
	workspace = fixture.workspaceLocked(principal)
	addresses, _ := json.Marshal(workspace.Addresses)
	_, err = tx.Exec(ctx, `INSERT INTO sandbox_workspaces
(tenant_id,merchant_id,test_clock,credential_key_id,addresses,version,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5::jsonb,$6,$3,$3) ON CONFLICT (tenant_id,merchant_id) DO NOTHING`,
		principal.TenantID, principal.MerchantID, workspace.Clock, principal.KeyID, addresses, workspace.Version)
	if err != nil {
		return Workspace{}, err
	}
	return r.lockWorkspace(ctx, tx, principal)
}

func (r *PostgresRepository) lockWorkspace(ctx context.Context, tx pgx.Tx, principal application.Principal) (Workspace, error) {
	var workspace Workspace
	var addresses []byte
	err := tx.QueryRow(ctx, `SELECT test_clock,credential_key_id,addresses::text,version
FROM sandbox_workspaces WHERE tenant_id=$1 AND merchant_id=$2 FOR UPDATE`, principal.TenantID, principal.MerchantID).
		Scan(&workspace.Clock, &workspace.Credential.KeyID, &addresses, &workspace.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, domain.ErrNotFound
	}
	if err != nil {
		return Workspace{}, err
	}
	if err := json.Unmarshal(addresses, &workspace.Addresses); err != nil {
		return Workspace{}, err
	}
	workspace.Mode, workspace.MerchantID = "sandbox", principal.MerchantID
	workspace.Credential.Environment = "test"
	workspace.Credential.Secret, workspace.Credential.SecretStatus = "[REDACTED]", "configured"
	for scope, allowed := range principal.Scopes {
		if allowed {
			workspace.Credential.Scopes = append(workspace.Credential.Scopes, scope)
		}
	}
	return workspace, nil
}

func trustedMemory(principal application.Principal, workspace Workspace) *MemoryRepository {
	fixture := NewMemoryRepository()
	fixture.trustedTestPrincipal = true
	fixture.workspaces[merchantScope(principal)] = workspace
	return fixture
}

func updateWorkspace(ctx context.Context, tx pgx.Tx, principal application.Principal, workspace Workspace) error {
	addresses, _ := json.Marshal(workspace.Addresses)
	command, err := tx.Exec(ctx, `UPDATE sandbox_workspaces SET test_clock=$3,credential_key_id=$4,addresses=$5::jsonb,version=$6,updated_at=$3
WHERE tenant_id=$1 AND merchant_id=$2`, principal.TenantID, principal.MerchantID, workspace.Clock, principal.KeyID, addresses, workspace.Version)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}

func insertScenario(ctx context.Context, tx pgx.Tx, principal application.Principal, scenario Scenario) error {
	intent, _ := json.Marshal(scenario.PaymentIntent)
	route, _ := json.Marshal(scenario.Route)
	_, err := tx.Exec(ctx, `INSERT INTO sandbox_scenarios
(id,tenant_id,merchant_id,kind,merchant_order_id,payment_intent_id,payment_route_id,intent_snapshot,route_snapshot,
 observed_amount_atomic,observed_asset_id,confirmations,finalized,last_event_sequence,version,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10::uint256,$11,$12,$13,$14,$15,$16,$17)`,
		scenario.ID, principal.TenantID, principal.MerchantID, scenario.Kind, scenario.MerchantOrderID,
		scenario.PaymentIntent.ID, scenario.Route.ID, intent, route, scenario.ObservedAmount, scenario.ObservedAssetID,
		scenario.Confirmations, scenario.Finalized, scenario.LastEventSequence, scenario.Version, scenario.CreatedAt, scenario.UpdatedAt)
	return err
}

func updateScenario(ctx context.Context, tx pgx.Tx, principal application.Principal, scenario Scenario) error {
	intent, _ := json.Marshal(scenario.PaymentIntent)
	route, _ := json.Marshal(scenario.Route)
	command, err := tx.Exec(ctx, `UPDATE sandbox_scenarios SET intent_snapshot=$4::jsonb,route_snapshot=$5::jsonb,
observed_amount_atomic=$6::uint256,observed_asset_id=$7,confirmations=$8,finalized=$9,last_event_sequence=$10,version=$11,updated_at=$12
WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`, principal.TenantID, principal.MerchantID, scenario.ID, intent, route,
		scenario.ObservedAmount, scenario.ObservedAssetID, scenario.Confirmations, scenario.Finalized, scenario.LastEventSequence, scenario.Version, scenario.UpdatedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}

func loadScenario(ctx context.Context, tx pgx.Tx, principal application.Principal, id string, lock bool) (Scenario, error) {
	var scenario Scenario
	var intent, route []byte
	query := `SELECT id::text,kind,merchant_order_id,intent_snapshot::text,route_snapshot::text,observed_amount_atomic::text,
observed_asset_id,confirmations,finalized,last_event_sequence,version,created_at,updated_at
FROM sandbox_scenarios WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, principal.TenantID, principal.MerchantID, id).Scan(
		&scenario.ID, &scenario.Kind, &scenario.MerchantOrderID, &intent, &route, &scenario.ObservedAmount,
		&scenario.ObservedAssetID, &scenario.Confirmations, &scenario.Finalized, &scenario.LastEventSequence,
		&scenario.Version, &scenario.CreatedAt, &scenario.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scenario{}, domain.ErrNotFound
	}
	if err != nil {
		return Scenario{}, err
	}
	if err := json.Unmarshal(intent, &scenario.PaymentIntent); err != nil {
		return Scenario{}, err
	}
	if err := json.Unmarshal(route, &scenario.Route); err != nil {
		return Scenario{}, err
	}
	scenario.TenantID, scenario.MerchantID = principal.TenantID, principal.MerchantID
	rows, err := tx.Query(ctx, `SELECT id::text,sequence,event_type,occurred_at,payload::text FROM sandbox_events
WHERE tenant_id=$1 AND merchant_id=$2 AND scenario_id=$3 ORDER BY sequence`, principal.TenantID, principal.MerchantID, id)
	if err != nil {
		return Scenario{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Sequence, &event.Type, &event.OccurredAt, &event.Payload); err != nil {
			return Scenario{}, err
		}
		scenario.Events = append(scenario.Events, event)
	}
	return scenario, rows.Err()
}

func insertEvent(ctx context.Context, tx pgx.Tx, principal application.Principal, scenarioID string, event Event) error {
	_, err := tx.Exec(ctx, `INSERT INTO sandbox_events
(id,tenant_id,merchant_id,scenario_id,sequence,event_type,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8) ON CONFLICT (scenario_id,sequence) DO NOTHING`,
		event.ID, principal.TenantID, principal.MerchantID, scenarioID, event.Sequence, event.Type, event.Payload, event.OccurredAt)
	return err
}

func upsertCallback(ctx context.Context, tx pgx.Tx, principal application.Principal, callback Callback) error {
	digestBytes, err := hex.DecodeString(callback.BodySHA256)
	if err != nil || len(digestBytes) != 32 {
		return fmt.Errorf("invalid sandbox callback digest")
	}
	_, err = tx.Exec(ctx, `INSERT INTO sandbox_callbacks
(id,tenant_id,merchant_id,scenario_id,event_id,event_sequence,status,canonical_body,body_sha256,attempt_count,created_at,updated_at,version)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status,attempt_count=EXCLUDED.attempt_count,
updated_at=EXCLUDED.updated_at,version=EXCLUDED.version`, callback.ID, principal.TenantID, principal.MerchantID,
		callback.ScenarioID, callback.EventID, callback.EventSequence, callback.Status, []byte(callback.CanonicalBody), digestBytes,
		callback.AttemptCount, callback.CreatedAt, callback.UpdatedAt, callback.Version)
	if err != nil {
		return err
	}
	for _, attempt := range callback.Attempts {
		_, err := tx.Exec(ctx, `INSERT INTO sandbox_callback_attempts
(callback_id,tenant_id,merchant_id,attempt_number,outcome,http_status,error_category,response_bytes,attempted_at)
VALUES ($1,$2,$3,$4,$5,NULLIF($6,0),$7,$8,$9) ON CONFLICT (callback_id,attempt_number) DO NOTHING`,
			callback.ID, principal.TenantID, principal.MerchantID, attempt.Number, attempt.Outcome, attempt.HTTPStatus,
			attempt.ErrorCategory, attempt.ResponseBytes, attempt.AttemptedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func loadScenarioCallbacks(ctx context.Context, tx pgx.Tx, principal application.Principal, scenarioID string) ([]Callback, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM sandbox_callbacks WHERE tenant_id=$1 AND merchant_id=$2 AND scenario_id=$3 ORDER BY event_sequence DESC`, principal.TenantID, principal.MerchantID, scenarioID)
	if err != nil {
		return nil, err
	}
	var callbackIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		callbackIDs = append(callbackIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	callbacks := make([]Callback, 0, len(callbackIDs))
	for _, id := range callbackIDs {
		callback, err := loadCallback(ctx, tx, principal, id)
		if err != nil {
			return nil, err
		}
		callbacks = append(callbacks, callback)
	}
	return callbacks, nil
}

func loadCallback(ctx context.Context, tx pgx.Tx, principal application.Principal, id string) (Callback, error) {
	var callback Callback
	var canonical, digestBytes []byte
	err := tx.QueryRow(ctx, `SELECT id::text,scenario_id::text,event_id::text,event_sequence,status,canonical_body,body_sha256,
attempt_count,created_at,updated_at,version FROM sandbox_callbacks WHERE tenant_id=$1 AND merchant_id=$2 AND id=$3`,
		principal.TenantID, principal.MerchantID, id).Scan(&callback.ID, &callback.ScenarioID, &callback.EventID,
		&callback.EventSequence, &callback.Status, &canonical, &digestBytes, &callback.AttemptCount,
		&callback.CreatedAt, &callback.UpdatedAt, &callback.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Callback{}, domain.ErrNotFound
	}
	if err != nil {
		return Callback{}, err
	}
	callback.CanonicalBody = append(json.RawMessage(nil), canonical...)
	callback.BodySHA256 = hex.EncodeToString(digestBytes)
	rows, err := tx.Query(ctx, `SELECT attempt_number,outcome,COALESCE(http_status,0),error_category,response_bytes,attempted_at
FROM sandbox_callback_attempts WHERE tenant_id=$1 AND merchant_id=$2 AND callback_id=$3 ORDER BY attempt_number`,
		principal.TenantID, principal.MerchantID, id)
	if err != nil {
		return Callback{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt CallbackAttempt
		if err := rows.Scan(&attempt.Number, &attempt.Outcome, &attempt.HTTPStatus, &attempt.ErrorCategory, &attempt.ResponseBytes, &attempt.AttemptedAt); err != nil {
			return Callback{}, err
		}
		callback.Attempts = append(callback.Attempts, attempt)
	}
	return callback, rows.Err()
}

func sandboxIdempotencyLock(ctx context.Context, tx pgx.Tx, principal application.Principal, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, principal.MerchantID+"\x1f"+operation+"\x1f"+key)
	return err
}

func findSandboxReplay(ctx context.Context, tx pgx.Tx, principal application.Principal, operation, key, requestHash string) ([]byte, bool, error) {
	var storedHash, body []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response_body::text FROM sandbox_idempotency
WHERE tenant_id=$1 AND merchant_id=$2 AND operation=$3 AND idempotency_key=$4`,
		principal.TenantID, principal.MerchantID, operation, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	provided, err := hex.DecodeString(requestHash)
	if err != nil || !bytes.Equal(provided, storedHash) {
		return nil, false, domain.ErrIdempotencyConflict
	}
	return body, true, nil
}

func insertSandboxReplay(ctx context.Context, tx pgx.Tx, principal application.Principal, operation, key, requestHash, resourceType, resourceID string, response any, now time.Time) error {
	hash, err := hex.DecodeString(requestHash)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("invalid sandbox request hash")
	}
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sandbox_idempotency
(tenant_id,merchant_id,operation,idempotency_key,request_hash,resource_type,resource_id,response_body,created_at)
VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8::jsonb,$9)`, principal.TenantID, principal.MerchantID,
		operation, key, hash, resourceType, resourceID, body, now)
	return err
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

var _ Repository = (*PostgresRepository)(nil)
