package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresRepository) CreatePaymentLink(ctx context.Context, p Principal, input PaymentLinkInput, publicURL string, tokenHash [32]byte, idem Idempotency) (result PaymentLink, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, err := s.replay(ctx, tx, p, "payment_link.create", idem, &result); err != nil || found {
			replay = found
			return err
		}
		id, err := ids.New()
		if err != nil {
			return err
		}
		now := s.now()
		metadata := input.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		_, err = tx.Exec(ctx, `INSERT INTO payment_links(id,tenant_id,merchant_id,name,public_token_hash,amount_minor,currency,currency_scale,description,allowed_routes,metadata,allowed_origin,success_url,cancel_url,max_uses,status,expires_at,created_by,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6::numeric,$7,$8,$9,$10::jsonb,$11::jsonb,NULLIF($12,''),$13,$14,$15,'active',$16,$17,$18,$18,1)`, id, p.TenantID, p.MerchantID, input.Name, tokenHash[:], input.AmountMinor, input.Currency, input.CurrencyScale, input.Description, input.AllowedRoutes, metadata, input.AllowedOrigin, input.SuccessURL, input.CancelURL, input.MaxUses, input.ExpiresAt, p.ActorID, now)
		if err != nil {
			return classifyManagement(err)
		}
		result, err = loadPaymentLink(ctx, tx, p, id)
		if err != nil {
			return err
		}
		result.PublicURL = publicURL
		if err = s.auditOutbox(ctx, tx, p, "payment_link.created", "payment_link", id, "", 1, map[string]any{"status": "active", "max_uses": input.MaxUses}); err != nil {
			return err
		}
		return s.remember(ctx, tx, p, "payment_link.create", idem, "payment_link", id, 201, result)
	})
	return result, replay, err
}

func (s *PostgresRepository) GetPaymentLink(ctx context.Context, p Principal, id string) (result PaymentLink, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error { var e error; result, e = loadPaymentLink(ctx, tx, p, id); return e })
	return
}

func (s *PostgresRepository) ListPaymentLinks(ctx context.Context, p Principal, cursor string, limit int) (page Page[PaymentLink], err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM payment_links WHERE tenant_id=$1 AND merchant_id=$2 AND ($3='' OR id<NULLIF($3,'')::uuid) ORDER BY id DESC LIMIT $4`, p.TenantID, p.MerchantID, cursor, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		var idsList []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			idsList = append(idsList, id)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(idsList) > limit {
			page.NextCursor = idsList[limit-1]
			idsList = idsList[:limit]
		}
		for _, id := range idsList {
			item, e := loadPaymentLink(ctx, tx, p, id)
			if e != nil {
				return e
			}
			page.Data = append(page.Data, item)
		}
		return nil
	})
	return
}

func (s *PostgresRepository) DisablePaymentLink(ctx context.Context, p Principal, id string, version int64, idem Idempotency) (result PaymentLink, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "payment_link.disable", idem, &result); e != nil || found {
			replay = found
			return e
		}
		now := s.now()
		command, e := tx.Exec(ctx, `UPDATE payment_links SET status='disabled',disabled_by=$1,disabled_at=$2,updated_at=$2,version=version+1 WHERE id=$3 AND tenant_id=$4 AND merchant_id=$5 AND version=$6 AND status='active'`, p.ActorID, now, id, p.TenantID, p.MerchantID, version)
		if e != nil {
			return classifyManagement(e)
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		result, e = loadPaymentLink(ctx, tx, p, id)
		if e != nil {
			return e
		}
		if e = s.auditOutbox(ctx, tx, p, "payment_link.disabled", "payment_link", id, "", result.Version, map[string]any{"previous_version": version}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "payment_link.disable", idem, "payment_link", id, 200, result)
	})
	return
}

func loadPaymentLink(ctx context.Context, tx pgx.Tx, p Principal, id string) (result PaymentLink, err error) {
	var allowed, metadata []byte
	err = tx.QueryRow(ctx, `SELECT l.id::text,l.name,l.amount_minor::text,l.currency,l.currency_scale,l.description,l.allowed_routes,l.metadata,COALESCE(l.allowed_origin,''),l.success_url,l.cancel_url,l.max_uses,l.use_count,COALESCE(stats.settled_count,0),COALESCE(stats.settled_minor,0)::text,CASE WHEN l.status='active' AND l.expires_at<=clock_timestamp() THEN 'expired' ELSE l.status END,l.expires_at,l.created_at,l.updated_at,l.version FROM payment_links l LEFT JOIN LATERAL(SELECT count(*) FILTER(WHERE i.status IN('settled','overpaid')) settled_count,COALESCE(sum(i.amount_minor) FILTER(WHERE i.status IN('settled','overpaid')),0) settled_minor FROM payment_link_redemptions r JOIN payment_intents i ON i.id=r.intent_id AND i.tenant_id=r.tenant_id WHERE r.payment_link_id=l.id AND r.tenant_id=l.tenant_id)stats ON true WHERE l.id=$1 AND l.tenant_id=$2 AND l.merchant_id=$3`, id, p.TenantID, p.MerchantID).Scan(&result.ID, &result.Name, &result.AmountMinor, &result.Currency, &result.CurrencyScale, &result.Description, &allowed, &metadata, &result.AllowedOrigin, &result.SuccessURL, &result.CancelURL, &result.MaxUses, &result.UseCount, &result.SettledCount, &result.SettledMinor, &result.Status, &result.ExpiresAt, &result.CreatedAt, &result.UpdatedAt, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	result.AllowedRoutes = append([]byte(nil), allowed...)
	result.Metadata = append([]byte(nil), metadata...)
	return
}

func (s *PostgresRepository) PublicPaymentLink(ctx context.Context, hash [32]byte) (result PublicPaymentLink, err error) {
	var tenantID, merchantID, linkID string
	err = s.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,payment_link_id::text FROM lookup_payment_link($1)`, hash[:]).Scan(&tenantID, &merchantID, &linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return
	}
	err = s.withinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var routes []byte
		e := tx.QueryRow(ctx, `SELECT name,amount_minor::text,currency,currency_scale,description,allowed_routes,expires_at FROM payment_links WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 AND status='active' AND (expires_at IS NULL OR expires_at>clock_timestamp()) AND use_count<max_uses`, linkID, tenantID, merchantID).Scan(&result.Name, &result.AmountMinor, &result.Currency, &result.CurrencyScale, &result.Description, &routes, &result.ExpiresAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		result.AllowedRoutes = append([]byte(nil), routes...)
		return e
	})
	return
}

