package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/checkout"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db  *Database
	now func() time.Time
}

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	db, err := NewDatabase(pool)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

type idempotencyRecord struct {
	RequestHash  []byte
	ResourceID   string
	ResponseBody []byte
}

func (s *Store) CreateIntent(ctx context.Context, cmd application.CreateIntent, requestHash string) (intent domain.PaymentIntent, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_intent", cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_intent", cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.ResponseBody, &intent); err != nil {
				return fmt.Errorf("decode idempotent intent: %w", err)
			}
			replay = true
			return nil
		}
		intentID, err := ids.New()
		if err != nil {
			return err
		}
		eventID, err := ids.New()
		if err != nil {
			return err
		}
		checkoutToken, checkoutHash, err := checkout.NewToken()
		if err != nil {
			return err
		}
		now := s.now()
		sessionNonce, err := ids.New()
		if err != nil {
			return err
		}
		intent, err = s.CreateIntentInTx(ctx, tx, cmd, IntentInsertIDs{IntentID: intentID, EventID: eventID, Now: now, Checkout: CheckoutSessionInsert{Token: checkoutToken, TokenHash: checkoutHash, Audience: "legacy_hosted_checkout", AllowedActions: []string{"read"}, SessionNonce: sessionNonce}})
		if err != nil {
			return err
		}
		body, _ := json.Marshal(intent)
		return insertIdempotency(ctx, tx, cmd.Principal, "create_intent", cmd.IdempotencyKey, requestHash, "payment_intent", intentID, httpCreated, body, now.Add(30*24*time.Hour))
	})
	return intent, replay, err
}

func (s *Store) GetIntent(ctx context.Context, principal application.Principal, id string) (intent domain.PaymentIntent, err error) {
	err = s.db.WithinTenant(ctx, principal.TenantID, func(tx pgx.Tx) error {
		var err error
		intent, err = getIntent(ctx, tx, principal, id, false)
		return err
	})
	return intent, err
}

// GetCheckoutSession performs the only pre-tenant lookup through a narrowly
// scoped SECURITY DEFINER function. The opaque token hash resolves a single
// tenant/merchant/intent tuple; every subsequent read executes under that
// tenant's RLS context and returns display-safe fields only.
func (s *Store) GetCheckoutSession(ctx context.Context, tokenHash [32]byte) (session domain.CheckoutSession, err error) {
	var tenantID, merchantID, intentID string
	err = s.db.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,intent_id::text FROM lookup_checkout_session($1)`, tokenHash[:]).Scan(&tenantID, &merchantID, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return session, domain.ErrNotFound
	}
	if err != nil {
		return session, err
	}
	err = s.db.WithinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		intent, err := getIntent(ctx, tx, application.Principal{TenantID: tenantID, MerchantID: merchantID}, intentID, false)
		if err != nil {
			return err
		}
		session = domain.CheckoutSession{
			IntentID:  intent.ID,
			OrderID:   intent.MerchantOrderID,
			Status:    domain.CheckoutStatusForIntent(intent.Status, intent.ExpiresAt, s.now()),
			ExpiresAt: intent.ExpiresAt,
			Routes:    make([]domain.CheckoutRoute, 0, len(intent.Routes)),
			Version:   intent.Version,
		}
		rows, err := tx.Query(ctx, `SELECT r.id::text,r.provider,COALESCE(r.provider_id,''),COALESCE(r.chain_id,''),r.asset_id,r.display_amount,COALESCE(r.receiving_address,''),COALESCE(r.payment_url,''),
COALESCE(m.transaction_id,''),
CASE WHEN COALESCE(m.transaction_id,'')='' THEN '' ELSE replace(c.transaction_url_template,'{tx}',m.transaction_id) END,
EXISTS(SELECT 1 FROM payment_matches selected WHERE selected.tenant_id=r.tenant_id AND selected.route_id=r.id AND selected.state<>'reversed'),r.version
FROM payment_routes r
LEFT JOIN chains c ON c.id=r.chain_id
LEFT JOIN LATERAL (
    SELECT te.transaction_id,true AS has_match
      FROM payment_matches pm
      JOIN transfer_events te ON te.id=pm.event_id
     WHERE pm.tenant_id=r.tenant_id AND pm.route_id=r.id AND pm.state <> 'reversed'
     ORDER BY (pm.state='finalized') DESC,pm.created_at DESC,pm.id DESC
     LIMIT 1
) m ON true
WHERE r.tenant_id=$1 AND r.intent_id=$2
ORDER BY r.created_at,r.id`, tenantID, intentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var route domain.CheckoutRoute
			var matched bool
			var version int64
			if err := rows.Scan(&route.ID, &route.Provider, &route.ProviderID, &route.Network, &route.Asset, &route.Amount, &route.Address, &route.PaymentURL, &route.TransactionHash, &route.ExplorerURL, &matched, &version); err != nil {
				return err
			}
			// Templates are provisioned by operators and constrained to HTTPS in
			// SQL. Do not emit a malformed or unexpanded explorer link.
			if route.ExplorerURL != "" && (!strings.HasPrefix(route.ExplorerURL, "https://") || strings.Contains(route.ExplorerURL, "{tx}")) {
				route.ExplorerURL = ""
			}
			if matched && session.SelectedRouteID == "" {
				session.SelectedRouteID = route.ID
			}
			if version > session.Version {
				session.Version = version
			}
			session.Routes = append(session.Routes, route)
		}
		return rows.Err()
	})
	return session, err
}

func (s *Store) ListIntents(ctx context.Context, p application.Principal, status, after string, limit int) (items []domain.PaymentIntent, err error) {
	err = s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM payment_intents WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR status::text=$3) AND ($4='' OR id<$4::uuid) ORDER BY id DESC LIMIT $5`, p.TenantID, p.MerchantID, status, after, limit)
		if err != nil {
			return err
		}
		var idsList []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			idsList = append(idsList, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range idsList {
			intent, err := getIntent(ctx, tx, p, id, false)
			if err != nil {
				return err
			}
			items = append(items, intent)
		}
		return nil
	})
	return items, err
}

