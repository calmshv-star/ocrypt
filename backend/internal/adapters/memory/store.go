package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/chains"
	"github.com/calmshv-star/ocrypt/backend/internal/checkout"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type idempotencyRecord struct {
	Hash string
	Kind string
	ID   string
}

// Store is deterministic and race-safe. It is used by tests and local demos;
// production deployments use the PostgreSQL schema and identical transaction boundaries.
type Store struct {
	mu               sync.RWMutex
	intents          map[string]domain.PaymentIntent
	merchantOrders   map[string]string
	idempotency      map[string]idempotencyRecord
	reservations     map[string]string
	reservationGrace map[string]time.Time
	outbox           []domain.EventEnvelope
	assets           []domain.Asset
	proofs           map[string]domain.PaymentProof
	checkoutByHash   map[[32]byte]string
	checkoutTokens   map[string]string
	reports          map[string]domain.ReconciliationReport
	now              func() time.Time
}

func New() *Store {
	return &Store{
		intents: make(map[string]domain.PaymentIntent), merchantOrders: make(map[string]string),
		idempotency: make(map[string]idempotencyRecord), reservations: make(map[string]string), reservationGrace: make(map[string]time.Time),
		proofs:         make(map[string]domain.PaymentProof),
		checkoutByHash: make(map[[32]byte]string), checkoutTokens: make(map[string]string),
		reports: make(map[string]domain.ReconciliationReport),
		now:     func() time.Time { return time.Now().UTC() },
		assets: []domain.Asset{
			{ID: "usdt-tron", ChainID: "tron:mainnet", Symbol: "USDT", Name: "Tether USD", Kind: "fungible_token", Decimals: 6, Status: "active", MinimumDeposit: "1000000"},
			{ID: "usdc-ethereum", ChainID: "eip155:1", Symbol: "USDC", Name: "USD Coin", Kind: "fungible_token", Decimals: 6, Status: "active", MinimumDeposit: "1000000"},
		},
	}
}

func (s *Store) ExpireIntent(_ context.Context, cmd application.ExpireIntent, requestHash string) (domain.PaymentIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "expire_intent", cmd.IdempotencyKey)
	if previous, ok := s.idempotency[key]; ok {
		if previous.Hash != requestHash {
			return domain.PaymentIntent{}, false, domain.ErrIdempotencyConflict
		}
		return cloneIntent(s.intents[previous.ID]), true, nil
	}
	intent, ok := s.intents[cmd.IntentID]
	if !ok || intent.TenantID != cmd.Principal.TenantID || intent.MerchantID != cmd.Principal.MerchantID {
		return domain.PaymentIntent{}, false, domain.ErrNotFound
	}
	if intent.Version != cmd.ExpectedVersion {
		return domain.PaymentIntent{}, false, domain.ErrVersionConflict
	}
	now := s.now()
	if err := intent.Transition(domain.IntentExpired, cmd.Reason, now); err != nil {
		return domain.PaymentIntent{}, false, err
	}
	intent.ExpiresAt = now
	for index := range intent.Routes {
		if intent.Routes[index].Status == domain.RouteActive {
			intent.Routes[index].Status = domain.RouteExpired
			intent.Routes[index].ExpiresAt = now
			intent.Routes[index].Version++
		}
	}
	s.intents[intent.ID] = intent
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "intent", ID: intent.ID}
	return cloneIntent(intent), false, nil
}

func (s *Store) UpdateIntentMetadata(_ context.Context, cmd application.UpdateIntentMetadata, requestHash string) (domain.PaymentIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "update_intent_metadata", cmd.IdempotencyKey)
	if previous, ok := s.idempotency[key]; ok {
		if previous.Hash != requestHash {
			return domain.PaymentIntent{}, false, domain.ErrIdempotencyConflict
		}
		return cloneIntent(s.intents[previous.ID]), true, nil
	}
	intent, ok := s.intents[cmd.IntentID]
	if !ok || intent.TenantID != cmd.Principal.TenantID || intent.MerchantID != cmd.Principal.MerchantID {
		return domain.PaymentIntent{}, false, domain.ErrNotFound
	}
	if intent.Version != cmd.ExpectedVersion || intent.Status == domain.IntentReversed {
		return domain.PaymentIntent{}, false, domain.ErrVersionConflict
	}
	intent.Metadata = append(json.RawMessage(nil), cmd.Metadata...)
	intent.Version++
	intent.UpdatedAt = s.now()
	s.intents[intent.ID] = intent
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "intent", ID: intent.ID}
	return cloneIntent(intent), false, nil
}