func (s *PostgresRepository) IssueCheckout(ctx context.Context, p Principal, input CheckoutIssueInput, publicURL string, hash [32]byte, idem Idempotency) (result CheckoutIssue, replay bool, err error) {
	err = s.withinTenant(ctx, p.TenantID, func(tx pgx.Tx) error {
		if found, e := s.replay(ctx, tx, p, "checkout.issue", idem, &result); e != nil || found {
			replay = found
			return e
		}
		var intentExpiry time.Time
		var status string
		e := tx.QueryRow(ctx, `SELECT expires_at,status::text FROM payment_intents WHERE id=$1 AND tenant_id=$2 AND merchant_id=$3 FOR UPDATE`, input.IntentID, p.TenantID, p.MerchantID).Scan(&intentExpiry, &status)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if status == "settled" || status == "overpaid" || status == "cancelled" || status == "reversed" || !intentExpiry.After(s.now()) {
			return ErrConflict
		}
		expires := s.now().Add(time.Duration(input.TTLSeconds) * time.Second)
		if expires.After(intentExpiry) {
			expires = intentExpiry
		}
		nonce, e := ids.New()
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO checkout_sessions(token_hash,tenant_id,merchant_id,intent_id,expires_at,created_at,audience,allowed_origin,allowed_actions,session_nonce,version)VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,1)`, hash[:], p.TenantID, p.MerchantID, input.IntentID, expires, s.now(), input.Audience, input.AllowedOrigin, input.AllowedActions, nonce)
		if e != nil {
			return classifyManagement(e)
		}
		u, _ := url.Parse(publicURL)
		token := u.Query().Get("token")
		if token == "" {
			return ErrInvalid
		}
		result = CheckoutIssue{Token: token, URL: publicURL, ExpiresAt: expires}
		if e = s.auditOutbox(ctx, tx, p, "checkout.issued", "payment_intent", input.IntentID, "", 1, map[string]any{"audience": "hosted_checkout", "expires_at": expires}); e != nil {
			return e
		}
		return s.remember(ctx, tx, p, "checkout.issue", idem, "checkout_session", input.IntentID, 201, result)
	})
	return
}

func (s *PostgresRepository) PublicCheckout(ctx context.Context, hash [32]byte, origin string) (result CheckoutSession, err error) {
	var tenantID, merchantID, intentID string
	err = s.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,intent_id::text FROM lookup_checkout_session($1)`, hash[:]).Scan(&tenantID, &merchantID, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return
	}
	err = s.withinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		result, e = loadPublicCheckout(ctx, tx, hash, tenantID, merchantID, intentID, origin, s.now())
		return e
	})
	return
}