func (s *Store) FindRouteReplay(ctx context.Context, p application.Principal, key, requestHash string) (route domain.PaymentRoute, found bool, err error) {
	err = s.db.WithinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		record, exists, err := findIdempotency(ctx, tx, p.MerchantID, "create_route", key)
		if err != nil || !exists {
			return err
		}
		if !hashMatches(record.RequestHash, requestHash) {
			return domain.ErrIdempotencyConflict
		}
		if err := json.Unmarshal(record.ResponseBody, &route); err != nil {
			return fmt.Errorf("decode idempotent route: %w", err)
		}
		found = true
		return nil
	})
	return route, found, err
}

func (s *Store) CreateRoute(ctx context.Context, cmd application.CreateRoute, requestHash string) (route domain.PaymentRoute, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := setMerchantContext(ctx, tx, cmd.Principal.MerchantID); err != nil {
			return err
		}
		// Serialize equal merchant/key pairs before inspecting their record. The
		// second concurrent request replays the committed route instead of racing
		// exact-amount and address reservations.
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_route", cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, "create_route", cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.ResponseBody, &route); err != nil {
				return err
			}
			replay = true
			return nil
		}
		route, err = s.CreateRouteInTx(ctx, tx, cmd)
		if err != nil {
			return err
		}
		now := s.now()
		body, _ := json.Marshal(route)
		return insertIdempotency(ctx, tx, cmd.Principal, "create_route", cmd.IdempotencyKey, requestHash, "payment_route", route.ID, httpCreated, body, now.Add(30*24*time.Hour))
	})
	return route, replay, err
}