func (s *Store) CreatePaymentProof(_ context.Context, cmd application.SubmitPaymentProof, requestHash string) (domain.PaymentProof, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "create_payment_proof", cmd.IdempotencyKey)
	if old, ok := s.idempotency[key]; ok {
		if old.Hash != requestHash {
			return domain.PaymentProof{}, false, domain.ErrIdempotencyConflict
		}
		return s.proofs[old.ID], true, nil
	}
	intent, ok := s.intents[cmd.PaymentIntentID]
	if !ok || intent.TenantID != cmd.Principal.TenantID || intent.MerchantID != cmd.Principal.MerchantID {
		return domain.PaymentProof{}, false, domain.ErrNotFound
	}
	id, err := ids.New()
	if err != nil {
		return domain.PaymentProof{}, false, err
	}
	eventID, err := ids.New()
	if err != nil {
		return domain.PaymentProof{}, false, err
	}
	now := s.now()
	proof := domain.PaymentProof{ID: id, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID, PaymentIntentID: cmd.PaymentIntentID, ChainID: cmd.ChainID, TransactionID: cmd.TransactionID, Status: domain.ProofQueued, TransferEventIDs: []string{}, CreatedAt: now, UpdatedAt: now, Version: 1}
	s.proofs[id] = proof
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "payment_proof", ID: id}
	payload, _ := json.Marshal(map[string]any{"payment_proof_id": id, "payment_intent_id": cmd.PaymentIntentID, "chain_id": cmd.ChainID, "transaction_id": cmd.TransactionID})
	s.outbox = append(s.outbox, domain.EventEnvelope{ID: eventID, Type: "payment.proof.submitted", SchemaVersion: "1", AggregateID: id, AggregateType: "payment_proof", AggregateVersion: 1, Sequence: 1, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID, CorrelationID: cmd.CorrelationID, OccurredAt: now, RecordedAt: now, Payload: payload})
	return proof, false, nil
}
func (s *Store) GetPaymentProof(_ context.Context, p application.Principal, id string) (domain.PaymentProof, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	proof, ok := s.proofs[id]
	if !ok || proof.TenantID != p.TenantID || proof.MerchantID != p.MerchantID {
		return domain.PaymentProof{}, domain.ErrNotFound
	}
	proof.TransferEventIDs = append([]string(nil), proof.TransferEventIDs...)
	return proof, nil
}