func (s *PostgresRepository) SelectCheckoutRoute(ctx context.Context, hash [32]byte, origin, routeID string, idem Idempotency) (result CheckoutSession, replay bool, err error) {
	var tenantID, merchantID, intentID string
	err = s.pool.QueryRow(ctx, `SELECT tenant_id::text,merchant_id::text,intent_id::text FROM lookup_checkout_session($1)`, hash[:]).Scan(&tenantID, &merchantID, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, ErrNotFound
	}
	if err != nil {
		return
	}
	err = s.withinTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var selected, storedKey, audience, allowedOrigin string
		var storedHash []byte
		var actions []string
		e := tx.QueryRow(ctx, `SELECT COALESCE(selected_route_id::text,''),COALESCE(selection_idempotency_key,''),selection_request_hash,audience,COALESCE(allowed_origin,''),allowed_actions FROM checkout_sessions WHERE token_hash=$1 AND tenant_id=$2 AND merchant_id=$3 AND intent_id=$4 AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR UPDATE`, hash[:], tenantID, merchantID, intentID).Scan(&selected, &storedKey, &storedHash, &audience, &allowedOrigin, &actions)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if !checkoutOriginAllowed(audience, allowedOrigin, origin) || !contains(actions, "select_route") {
			return ErrNotFound
		}
		if storedKey != "" {
			if storedKey != idem.Key || !bytes.Equal(storedHash, idem.Fingerprint[:]) || selected != routeID {
				return ErrIdempotencyConflict
			}
			replay = true
			result, e = loadPublicCheckout(ctx, tx, hash, tenantID, merchantID, intentID, origin, s.now())
			return e
		}
		var active bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_routes WHERE id=$1 AND intent_id=$2 AND tenant_id=$3 AND merchant_id=$4 AND status='active' AND expires_at>clock_timestamp())`, routeID, intentID, tenantID, merchantID).Scan(&active); e != nil {
			return e
		}
		if !active {
			return ErrNotFound
		}
		command, e := tx.Exec(ctx, `UPDATE checkout_sessions SET selected_route_id=$1,selection_idempotency_key=$2,selection_request_hash=$3,version=version+1 WHERE token_hash=$4 AND selected_route_id IS NULL`, routeID, idem.Key, idem.Fingerprint[:], hash[:])
		if e != nil {
			return e
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		result, e = loadPublicCheckout(ctx, tx, hash, tenantID, merchantID, intentID, origin, s.now())
		if e != nil {
			return e
		}
		return insertPublicCheckoutEvent(ctx, tx, tenantID, merchantID, intentID, routeID, result.Version, s.now())
	})
	return
}

func loadPublicCheckout(ctx context.Context, tx pgx.Tx, hash [32]byte, tenantID, merchantID, intentID, origin string, now time.Time) (result CheckoutSession, err error) {
	if _, err = tx.Exec(ctx, `SELECT set_config('app.merchant_id',$1,true)`, merchantID); err != nil {
		return result, err
	}
	var status, audience, allowedOrigin string
	var actions []string
	var sessionExpiry, intentExpiry time.Time
	err = tx.QueryRow(ctx, `SELECT i.merchant_order_id,m.display_name,i.amount_minor::text,i.currency,i.currency_scale,i.description,i.status::text,i.expires_at,cs.expires_at,COALESCE(cs.selected_route_id::text,''),cs.audience,COALESCE(cs.allowed_origin,''),cs.allowed_actions,GREATEST(i.version,cs.version) FROM checkout_sessions cs JOIN payment_intents i ON i.id=cs.intent_id AND i.tenant_id=cs.tenant_id JOIN merchants m ON m.id=cs.merchant_id AND m.tenant_id=cs.tenant_id WHERE cs.token_hash=$1 AND cs.tenant_id=$2 AND cs.merchant_id=$3 AND cs.intent_id=$4 AND cs.revoked_at IS NULL AND cs.expires_at>clock_timestamp()`, hash[:], tenantID, merchantID, intentID).Scan(&result.OrderID, &result.MerchantName, &result.AmountMinor, &result.Currency, &result.CurrencyScale, &result.Description, &status, &intentExpiry, &sessionExpiry, &result.SelectedRouteID, &audience, &allowedOrigin, &actions, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return
	}
	if !checkoutOriginAllowed(audience, allowedOrigin, origin) || !contains(actions, "read") {
		return result, ErrNotFound
	}
	result.IntentID = intentID
	result.ExpiresAt = sessionExpiry
	if intentExpiry.Before(result.ExpiresAt) {
		result.ExpiresAt = intentExpiry
	}
	result.Status = publicCheckoutStatus(status, intentExpiry, now)
	var hostedJobState string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM hosted_payment_link_jobs WHERE tenant_id=$1 AND merchant_id=$2 AND intent_id=$3 ORDER BY created_at DESC LIMIT 1),'')`, tenantID, merchantID, intentID).Scan(&hostedJobState); err != nil {
		return result, err
	}
	rows, e := tx.Query(ctx, `SELECT
 r.id::text,r.provider,COALESCE(r.provider_id,''),COALESCE(r.chain_id,''),r.asset_id,r.display_amount,
 COALESCE(r.receiving_address,''),COALESCE(r.payment_url,''),COALESCE(hpc.payment_url_origins,'{}'::text[]),
 COALESCE(te.transaction_id,''),CASE WHEN te.transaction_id IS NULL THEN '' ELSE replace(c.transaction_url_template,'{tx}',te.transaction_id) END,
 r.expected_amount_atomic::text,r.asset_decimals,COALESCE(pma.received_atomic,0)::text,COALESCE(progress.payment_count,0),
 COALESCE((binding.policy_snapshot->>'accumulate_partials')::boolean,false),
 CASE WHEN COALESCE((binding.policy_snapshot->>'accept_late_within_grace')::boolean,false) THEN r.grace_ends_at ELSE r.expires_at END
 FROM payment_routes r
 LEFT JOIN chains c ON c.id=r.chain_id
 LEFT JOIN hosted_provider_configs hpc ON hpc.id=r.provider_id AND hpc.tenant_id=r.tenant_id AND hpc.merchant_id=r.merchant_id
 LEFT JOIN payment_route_policy_bindings binding ON binding.route_id=r.id AND binding.tenant_id=r.tenant_id
 LEFT JOIN payment_match_aggregates pma ON pma.route_id=r.id AND pma.tenant_id=r.tenant_id AND pma.state<>'reversed'
 LEFT JOIN LATERAL(SELECT count(*)::bigint AS payment_count FROM payment_matches matched WHERE matched.tenant_id=r.tenant_id AND matched.aggregate_id=pma.id AND matched.allocation_role='payment' AND matched.state<>'reversed')progress ON true
 LEFT JOIN LATERAL(SELECT event_id FROM payment_matches WHERE tenant_id=r.tenant_id AND route_id=r.id AND state<>'reversed' AND event_id IS NOT NULL ORDER BY finalized_at DESC NULLS LAST,id DESC LIMIT 1)pm ON true
 LEFT JOIN transfer_events te ON te.id=pm.event_id
 WHERE r.intent_id=$1 AND r.tenant_id=$2 AND r.merchant_id=$3 AND r.status IN('active','settled','expired')
 ORDER BY r.created_at,r.id`, intentID, tenantID, merchantID)
	if e != nil {
		return result, e
	}
	defer rows.Close()
	for rows.Next() {
		var route CheckoutRoute
		var admittedOrigins []string
		var expectedAtomic, receivedAtomic string
		var assetDecimals int16
		var accumulatesPartials bool
		var collectionDeadline time.Time
		if e = rows.Scan(&route.ID, &route.Provider, &route.ProviderID, &route.Network, &route.Asset, &route.Amount, &route.Address, &route.PaymentURL, &admittedOrigins, &route.TransactionHash, &route.ExplorerURL, &expectedAtomic, &assetDecimals, &receivedAtomic, &route.PaymentCount, &accumulatesPartials, &collectionDeadline); e != nil {
			return result, e
		}
		var amountRemaining bool
		route.ReceivedAmount, route.RemainingAmount, amountRemaining = checkoutPaymentProgress(expectedAtomic, receivedAtomic, assetDecimals)
		if route.ReceivedAmount != "" {
			topUpAllowed := result.Status == "partially_paid" && route.Provider == "on_chain" && accumulatesPartials && amountRemaining && collectionDeadline.After(now)
			route.TopUpAllowed = &topUpAllowed
		}
		if route.Provider == "hosted_gateway" && !admittedCheckoutPaymentURL(route.PaymentURL, admittedOrigins) {
			return result, errors.New("hosted checkout payment URL is not admitted")
		}
		if !strings.HasPrefix(route.ExplorerURL, "https://") {
			route.ExplorerURL = ""
		}
		result.Routes = append(result.Routes, route)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	switch hostedJobState {
	case "preparing":
		if len(result.Routes) != 0 || result.SelectedRouteID != "" {
			return result, errors.New("hosted payment-link preparation has an unexpected route")
		}
		result.Status = "preparing_payment_route"
	case "terminal":
		if len(result.Routes) != 0 || result.SelectedRouteID != "" {
			return result, errors.New("terminal hosted payment-link preparation has an unexpected route")
		}
		result.Status = "payment_route_failed"
	case "bound":
		if len(result.Routes) != 1 || result.SelectedRouteID == "" || result.SelectedRouteID != result.Routes[0].ID {
			return result, errors.New("bound hosted payment-link preparation is missing its selected route")
		}
	case "":
	default:
		return result, errors.New("invalid hosted payment-link preparation state")
	}
	return result, nil
}