func (s *Store) CancelIntent(ctx context.Context, cmd application.CancelIntent, requestHash string) (intent domain.PaymentIntent, replay bool, err error) {
	err = s.db.WithinTenant(ctx, cmd.Principal.TenantID, func(tx pgx.Tx) error {
		if err := setCorrelation(ctx, tx, cmd.CorrelationID); err != nil {
			return err
		}
		if err := lockIdempotency(ctx, tx, cmd.Principal.MerchantID, "cancel_intent", cmd.IdempotencyKey); err != nil {
			return err
		}
		record, found, err := findIdempotency(ctx, tx, cmd.Principal.MerchantID, "cancel_intent", cmd.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if !hashMatches(record.RequestHash, requestHash) {
				return domain.ErrIdempotencyConflict
			}
			if err := json.Unmarshal(record.ResponseBody, &intent); err != nil {
				return err
			}
			replay = true
			return nil
		}
		intent, err = getIntent(ctx, tx, cmd.Principal, cmd.IntentID, true)
		if err != nil {
			return err
		}
		if cmd.ExpectedVersion > 0 && cmd.ExpectedVersion != intent.Version {
			return domain.ErrVersionConflict
		}
		now := s.now()
		if err := intent.Transition(domain.IntentCancelled, cmd.Reason, now); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE payment_intents SET status='cancelled',status_reason=$1,version=$2,updated_at=$3,cancelled_at=$3 WHERE id=$4 AND tenant_id=$5 AND version=$6`, cmd.Reason, intent.Version, now, intent.ID, cmd.Principal.TenantID, intent.Version-1)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		_, err = tx.Exec(ctx, `UPDATE payment_routes SET status='cancelled',version=version+1,updated_at=$1 WHERE intent_id=$2 AND tenant_id=$3 AND status='active'`, now, intent.ID, cmd.Principal.TenantID)
		if err != nil {
			return err
		}
		// Preserve exact-amount collision protection through route grace. Late
		// transfers after cancellation remain observable and reviewable.
		if _, err = tx.Exec(ctx, `UPDATE checkout_sessions SET revoked_at=COALESCE(revoked_at,$1),version=version+1 WHERE tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND revoked_at IS NULL`, now, cmd.Principal.TenantID, cmd.Principal.MerchantID, intent.ID); err != nil {
			return err
		}
		for i := range intent.Routes {
			if intent.Routes[i].Status == domain.RouteActive {
				intent.Routes[i].Status = domain.RouteCancelled
				intent.Routes[i].Version++
			}
		}
		if err := insertPaymentStateCallback(ctx, tx, cmd.Principal, intent, "payment.cancelled", cmd.CorrelationID, now); err != nil {
			return err
		}
		body, _ := json.Marshal(intent)
		return insertIdempotency(ctx, tx, cmd.Principal, "cancel_intent", cmd.IdempotencyKey, requestHash, "payment_intent", intent.ID, httpOK, body, now.Add(30*24*time.Hour))
	})
	return intent, replay, err
}

func (s *Store) ListAssets(ctx context.Context, _ application.Principal) ([]domain.Asset, error) {
	rows, err := s.db.pool.Query(ctx, `SELECT id,chain_id,symbol,name,kind,canonical_contract,decimals,status,minimum_deposit::text FROM assets WHERE status='active' ORDER BY chain_id,symbol,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Asset
	for rows.Next() {
		var a domain.Asset
		if err := rows.Scan(&a.ID, &a.ChainID, &a.Symbol, &a.Name, &a.Kind, &a.Contract, &a.Decimals, &a.Status, &a.MinimumDeposit); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func getIntent(ctx context.Context, tx pgx.Tx, p application.Principal, id string, lock bool) (domain.PaymentIntent, error) {
	query := `SELECT id::text,tenant_id::text,merchant_id::text,merchant_order_id,COALESCE(customer_reference,''),amount_minor::text,currency,currency_scale,description,metadata::text,allowed_routes::text,status::text,status_reason,version,created_at,updated_at,expires_at,settled_at,cancelled_at FROM payment_intents WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3`
	if lock {
		query += " FOR UPDATE"
	}
	var i domain.PaymentIntent
	var amount, metadata, allowed, status string
	err := tx.QueryRow(ctx, query, id, p.TenantID, p.MerchantID).Scan(&i.ID, &i.TenantID, &i.MerchantID, &i.MerchantOrderID, &i.CustomerReference, &amount, &i.Currency, &i.CurrencyScale, &i.Description, &metadata, &allowed, &status, &i.StatusReason, &i.Version, &i.CreatedAt, &i.UpdatedAt, &i.ExpiresAt, &i.SettledAt, &i.CancelledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return i, domain.ErrNotFound
	}
	if err != nil {
		return i, err
	}
	i.AmountMinor, err = money.Parse(amount)
	if err != nil {
		return i, err
	}
	i.Metadata = []byte(metadata)
	if err = json.Unmarshal([]byte(allowed), &i.AllowedRoutes); err != nil {
		return i, err
	}
	i.Status = domain.IntentStatus(status)
	i.Routes, err = getRoutes(ctx, tx, i.TenantID, i.ID)
	return i, err
}
func getRoutes(ctx context.Context, tx pgx.Tx, tenantID, intentID string) ([]domain.PaymentRoute, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.intent_id::text,COALESCE(r.quote_id::text,''),COALESCE(r.address_assignment_id::text,''),COALESCE(r.chain_id,''),r.asset_id,r.provider,COALESCE(r.provider_id,''),COALESCE(r.provider_order_id::text,''),COALESCE(r.provider_reference,''),COALESCE(r.payment_url,''),r.expected_amount_atomic::text,r.asset_decimals,r.display_amount,COALESCE(r.receiving_address,''),COALESCE(r.memo,''),r.required_finality,r.status::text,r.version,r.starts_at,r.expires_at,r.grace_ends_at,COALESCE(progress.received_atomic,0)::text,COALESCE(progress.payment_count,0)
FROM payment_routes r
LEFT JOIN LATERAL(SELECT count(*)::bigint AS payment_count,COALESCE(sum(matched.received_atomic),0) AS received_atomic FROM payment_matches matched WHERE matched.tenant_id=r.tenant_id AND matched.route_id=r.id AND matched.state<>'reversed' AND matched.allocation_role='payment')progress ON true
WHERE r.tenant_id=$1 AND r.intent_id=$2 ORDER BY r.created_at,r.id`, tenantID, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.PaymentRoute{}
	for rows.Next() {
		var r domain.PaymentRoute
		var amount, received, status string
		if err := rows.Scan(&r.ID, &r.IntentID, &r.QuoteID, &r.AddressAssignmentID, &r.ChainID, &r.AssetID, &r.Provider, &r.ProviderID, &r.ProviderOrderID, &r.ProviderReference, &r.PaymentURL, &amount, &r.AssetDecimals, &r.DisplayAmount, &r.Address, &r.Memo, &r.RequiredFinality, &status, &r.Version, &r.StartsAt, &r.ExpiresAt, &r.GraceEndsAt, &received, &r.PaymentCount); err != nil {
			return nil, err
		}
		r.ExpectedAmount, err = money.Parse(amount)
		if err != nil {
			return nil, err
		}
		r.Status = domain.RouteStatus(status)
		r.ReceivedAmount, r.RemainingAmount, r.ExcessAmount = paymentProgress(amount, received, r.AssetDecimals)
		result = append(result, r)
	}
	return result, rows.Err()
}

func paymentProgress(expectedRaw, receivedRaw string, decimals uint8) (received, remaining, excess string) {
	expected, expectedOK := new(big.Int).SetString(expectedRaw, 10)
	actual, actualOK := new(big.Int).SetString(receivedRaw, 10)
	if !expectedOK || !actualOK || expected.Sign() <= 0 || actual.Sign() <= 0 {
		return "", "", ""
	}
	delta := new(big.Int).Sub(new(big.Int).Set(expected), actual)
	if delta.Sign() >= 0 {
		return formatAtomicProgress(actual, decimals), formatAtomicProgress(delta, decimals), ""
	}
	delta.Neg(delta)
	return formatAtomicProgress(actual, decimals), "0", formatAtomicProgress(delta, decimals)
}

func formatAtomicProgress(value *big.Int, decimals uint8) string {
	digits := value.String()
	if decimals == 0 {
		return digits
	}
	if len(digits) <= int(decimals) {
		digits = strings.Repeat("0", int(decimals)-len(digits)+1) + digits
	}
	cut := len(digits) - int(decimals)
	fraction := strings.TrimRight(digits[cut:], "0")
	if fraction == "" {
		return digits[:cut]
	}
	return digits[:cut] + "." + fraction
}

func findIdempotency(ctx context.Context, tx pgx.Tx, merchantID, operation, key string) (idempotencyRecord, bool, error) {
	var r idempotencyRecord
	err := tx.QueryRow(ctx, `SELECT request_hash,resource_id::text,response_body::text FROM idempotency_records WHERE merchant_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, merchantID, operation, key).Scan(&r.RequestHash, &r.ResourceID, &r.ResponseBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}
func lockIdempotency(ctx context.Context, tx pgx.Tx, merchantID, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, merchantID+"\x1f"+operation+"\x1f"+key)
	return err
}
func insertIdempotency(ctx context.Context, tx pgx.Tx, p application.Principal, operation, key, hash, resourceType, resourceID string, status int, body []byte, expires time.Time) error {
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("invalid request hash")
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records (tenant_id,merchant_id,operation,idempotency_key,request_hash,resource_type,resource_id,response_status,response_body,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,clock_timestamp())`, p.TenantID, p.MerchantID, operation, key, decoded, resourceType, resourceID, status, body, expires)
	return classify(err)
}
func insertOutbox(ctx context.Context, tx pgx.Tx, eventID string, p application.Principal, aggregateID string, version int64, eventType, correlation string, payload []byte, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO outbox_events (id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,correlation_id,occurred_at,recorded_at,available_at) VALUES ($1,$2,$3,'payment_intent',$4,$5,$5,$6,'1',$7::jsonb,NULLIF($8,''),$9,$9,$9)`, eventID, p.TenantID, p.MerchantID, aggregateID, version, eventType, payload, correlation, now)
	return err
}
func hashMatches(stored []byte, provided string) bool {
	decoded, err := hex.DecodeString(provided)
	return err == nil && bytes.Equal(stored, decoded)
}
func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" || pgErr.Code == "23P01" || pgErr.Code == "23514" {
			return fmt.Errorf("%w: %s", domain.ErrStateConflict, pgErr.ConstraintName)
		}
	}
	return err
}

const (
	httpOK       = 200
	httpCreated  = 201
	httpAccepted = 202
)

var _ application.Store = (*Store)(nil)