func (s *Store) CreateIntent(_ context.Context, cmd application.CreateIntent, requestHash string) (domain.PaymentIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "create_intent", cmd.IdempotencyKey)
	if old, ok := s.idempotency[key]; ok {
		if old.Hash != requestHash {
			return domain.PaymentIntent{}, false, domain.ErrIdempotencyConflict
		}
		replayed := cloneIntent(s.intents[old.ID])
		replayed.CheckoutToken = s.checkoutTokens[old.ID]
		return replayed, true, nil
	}
	orderKey := scopeKey(cmd.Principal, "merchant_order", cmd.MerchantOrderID)
	if _, exists := s.merchantOrders[orderKey]; exists {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: merchant_order_id already exists", domain.ErrStateConflict)
	}
	id, err := ids.New()
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	checkoutToken, checkoutHash, err := checkout.NewToken()
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	eventID, err := ids.New()
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	now := s.now()
	p := domain.PaymentIntent{ID: id, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID,
		MerchantOrderID: cmd.MerchantOrderID, CustomerReference: cmd.CustomerReference,
		AmountMinor: cmd.AmountMinor, Currency: cmd.Currency, CurrencyScale: cmd.CurrencyScale,
		Description: cmd.Description, Metadata: append(json.RawMessage(nil), cmd.Metadata...),
		AllowedRoutes: append([]domain.RouteSelector(nil), cmd.AllowedRoutes...), Status: domain.IntentAwaitingRouteSelection,
		Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: cmd.ExpiresAt.UTC(), Routes: []domain.PaymentRoute{}}
	payload, _ := json.Marshal(map[string]any{"payment_intent_id": id, "merchant_order_id": cmd.MerchantOrderID, "status": p.Status})
	event := domain.EventEnvelope{ID: eventID, Type: "payment.intent.created", SchemaVersion: "2026-08-01", AggregateID: id,
		AggregateType: "payment_intent", AggregateVersion: 1, Sequence: 1, TenantID: p.TenantID, MerchantID: p.MerchantID,
		CorrelationID: cmd.CorrelationID, OccurredAt: now, RecordedAt: now, Payload: payload}
	s.intents[id] = p
	s.checkoutByHash[checkoutHash] = id
	s.checkoutTokens[id] = checkoutToken
	s.merchantOrders[orderKey] = id
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "intent", ID: id}
	s.outbox = append(s.outbox, event)
	response := cloneIntent(p)
	response.CheckoutToken = checkoutToken
	return response, false, nil
}

func (s *Store) GetCheckoutSession(_ context.Context, tokenHash [32]byte) (domain.CheckoutSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	intentID, ok := s.checkoutByHash[tokenHash]
	if !ok {
		return domain.CheckoutSession{}, domain.ErrNotFound
	}
	intent, ok := s.intents[intentID]
	if !ok {
		return domain.CheckoutSession{}, domain.ErrInvariantViolation
	}
	session := domain.CheckoutSession{IntentID: intent.ID, OrderID: intent.MerchantOrderID, Status: domain.CheckoutStatusForIntent(intent.Status, intent.ExpiresAt, s.now()), ExpiresAt: intent.ExpiresAt, Version: intent.Version, Routes: make([]domain.CheckoutRoute, 0, len(intent.Routes))}
	for _, route := range intent.Routes {
		session.Routes = append(session.Routes, domain.CheckoutRoute{ID: route.ID, Network: route.ChainID, Asset: route.AssetID, Amount: route.DisplayAmount, Address: chains.DisplayAddress(route.ChainID, route.Address)})
		if route.Status == domain.RouteSettled {
			session.SelectedRouteID = route.ID
		}
	}
	return session, nil
}

func (s *Store) ListUnmatched(context.Context, application.Principal, string, int) ([]domain.UnmatchedPayment, error) {
	return []domain.UnmatchedPayment{}, nil
}

func (s *Store) GetCandidates(context.Context, application.Principal, string) ([]domain.MatchCandidate, error) {
	return []domain.MatchCandidate{}, nil
}

func (s *Store) RecordAIRank(context.Context, application.Principal, string, int64, application.AIRankResult, string, string) error {
	return nil
}

func (s *Store) RequestManualResolution(context.Context, application.RequestManualResolution, string) (domain.ManualResolution, bool, error) {
	return domain.ManualResolution{}, false, domain.ErrNotFound
}

func (s *Store) ApproveManualResolution(context.Context, application.ApproveManualResolution) (domain.ManualResolution, error) {
	return domain.ManualResolution{}, domain.ErrNotFound
}

func (s *Store) ListEvents(context.Context, application.Principal, int64, int) ([]domain.PublicEvent, error) {
	return []domain.PublicEvent{}, nil
}

func (s *Store) GetEvent(context.Context, application.Principal, string) (domain.WebhookEventView, error) {
	return domain.WebhookEventView{}, domain.ErrNotFound
}

func (s *Store) ListTransfers(context.Context, application.Principal, string, int) ([]domain.MerchantTransfer, error) {
	return []domain.MerchantTransfer{}, nil
}