func admittedCheckoutPaymentURL(value string, origins []string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	for _, candidate := range origins {
		if candidate == origin {
			return true
		}
	}
	return false
}

func insertPublicCheckoutEvent(ctx context.Context, tx pgx.Tx, tenantID, merchantID, intentID, routeID string, version int64, now time.Time) error {
	id, e := ids.New()
	if e != nil {
		return e
	}
	payload, _ := json.Marshal(map[string]any{"event_id": id, "event_type": "checkout.route_selected", "intent_id": intentID, "route_id": routeID, "occurred_at": now})
	_, e = tx.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,merchant_id,aggregate_type,aggregate_id,aggregate_version,aggregate_sequence,event_type,schema_version,payload,occurred_at,recorded_at,available_at)VALUES($1,$2,$3,'checkout_session',$4,$5,$5,'checkout.route_selected','1',$6::jsonb,$7,$7,$7)`, id, tenantID, merchantID, intentID, version, payload, now)
	return classifyManagement(e)
}
func publicCheckoutStatus(status string, expires, now time.Time) string {
	if !expires.After(now) && status != "settled" && status != "overpaid" && status != "needs_review" && status != "reorg_review" {
		return "expired"
	}
	switch status {
	case "settled", "overpaid":
		return "settled"
	case "partially_paid":
		return "partially_paid"
	case "observed":
		return "detected"
	case "confirmed":
		return "confirming"
	case "needs_review", "reorg_review":
		return "needs_review"
	case "expired", "cancelled", "reversed":
		return "expired"
	default:
		return "pending"
	}
}

func checkoutPaymentProgress(expectedRaw, receivedRaw string, decimals int16) (received, remaining string, amountRemaining bool) {
	if decimals < 0 {
		return "", "", false
	}
	expected, expectedOK := new(big.Int).SetString(expectedRaw, 10)
	receivedAtomic, receivedOK := new(big.Int).SetString(receivedRaw, 10)
	if !expectedOK || !receivedOK || expected.Sign() <= 0 || receivedAtomic.Sign() <= 0 {
		return "", "", false
	}
	remainder := new(big.Int).Sub(new(big.Int).Set(expected), receivedAtomic)
	if remainder.Sign() < 0 {
		remainder.SetInt64(0)
	}
	return formatAtomicCheckoutAmount(receivedAtomic, int(decimals)), formatAtomicCheckoutAmount(remainder, int(decimals)), remainder.Sign() > 0
}

func formatAtomicCheckoutAmount(value *big.Int, decimals int) string {
	digits := value.String()
	if decimals == 0 {
		return digits
	}
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}
	whole := digits[:len(digits)-decimals]
	fraction := strings.TrimRight(digits[len(digits)-decimals:], "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func checkoutOriginAllowed(audience, allowed, provided string) bool {
	if audience == "embedded_checkout" {
		return provided != "" && provided == allowed
	}
	return provided == "" || provided == allowed
}
