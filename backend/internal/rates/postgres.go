package rates

import (
	"context"
	"errors"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, ErrUnavailable
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}

func (s *PostgresStore) within(ctx context.Context, owner string, target Target, fn func(pgx.Tx) error) error {
	if !ids.Valid(owner) || !validTarget(target) {
		return ErrInvalidConfig
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		global, tenants := "false", target.TenantID
		if target.TenantID == "" {
			global, tenants = "true", ""
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.rate_worker_id',$1,true),set_config('app.rate_runtime_global',$2,true),set_config('app.rate_runtime_tenants',$3,true),set_config('app.platform_admin_global',$2,true),set_config('app.platform_admin_tenants',$3,true)`, owner, global, tenants); err != nil {
			return err
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM rate_runtime_identities WHERE id=$1 AND purpose='rate_collection'`, owner).Scan(&enabled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDisabled
			}
			return err
		}
		if !enabled {
			return ErrDisabled
		}
		return fn(tx)
	})
}

func (s *PostgresStore) EnsureTargets(ctx context.Context, owner string, targets []Target) error {
	seen := make(map[Target]struct{}, len(targets))
	for _, target := range SortedTargets(targets) {
		if _, duplicate := seen[target]; duplicate {
			continue
		}
		seen[target] = struct{}{}
		if err := s.within(ctx, owner, target, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO rate_runtime_jobs(scope_id,tenant_id,policy_key) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),NULLIF($1,'')::uuid,$2) ON CONFLICT(scope_id,policy_key) DO NOTHING`, target.TenantID, target.PolicyKey)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// DueTargets performs one bounded queue read for the worker loop. The former
// per-target polling path opened a transaction for every configured pair even
// when nothing was due; this keeps idle operation to one cheap query.
func (s *PostgresStore) DueTargets(ctx context.Context, owner string, configured []Target, limit int) (due []Target, err error) {
	if !ids.Valid(owner) || len(configured) < 1 || len(configured) > 256 || limit < 1 || limit > 256 {
		return nil, ErrInvalidConfig
	}
	keys := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, target := range configured {
		if !validTarget(target) {
			return nil, ErrInvalidConfig
		}
		if _, duplicate := seen[target.PolicyKey]; duplicate {
			continue
		}
		seen[target.PolicyKey] = struct{}{}
		keys = append(keys, target.PolicyKey)
	}
	if len(keys) == 0 {
		return nil, ErrInvalidConfig
	}
	err = s.within(ctx, owner, Target{PolicyKey: keys[0]}, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT policy_key FROM rate_runtime_jobs
WHERE scope_id=platform_scope_uuid(NULL) AND tenant_id IS NULL AND status='active'
  AND policy_key=ANY($1::text[]) AND next_attempt_at<=clock_timestamp()
  AND (lease_until IS NULL OR lease_until<clock_timestamp())
ORDER BY updated_at DESC,next_attempt_at,policy_key LIMIT $2`, keys, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if scanErr := rows.Scan(&key); scanErr != nil {
				return scanErr
			}
			due = append(due, Target{PolicyKey: key})
		}
		return rows.Err()
	})
	return due, err
}

func (s *PostgresStore) Claim(ctx context.Context, owner string, target Target, lease time.Duration) (Claim, bool, error) {
	if lease < time.Second || lease > 5*time.Minute {
		return Claim{}, false, ErrInvalidConfig
	}
	var claim Claim
	claimed := false
	err := s.within(ctx, owner, target, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE rate_runtime_jobs SET lease_owner=$3,lease_until=clock_timestamp()+$4*interval '1 millisecond',claim_token=claim_token+1,attempts=attempts+1,updated_at=clock_timestamp() WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND status='active' AND next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp()) RETURNING claim_token,attempts`, target.TenantID, target.PolicyKey, owner, lease.Milliseconds())
		if err := row.Scan(&claim.ClaimToken, &claim.Attempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		claim.Target, claimed = target, true
		return nil
	})
	return claim, claimed, err
}

func (s *PostgresStore) Commit(ctx context.Context, owner string, collection Collection, nextAttempt time.Time) error {
	if err := validateCollection(collection); err != nil || nextAttempt.IsZero() {
		return ErrInvalidConfig
	}
	target := collection.Claim.Target
	return s.within(ctx, owner, target, func(tx pgx.Tx) error {
		var token int64
		if err := tx.QueryRow(ctx, `SELECT claim_token FROM rate_runtime_jobs WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND status='active' AND lease_owner=$3 AND claim_token=$4 AND lease_until>=clock_timestamp() FOR UPDATE`, target.TenantID, target.PolicyKey, owner, collection.Claim.ClaimToken).Scan(&token); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		if err := currentSnapshot(ctx, tx, target, "rate_policy", collection.Config.Policy.Key, collection.Config.Policy.SnapshotID, collection.Config.Policy.FenceToken); err != nil {
			return err
		}
		for _, source := range collection.Config.Sources {
			if err := currentSnapshot(ctx, tx, target, "rate_source", source.Key, source.SnapshotID, source.FenceToken); err != nil {
				return err
			}
		}
		var assetActive bool
		if err := tx.QueryRow(ctx, `SELECT rate_runtime_asset_active($1)`, collection.Tick.BaseAsset).Scan(&assetActive); err != nil {
			return err
		}
		if !assetActive {
			return ErrInvalidConfig
		}
		if _, err := tx.Exec(ctx, `INSERT INTO rate_runtime_pair_bindings(base_asset,quote_asset,policy_key,first_policy_snapshot_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, collection.Tick.BaseAsset, collection.Tick.QuoteAsset, collection.Config.Policy.Key, collection.Config.Policy.SnapshotID); err != nil {
			return err
		}
		var binding bool
		// The binding is append-only and protected by an immutable-row trigger;
		// a row lock would unnecessarily require mutable table privileges.
		if err := tx.QueryRow(ctx, `SELECT true FROM rate_runtime_pair_bindings WHERE base_asset=$1 AND quote_asset=$2 AND policy_key=$3`, collection.Tick.BaseAsset, collection.Tick.QuoteAsset, collection.Config.Policy.Key).Scan(&binding); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidConfig
			}
			return err
		}
		var databaseNow time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return err
		}
		sourceByKey := make(map[string]SourceConfig, len(collection.Config.Sources))
		for _, source := range collection.Config.Sources {
			sourceByKey[source.Key] = source
		}
		for _, observation := range collection.Observations {
			source, exists := sourceByKey[observation.SourceKey]
			if !exists {
				return ErrInvalidConfig
			}
			ageLimit := min(collection.Config.Policy.MaxAge, source.MaxAge)
			if observation.ProviderObservedAt.After(databaseNow.Add(collection.Config.Policy.FutureTolerance)) {
				return ErrFuture
			}
			if observation.ProviderObservedAt.Before(databaseNow.Add(-ageLimit)) {
				return ErrStale
			}
			_, err := tx.Exec(ctx, `INSERT INTO rate_source_observations(id,scope_id,tenant_id,policy_key,source_key,provider_ref,provider_observation_id,base_asset,quote_asset,price_numerator,price_denominator,provider_observed_at,received_at,raw_response_hash,rate_source_snapshot_id,source_fence_token) VALUES($1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11,clock_timestamp(),$12,$13,$14)`, observation.ID, target.TenantID, target.PolicyKey, observation.SourceKey, observation.ProviderRef, observation.ProviderObservationID, observation.BaseAsset, observation.QuoteAsset, observation.Price.Numerator.String(), observation.Price.Denominator.String(), observation.ProviderObservedAt, observation.RawResponseHash[:], observation.SourceSnapshotID, observation.SourceFenceToken)
			if err != nil {
				return err
			}
		}
		tick := collection.Tick
		maxAgeSeconds := int64(tick.ExpiresAt.Sub(tick.ObservedAt) / time.Second)
		if maxAgeSeconds < 1 || maxAgeSeconds > 3600 || collection.Config.Policy.SnapshotVersion < 1 {
			return ErrInvalidConfig
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "rate-pair\x1f"+tick.BaseAsset+"\x1f"+tick.QuoteAsset); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE asset_rate_ticks SET status='superseded' WHERE asset_id=$1 AND fiat_currency=$2 AND status='active'`, tick.BaseAsset, tick.QuoteAsset); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO asset_rate_ticks(id,asset_id,fiat_currency,numerator,denominator,spread_bps,source,policy_version,observed_at,max_age_seconds,provenance_hash,status,created_at) SELECT $1,$2,$3,$4::uint256,$5::uint256,$6,$7,$8,$9::timestamptz,$10::bigint,$11,'active',clock_timestamp() WHERE $9::timestamptz+$10::bigint*interval '1 second'>clock_timestamp()`, tick.ID, tick.BaseAsset, tick.QuoteAsset, tick.Price.Numerator.String(), tick.Price.Denominator.String(), tick.SpreadBPS, "rate-runtime:"+collection.Config.Policy.Key, collection.Config.Policy.SnapshotVersion, tick.ObservedAt, maxAgeSeconds, tick.SourcesDigest[:]); err != nil {
			return err
		}
		var projected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM asset_rate_ticks WHERE id=$1 AND status='active')`, tick.ID).Scan(&projected); err != nil {
			return err
		}
		if !projected {
			return ErrStale
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admitted_rate_ticks(id,scope_id,tenant_id,policy_key,base_asset,quote_asset,price_numerator,price_denominator,observed_at,admitted_at,expires_at,spread_bps,quorum,source_count,rate_policy_snapshot_id,policy_fence_token,sources_digest) SELECT $1,platform_scope_uuid(NULLIF($2,'')::uuid),NULLIF($2,'')::uuid,$3,$4,$5,$6::numeric,$7::numeric,$8,clock_timestamp(),$9,$10,$11,$12,$13,$14,$15 WHERE $9>clock_timestamp()`, tick.ID, target.TenantID, target.PolicyKey, tick.BaseAsset, tick.QuoteAsset, tick.Price.Numerator.String(), tick.Price.Denominator.String(), tick.ObservedAt, tick.ExpiresAt, tick.SpreadBPS, tick.Quorum, tick.SourceCount, tick.PolicySnapshotID, tick.PolicyFenceToken, tick.SourcesDigest[:]); err != nil {
			return err
		}
		var inserted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admitted_rate_ticks WHERE id=$1 AND scope_id=platform_scope_uuid(NULLIF($2,'')::uuid))`, tick.ID, target.TenantID).Scan(&inserted); err != nil {
			return err
		}
		if !inserted {
			return ErrStale
		}
		for _, observation := range collection.Observations {
			if _, err := tx.Exec(ctx, `INSERT INTO admitted_rate_tick_observations(scope_id,tick_id,observation_id,source_key) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),$2,$3,$4)`, target.TenantID, tick.ID, observation.ID, observation.SourceKey); err != nil {
				return err
			}
		}
		command, err := tx.Exec(ctx, `UPDATE rate_runtime_jobs SET lease_owner=NULL,lease_until=NULL,attempts=0,next_attempt_at=GREATEST($5,clock_timestamp()+interval '1 second'),last_error_code=NULL,last_success_at=clock_timestamp(),updated_at=clock_timestamp() WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND lease_owner=$3 AND claim_token=$4`, target.TenantID, target.PolicyKey, owner, collection.Claim.ClaimToken, nextAttempt.UTC())
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func currentSnapshot(ctx context.Context, tx pgx.Tx, target Target, kind, key, snapshotID string, fence int64) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT rate_runtime_snapshot_current(NULLIF($1,'')::uuid,$2,$3,$4,$5)`, target.TenantID, kind, key, snapshotID, fence).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !found) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) Fail(ctx context.Context, owner string, claim Claim, code string, nextAttempt time.Time, maxAttempts int) (bool, error) {
	if !validErrorCode(code) || claim.ClaimToken < 1 || claim.Attempts < 1 || maxAttempts < 1 || maxAttempts > 100 || nextAttempt.IsZero() {
		return false, ErrInvalidConfig
	}
	dead := claim.Attempts >= maxAttempts
	err := s.within(ctx, owner, claim.Target, func(tx pgx.Tx) error {
		var attempts int
		if err := tx.QueryRow(ctx, `SELECT attempts FROM rate_runtime_jobs WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND status='active' AND lease_owner=$3 AND claim_token=$4 AND lease_until>=clock_timestamp() FOR UPDATE`, claim.Target.TenantID, claim.Target.PolicyKey, owner, claim.ClaimToken).Scan(&attempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		if attempts != claim.Attempts {
			return ErrLeaseLost
		}
		if dead {
			if _, err := tx.Exec(ctx, `INSERT INTO rate_collection_dead_letters(scope_id,tenant_id,policy_key,claim_token,attempts,error_code) VALUES(platform_scope_uuid(NULLIF($1,'')::uuid),NULLIF($1,'')::uuid,$2,$3,$4,$5)`, claim.Target.TenantID, claim.Target.PolicyKey, claim.ClaimToken, attempts, code); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE rate_runtime_jobs SET status='dead_letter',lease_owner=NULL,lease_until=NULL,last_error_code=$5,dead_lettered_at=clock_timestamp(),updated_at=clock_timestamp() WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND lease_owner=$3 AND claim_token=$4`, claim.Target.TenantID, claim.Target.PolicyKey, owner, claim.ClaimToken, code)
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE rate_runtime_jobs SET lease_owner=NULL,lease_until=NULL,next_attempt_at=GREATEST($5,clock_timestamp()+interval '1 second'),last_error_code=$6,updated_at=clock_timestamp() WHERE scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND policy_key=$2 AND lease_owner=$3 AND claim_token=$4`, claim.Target.TenantID, claim.Target.PolicyKey, owner, claim.ClaimToken, nextAttempt.UTC(), code)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
	return dead, err
}

func (s *PostgresStore) Health(ctx context.Context, owner string, targets []Target, maxReadyAge time.Duration) (Health, error) {
	health := Health{ConfiguredTargets: len(targets)}
	if maxReadyAge < time.Second || maxReadyAge > 24*time.Hour {
		return health, ErrInvalidConfig
	}
	if err := s.Ping(ctx); err != nil {
		return health, err
	}
	health.DatabaseReady = true
	for _, target := range targets {
		err := s.within(ctx, owner, target, func(tx pgx.Tx) error {
			var status string
			var fresh bool
			var admitted *time.Time
			if err := tx.QueryRow(ctx, `SELECT j.status,COALESCE(t.expires_at>clock_timestamp() AND t.admitted_at>clock_timestamp()-$3*interval '1 millisecond',false),t.admitted_at FROM rate_runtime_jobs j LEFT JOIN LATERAL (SELECT admitted_at,expires_at FROM admitted_rate_ticks WHERE scope_id=j.scope_id AND policy_key=j.policy_key ORDER BY admitted_at DESC,id DESC LIMIT 1) t ON true WHERE j.scope_id=platform_scope_uuid(NULLIF($1,'')::uuid) AND j.policy_key=$2`, target.TenantID, target.PolicyKey, maxReadyAge.Milliseconds()).Scan(&status, &fresh, &admitted); err != nil {
				return err
			}
			if status == "dead_letter" {
				health.DeadLetteredTargets++
			}
			if fresh {
				health.FreshTargets++
				if admitted != nil && (health.OldestTickAt.IsZero() || admitted.Before(health.OldestTickAt)) {
					health.OldestTickAt = admitted.UTC()
				}
			}
			return nil
		})
		if err != nil {
			return health, err
		}
	}
	health.Ready = health.DatabaseReady && health.ConfiguredTargets > 0 && health.FreshTargets == health.ConfiguredTargets && health.DeadLetteredTargets == 0
	return health, nil
}

func validateCollection(collection Collection) error {
	if !validTarget(collection.Claim.Target) || collection.Claim.ClaimToken < 1 || collection.Claim.Attempts < 1 || len(collection.Observations) < 2 ||
		collection.Tick.PolicySnapshotID != collection.Config.Policy.SnapshotID || collection.Tick.PolicyFenceToken != collection.Config.Policy.FenceToken ||
		collection.Tick.PolicyKey != collection.Claim.Target.PolicyKey || collection.Config.TenantID != collection.Claim.Target.TenantID || !collection.Tick.Price.valid() ||
		!ids.Valid(collection.Tick.ID) || !ids.Valid(collection.Tick.PolicySnapshotID) || collection.Tick.SourceCount != len(collection.Observations) ||
		collection.Tick.Quorum != collection.Config.Policy.Quorum || collection.Tick.Quorum < 2 || collection.Tick.SourceCount < collection.Tick.Quorum ||
		collection.Config.Policy.SnapshotVersion < 1 || collection.Claim.Target.TenantID != "" ||
		collection.Tick.BaseAsset != collection.Config.Policy.BaseAsset || collection.Tick.QuoteAsset != collection.Config.Policy.QuoteAsset ||
		!collection.Tick.ExpiresAt.After(collection.Tick.ObservedAt) || collection.Tick.SpreadBPS < 0 || collection.Tick.SpreadBPS > 10000 {
		return ErrInvalidConfig
	}
	seen := make(map[string]bool, len(collection.Observations))
	prices := make([]Rational, 0, len(collection.Observations))
	for _, observation := range collection.Observations {
		if !ids.Valid(observation.ID) || !ids.Valid(observation.SourceSnapshotID) || observation.SourceFenceToken < 1 || seen[observation.SourceKey] || !observation.Price.valid() ||
			observation.TenantID != collection.Claim.Target.TenantID || observation.PolicyKey != collection.Claim.Target.PolicyKey || observation.BaseAsset != collection.Tick.BaseAsset || observation.QuoteAsset != collection.Tick.QuoteAsset {
			return ErrInvalidConfig
		}
		seen[observation.SourceKey] = true
		prices = append(prices, observation.Price)
	}
	center, err := median(prices)
	if err != nil || center.Cmp(collection.Tick.Price) != 0 {
		return ErrInvalidConfig
	}
	spread, allowed, err := spreadBPS(prices, center, collection.Config.Policy.MaxSpreadBPS)
	if err != nil || !allowed || spread != collection.Tick.SpreadBPS || canonicalSourceDigest(collection.Observations) != collection.Tick.SourcesDigest {
		return ErrInvalidConfig
	}
	return nil
}

func validErrorCode(code string) bool {
	switch code {
	case "invalid_config", "no_quorum", "stale", "future_timestamp", "divergent", "identity_disabled", "dependency_unavailable":
		return true
	}
	return false
}

var _ Store = (*PostgresStore)(nil)