func (s *Store) GetTransfer(context.Context, application.Principal, string, string) ([]domain.MerchantTransfer, error) {
	return nil, domain.ErrNotFound
}

func (s *Store) ListQuotes(context.Context, application.Principal, string, int) ([]domain.QuoteView, error) {
	return []domain.QuoteView{}, nil
}

func (s *Store) GetQuote(context.Context, application.Principal, string) (domain.QuoteDetail, error) {
	return domain.QuoteDetail{}, domain.ErrNotFound
}

func (s *Store) CreateReconciliationReport(_ context.Context, cmd application.CreateReconciliationReport, requestHash string) (domain.ReconciliationReport, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "reconciliation_report.create", cmd.IdempotencyKey)
	if previous, ok := s.idempotency[key]; ok {
		if previous.Hash != requestHash {
			return domain.ReconciliationReport{}, false, domain.ErrIdempotencyConflict
		}
		return s.reports[previous.ID], true, nil
	}
	id, err := ids.New()
	if err != nil {
		return domain.ReconciliationReport{}, false, err
	}
	now := s.now()
	if cmd.PeriodEnd.After(now) {
		return domain.ReconciliationReport{}, false, fmt.Errorf("%w: period_end must not exceed snapshot_cutoff", domain.ErrValidation)
	}
	report := domain.ReconciliationReport{ID: id, TenantID: cmd.Principal.TenantID, MerchantID: cmd.Principal.MerchantID, Status: domain.ReconciliationReportQueued, Format: cmd.Format, PeriodStart: cmd.PeriodStart, PeriodEnd: cmd.PeriodEnd, SnapshotLedgerSequence: "0", SnapshotCutoff: now, CreatedAt: now, UpdatedAt: now, Version: 1}
	s.reports[id] = report
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "reconciliation_report", ID: id}
	return report, false, nil
}

func (s *Store) GetReconciliationReport(_ context.Context, principal application.Principal, id string) (domain.ReconciliationReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	report, ok := s.reports[id]
	if !ok || report.TenantID != principal.TenantID || report.MerchantID != principal.MerchantID {
		return domain.ReconciliationReport{}, domain.ErrNotFound
	}
	return report, nil
}

func (s *Store) ListBalances(context.Context, application.Principal) ([]domain.BalanceView, error) {
	return []domain.BalanceView{}, nil
}

func (s *Store) GetReconciliation(_ context.Context, principal application.Principal) (domain.ReconciliationSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int64{}
	for _, intent := range s.intents {
		if intent.TenantID == principal.TenantID && intent.MerchantID == principal.MerchantID {
			counts[string(intent.Status)]++
		}
	}
	return domain.ReconciliationSummary{IntentCounts: counts, GeneratedAt: s.now()}, nil
}

func (s *Store) GetIntent(_ context.Context, principal application.Principal, id string) (domain.PaymentIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.intents[id]
	if !ok || p.TenantID != principal.TenantID || p.MerchantID != principal.MerchantID {
		return domain.PaymentIntent{}, domain.ErrNotFound
	}
	return cloneIntent(p), nil
}

func (s *Store) ListIntents(_ context.Context, principal application.Principal, status, after string, limit int) ([]domain.PaymentIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.PaymentIntent, 0)
	for _, intent := range s.intents {
		if intent.TenantID != principal.TenantID || intent.MerchantID != principal.MerchantID {
			continue
		}
		if status != "" && string(intent.Status) != status {
			continue
		}
		if after != "" && intent.ID >= after {
			continue
		}
		items = append(items, cloneIntent(intent))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) FindRouteReplay(_ context.Context, p application.Principal, key, requestHash string) (domain.PaymentRoute, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.idempotency[scopeKey(p, "create_route", key)]
	if !ok {
		return domain.PaymentRoute{}, false, nil
	}
	if record.Hash != requestHash {
		return domain.PaymentRoute{}, false, domain.ErrIdempotencyConflict
	}
	for _, intent := range s.intents {
		if intent.TenantID != p.TenantID || intent.MerchantID != p.MerchantID {
			continue
		}
		for _, route := range intent.Routes {
			if route.ID == record.ID {
				return route, true, nil
			}
		}
	}
	return domain.PaymentRoute{}, false, domain.ErrInvariantViolation
}

func (s *Store) CreateRoute(_ context.Context, cmd application.CreateRoute, requestHash string) (domain.PaymentRoute, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "create_route", cmd.IdempotencyKey)
	if old, ok := s.idempotency[key]; ok {
		if old.Hash != requestHash {
			return domain.PaymentRoute{}, false, domain.ErrIdempotencyConflict
		}
		for _, r := range s.intents[cmd.IntentID].Routes {
			if r.ID == old.ID {
				return r, true, nil
			}
		}
		return domain.PaymentRoute{}, false, domain.ErrInvariantViolation
	}
	p, ok := s.intents[cmd.IntentID]
	if !ok || p.TenantID != cmd.Principal.TenantID || p.MerchantID != cmd.Principal.MerchantID {
		return domain.PaymentRoute{}, false, domain.ErrNotFound
	}
	if p.Status != domain.IntentAwaitingRouteSelection && p.Status != domain.IntentPending {
		return domain.PaymentRoute{}, false, domain.ErrStateConflict
	}
	id, err := ids.New()
	if err != nil {
		return domain.PaymentRoute{}, false, err
	}
	now := s.now()
	provider := cmd.Provider
	if provider == "" {
		provider = domain.RouteProviderOnChain
	}
	r := domain.PaymentRoute{ID: id, IntentID: p.ID, QuoteID: cmd.QuoteID, AddressAssignmentID: cmd.AddressAssignmentID, ChainID: cmd.ChainID, AssetID: cmd.AssetID, Provider: provider, ProviderID: cmd.ProviderID, ProviderOrderID: cmd.ProviderOrderID, ProviderReference: cmd.ProviderReference, PaymentURL: cmd.PaymentURL,
		ExpectedAmount: cmd.ExpectedAmount, AssetDecimals: cmd.AssetDecimals, DisplayAmount: cmd.DisplayAmount,
		Address: cmd.Address, Memo: cmd.Memo, RequiredFinality: cmd.RequiredFinality, Status: domain.RouteActive,
		Version: 1, StartsAt: now, ExpiresAt: cmd.ExpiresAt.UTC(), GraceEndsAt: cmd.GraceEndsAt.UTC()}
	if provider == domain.RouteProviderOnChain {
		reservation := r.ReservationKey()
		if existing, exists := s.reservations[reservation]; exists && existing != id {
			return domain.PaymentRoute{}, false, fmt.Errorf("%w: exact amount is already reserved", domain.ErrStateConflict)
		}
		s.reservations[reservation] = id
		s.reservationGrace[reservation] = r.GraceEndsAt
	}
	p.Routes = append(p.Routes, r)
	if p.Status == domain.IntentAwaitingRouteSelection {
		_ = p.Transition(domain.IntentPending, "route_created", now)
	} else {
		p.Version++
		p.UpdatedAt = now
	}
	eventID, err := ids.New()
	if err != nil {
		return domain.PaymentRoute{}, false, err
	}
	payload, _ := json.Marshal(map[string]any{"payment_intent_id": p.ID, "route_id": id, "provider": r.Provider, "chain_id": r.ChainID, "provider_id": r.ProviderID, "asset_id": r.AssetID})
	s.intents[p.ID] = p
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "route", ID: id}
	s.outbox = append(s.outbox, domain.EventEnvelope{ID: eventID, Type: "payment.route.created", SchemaVersion: "2026-08-01",
		AggregateID: p.ID, AggregateType: "payment_intent", AggregateVersion: p.Version, Sequence: p.Version,
		TenantID: p.TenantID, MerchantID: p.MerchantID, CorrelationID: cmd.CorrelationID, OccurredAt: now, RecordedAt: now, Payload: payload})
	return r, false, nil
}

func (s *Store) CancelIntent(_ context.Context, cmd application.CancelIntent, requestHash string) (domain.PaymentIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(cmd.Principal, "cancel_intent", cmd.IdempotencyKey)
	if old, ok := s.idempotency[key]; ok {
		if old.Hash != requestHash {
			return domain.PaymentIntent{}, false, domain.ErrIdempotencyConflict
		}
		return cloneIntent(s.intents[old.ID]), true, nil
	}
	p, ok := s.intents[cmd.IntentID]
	if !ok || p.TenantID != cmd.Principal.TenantID || p.MerchantID != cmd.Principal.MerchantID {
		return domain.PaymentIntent{}, false, domain.ErrNotFound
	}
	if cmd.ExpectedVersion > 0 && cmd.ExpectedVersion != p.Version {
		return domain.PaymentIntent{}, false, domain.ErrVersionConflict
	}
	if err := p.Transition(domain.IntentCancelled, cmd.Reason, s.now()); err != nil {
		return domain.PaymentIntent{}, false, err
	}
	for i := range p.Routes {
		if p.Routes[i].Status == domain.RouteActive {
			p.Routes[i].Status = domain.RouteCancelled
			p.Routes[i].Version++
		}
	}
	eventID, err := ids.New()
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	now := s.now()
	payload, _ := json.Marshal(map[string]any{"payment_intent_id": p.ID, "merchant_order_id": p.MerchantOrderID, "status": p.Status, "reason": cmd.Reason})
	s.intents[p.ID] = p
	s.idempotency[key] = idempotencyRecord{Hash: requestHash, Kind: "intent", ID: p.ID}
	s.outbox = append(s.outbox, domain.EventEnvelope{ID: eventID, Type: "payment.cancelled", SchemaVersion: "2026-08-01",
		AggregateID: p.ID, AggregateType: "payment_intent", AggregateVersion: p.Version, Sequence: p.Version,
		TenantID: p.TenantID, MerchantID: p.MerchantID, CorrelationID: cmd.CorrelationID, OccurredAt: now, RecordedAt: now, Payload: payload})
	return cloneIntent(p), false, nil
}

// ReleaseElapsedRouteGrace mirrors the production grace sweeper for
// deterministic lifecycle tests. Exact address/asset/amount tuples remain
// unavailable after cancellation/expiry until the immutable route grace ends.
func (s *Store) ReleaseElapsedRouteGrace(_ context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit < 1 || limit > 500 {
		return 0, fmt.Errorf("%w: invalid route grace sweep", domain.ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	released := 0
	for reservation, grace := range s.reservationGrace {
		if released >= limit || grace.After(now) {
			continue
		}
		routeID := s.reservations[reservation]
		terminal := false
		for _, intent := range s.intents {
			for _, route := range intent.Routes {
				if route.ID == routeID && (route.Status == domain.RouteExpired || route.Status == domain.RouteCancelled || route.Status == domain.RouteSettled) {
					terminal = true
					break
				}
			}
			if terminal {
				break
			}
		}
		if !terminal {
			continue
		}
		delete(s.reservations, reservation)
		delete(s.reservationGrace, reservation)
		released++
	}
	return released, nil
}

func (s *Store) ListAssets(_ context.Context, _ application.Principal) ([]domain.Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.Asset(nil), s.assets...), nil
}

func (s *Store) Outbox() []domain.EventEnvelope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.EventEnvelope(nil), s.outbox...)
}

func scopeKey(p application.Principal, kind, value string) string {
	return p.TenantID + "\x1f" + p.MerchantID + "\x1f" + kind + "\x1f" + value
}
func cloneIntent(p domain.PaymentIntent) domain.PaymentIntent {
	p.Routes = append([]domain.PaymentRoute(nil), p.Routes...)
	p.AllowedRoutes = append([]domain.RouteSelector(nil), p.AllowedRoutes...)
	p.Metadata = append(json.RawMessage(nil), p.Metadata...)
	return p
}
