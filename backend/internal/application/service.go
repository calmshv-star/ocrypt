package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/calmshv-star/ocrypt/backend/internal/checkout"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Service struct {
	store Store
	clock Clock
}

func New(store Store) *Service                       { return &Service{store: store, clock: systemClock{}} }
func NewWithClock(store Store, clock Clock) *Service { return &Service{store: store, clock: clock} }

func (s *Service) CreateIntent(ctx context.Context, cmd CreateIntent) (domain.PaymentIntent, bool, error) {
	if cmd.Principal.TenantID == "" || cmd.Principal.MerchantID == "" {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: tenant and merchant identity are required", domain.ErrValidation)
	}
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentIntent{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: idempotency key length must be 8..255", domain.ErrValidation)
	}
	if strings.TrimSpace(cmd.MerchantOrderID) == "" || len(cmd.MerchantOrderID) > 128 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: merchant_order_id is required and cannot exceed 128 characters", domain.ErrValidation)
	}
	if cmd.AmountMinor.IsZero() {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: amount_minor must be positive", domain.ErrValidation)
	}
	if err := domain.ValidateCurrency(cmd.Currency, cmd.CurrencyScale); err != nil {
		return domain.PaymentIntent{}, false, err
	}
	now := s.clock.Now()
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = now.Add(30 * time.Minute)
	}
	if cmd.ExpiresAt.Before(now.Add(time.Minute)) || cmd.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: expires_at must be between 1 minute and 7 days from now", domain.ErrValidation)
	}
	if len(cmd.Metadata) > 16*1024 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: metadata exceeds 16 KiB", domain.ErrValidation)
	}
	if len(cmd.Description) > 2048 || len(cmd.CustomerReference) > 256 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: description or customer_reference exceeds its size limit", domain.ErrValidation)
	}
	if len(cmd.Metadata) > 0 {
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(cmd.Metadata, &metadata); err != nil || metadata == nil || len(metadata) > 64 {
			return domain.PaymentIntent{}, false, fmt.Errorf("%w: metadata must be a JSON object with at most 64 properties", domain.ErrValidation)
		}
	}
	if len(cmd.AllowedRoutes) > 64 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: allowed_routes cannot exceed 64 entries", domain.ErrValidation)
	}
	seenRoutes := make(map[string]bool, len(cmd.AllowedRoutes))
	for _, route := range cmd.AllowedRoutes {
		provider := route.Provider
		if provider == "" {
			provider = domain.RouteProviderOnChain
		}
		key := provider + "\x1f" + route.ChainID + "\x1f" + route.ProviderID + "\x1f" + route.AssetID
		validOnChain := provider == domain.RouteProviderOnChain && route.ChainID != "" && route.ProviderID == ""
		validHosted := provider == domain.RouteProviderHostedGateway && route.ChainID == "" && route.ProviderID != ""
		if route.AssetID == "" || (!validOnChain && !validHosted) || seenRoutes[key] {
			return domain.PaymentIntent{}, false, fmt.Errorf("%w: allowed_routes must be unique provider-discriminated routes", domain.ErrValidation)
		}
		seenRoutes[key] = true
	}
	hash := cmd.RequestHash
	var err error
	if hash == "" {
		hash, err = commandHash(struct {
			MerchantOrderID   string                 `json:"merchant_order_id"`
			CustomerReference string                 `json:"customer_reference"`
			AmountMinor       string                 `json:"amount_minor"`
			Currency          string                 `json:"currency"`
			CurrencyScale     uint8                  `json:"currency_scale"`
			Description       string                 `json:"description"`
			Metadata          json.RawMessage        `json:"metadata"`
			AllowedRoutes     []domain.RouteSelector `json:"allowed_routes"`
			ExpiresAt         time.Time              `json:"expires_at"`
		}{cmd.MerchantOrderID, cmd.CustomerReference, cmd.AmountMinor.String(), cmd.Currency, cmd.CurrencyScale, cmd.Description, cmd.Metadata, cmd.AllowedRoutes, cmd.ExpiresAt.UTC()})
	}
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	return s.store.CreateIntent(ctx, cmd, hash)
}

func (s *Service) GetIntent(ctx context.Context, principal Principal, id string) (domain.PaymentIntent, error) {
	if !principal.Allows("payments:read") {
		return domain.PaymentIntent{}, fmt.Errorf("forbidden: payments:read scope is required")
	}
	if !ids.Valid(id) {
		return domain.PaymentIntent{}, fmt.Errorf("%w: payment intent id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetIntent(ctx, principal, id)
}

func (s *Service) ListIntents(ctx context.Context, principal Principal, status, after string, limit int) ([]domain.PaymentIntent, error) {
	if !principal.Allows("payments:read") {
		return nil, fmt.Errorf("forbidden: payments:read scope is required")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: limit must be 1..100", domain.ErrValidation)
	}
	if after != "" && !ids.Valid(after) {
		return nil, fmt.Errorf("%w: after cursor must be a canonical UUID", domain.ErrValidation)
	}
	if status != "" {
		valid := false
		for _, candidate := range []domain.IntentStatus{domain.IntentCreated, domain.IntentAwaitingRouteSelection, domain.IntentPending, domain.IntentObserved, domain.IntentPartiallyPaid, domain.IntentConfirmed, domain.IntentSettled, domain.IntentExpired, domain.IntentNeedsReview, domain.IntentOverpaid, domain.IntentReorgReview, domain.IntentReversed, domain.IntentCancelled} {
			if status == string(candidate) {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("%w: invalid payment intent status", domain.ErrValidation)
		}
	}
	return s.store.ListIntents(ctx, principal, status, after, limit)
}

func (s *Service) CreateRoute(ctx context.Context, cmd CreateRoute) (domain.PaymentRoute, bool, error) {
	if cmd.Principal.TenantID == "" || cmd.Principal.MerchantID == "" {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: tenant and merchant identity are required", domain.ErrValidation)
	}
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentRoute{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if cmd.Provider == "" {
		cmd.Provider = domain.RouteProviderOnChain
	}
	validOnChain := cmd.Provider == domain.RouteProviderOnChain && cmd.ChainID != "" && cmd.Address != "" && cmd.ProviderID == "" && cmd.ProviderOrderID == "" && cmd.ProviderReference == "" && cmd.PaymentURL == ""
	validHosted := cmd.Provider == domain.RouteProviderHostedGateway && cmd.ChainID == "" && cmd.Address == "" && cmd.QuoteID == "" && cmd.AddressAssignmentID == "" && cmd.RequiredFinality == 0 && cmd.ProviderID != "" && ids.Valid(cmd.ProviderOrderID) && cmd.ProviderReference != "" && cmd.PaymentURL != ""
	if len(cmd.IdempotencyKey) < 8 || cmd.AssetID == "" || cmd.ExpectedAmount.IsZero() || (!validOnChain && !validHosted) {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: route must be exactly one complete on_chain or hosted_gateway variant", domain.ErrValidation)
	}
	if !ids.Valid(cmd.IntentID) || (cmd.QuoteID != "" && !ids.Valid(cmd.QuoteID)) || (cmd.AddressAssignmentID != "" && !ids.Valid(cmd.AddressAssignmentID)) {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: route identifiers must be canonical UUIDs", domain.ErrValidation)
	}
	if len(cmd.IdempotencyKey) > 255 || len(cmd.ChainID) > 128 || len(cmd.AssetID) > 128 || len(cmd.Address) > 512 || len(cmd.Memo) > 256 || len(cmd.ProviderID) > 128 || len(cmd.ProviderReference) > 255 || len(cmd.PaymentURL) > 2048 {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: route field exceeds its size limit", domain.ErrValidation)
	}
	if validHosted {
		paymentURL, err := url.Parse(cmd.PaymentURL)
		if err != nil || paymentURL.Scheme != "https" || paymentURL.Host == "" || paymentURL.User != nil || paymentURL.Fragment != "" {
			return domain.PaymentRoute{}, false, fmt.Errorf("%w: hosted payment_url must be an admitted HTTPS URL", domain.ErrValidation)
		}
	}
	now := s.clock.Now()
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = now.Add(30 * time.Minute)
	}
	if cmd.GraceEndsAt.IsZero() {
		cmd.GraceEndsAt = cmd.ExpiresAt.Add(24 * time.Hour)
	}
	if !cmd.ExpiresAt.After(now) || cmd.GraceEndsAt.Before(cmd.ExpiresAt) {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: invalid route validity window", domain.ErrValidation)
	}
	hash := cmd.RequestHash
	var err error
	if hash == "" {
		hash, err = commandHash(struct {
			IntentID, Provider, ProviderID, ProviderOrderID, ProviderReference, PaymentURL, QuoteID, AddressAssignmentID, ChainID, AssetID, ExpectedAmount, Address, Memo string
			AssetDecimals                                                                                                                                                 uint8
			RequiredFinality                                                                                                                                              uint64
			ExpiresAt, GraceEndsAt                                                                                                                                        time.Time
		}{cmd.IntentID, cmd.Provider, cmd.ProviderID, cmd.ProviderOrderID, cmd.ProviderReference, cmd.PaymentURL, cmd.QuoteID, cmd.AddressAssignmentID, cmd.ChainID, cmd.AssetID, cmd.ExpectedAmount.String(), cmd.Address, cmd.Memo, cmd.AssetDecimals, cmd.RequiredFinality, cmd.ExpiresAt.UTC(), cmd.GraceEndsAt.UTC()})
	}
	if err != nil {
		return domain.PaymentRoute{}, false, err
	}
	return s.store.CreateRoute(ctx, cmd, hash)
}

// FindRouteReplay is deliberately called before route planning. Quotes,
// provider health, and address pools can change between retries; a completed
// idempotent request must replay its recorded response without consulting any
// of those mutable dependencies.
func (s *Service) FindRouteReplay(ctx context.Context, p Principal, key, requestHash string) (domain.PaymentRoute, bool, error) {
	if p.TenantID == "" || p.MerchantID == "" || !p.Allows("payments:write") {
		return domain.PaymentRoute{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(key) < 8 || len(key) > 255 || len(requestHash) != sha256.Size*2 {
		return domain.PaymentRoute{}, false, fmt.Errorf("%w: valid idempotency key and request hash are required", domain.ErrValidation)
	}
	return s.store.FindRouteReplay(ctx, p, key, requestHash)
}

func (s *Service) CancelIntent(ctx context.Context, cmd CancelIntent) (domain.PaymentIntent, bool, error) {
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentIntent{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: idempotency key is required", domain.ErrValidation)
	}
	if !ids.Valid(cmd.IntentID) {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: payment intent id must be a canonical UUID", domain.ErrValidation)
	}
	if strings.TrimSpace(cmd.Reason) == "" || len(cmd.Reason) > 512 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: cancellation reason is required and cannot exceed 512 characters", domain.ErrValidation)
	}
	hash := cmd.RequestHash
	if hash == "" {
		hash, _ = commandHash(struct {
			ID, Reason string
			Version    int64
		}{cmd.IntentID, cmd.Reason, cmd.ExpectedVersion})
	}
	return s.store.CancelIntent(ctx, cmd, hash)
}

func (s *Service) ExpireIntent(ctx context.Context, cmd ExpireIntent) (domain.PaymentIntent, bool, error) {
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentIntent{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 || !ids.Valid(cmd.IntentID) || cmd.ExpectedVersion < 1 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: intent, expected_version, and valid idempotency key are required", domain.ErrValidation)
	}
	cmd.Reason = strings.TrimSpace(cmd.Reason)
	if cmd.Reason == "" || len(cmd.Reason) > 512 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: expiration reason is required and cannot exceed 512 characters", domain.ErrValidation)
	}
	hash := cmd.RequestHash
	if hash == "" {
		hash, _ = commandHash(struct {
			ID, Reason string
			Version    int64
		}{cmd.IntentID, cmd.Reason, cmd.ExpectedVersion})
	}
	return s.store.ExpireIntent(ctx, cmd, hash)
}

func (s *Service) UpdateIntentMetadata(ctx context.Context, cmd UpdateIntentMetadata) (domain.PaymentIntent, bool, error) {
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentIntent{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 || !ids.Valid(cmd.IntentID) || cmd.ExpectedVersion < 1 {
		return domain.PaymentIntent{}, false, fmt.Errorf("%w: intent, expected_version, and valid idempotency key are required", domain.ErrValidation)
	}
	canonical, err := validateMutableMetadata(cmd.Metadata)
	if err != nil {
		return domain.PaymentIntent{}, false, err
	}
	cmd.Metadata = canonical
	hash := cmd.RequestHash
	if hash == "" {
		hash, _ = commandHash(struct {
			ID       string          `json:"id"`
			Version  int64           `json:"expected_version"`
			Metadata json.RawMessage `json:"metadata"`
		}{cmd.IntentID, cmd.ExpectedVersion, cmd.Metadata})
	}
	return s.store.UpdateIntentMetadata(ctx, cmd, hash)
}

func validateMutableMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 8*1024 {
		return nil, fmt.Errorf("%w: metadata must be a JSON object no larger than 8 KiB", domain.ErrValidation)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil || len(object) > 4 {
		return nil, fmt.Errorf("%w: metadata must contain only allowlisted non-financial fields", domain.ErrValidation)
	}
	for key, value := range object {
		switch key {
		case "display_note":
			var text string
			if json.Unmarshal(value, &text) != nil || len(text) > 1024 {
				return nil, fmt.Errorf("%w: display_note must be a string of at most 1024 bytes", domain.ErrValidation)
			}
		case "locale":
			var locale string
			if json.Unmarshal(value, &locale) != nil || !validMetadataLocale(locale) {
				return nil, fmt.Errorf("%w: locale is invalid", domain.ErrValidation)
			}
		case "return_reference":
			var ref string
			if json.Unmarshal(value, &ref) != nil || strings.TrimSpace(ref) != ref || len(ref) > 512 {
				return nil, fmt.Errorf("%w: return_reference must be a trimmed string of at most 512 bytes", domain.ErrValidation)
			}
		case "custom_data":
			var custom map[string]any
			if json.Unmarshal(value, &custom) != nil || custom == nil || len(custom) > 32 || len(value) > 4096 {
				return nil, fmt.Errorf("%w: custom_data must be an object with at most 32 fields and 4 KiB", domain.ErrValidation)
			}
			for customKey, customValue := range custom {
				if customKey == "" || len(customKey) > 64 {
					return nil, fmt.Errorf("%w: custom_data key is invalid", domain.ErrValidation)
				}
				switch typed := customValue.(type) {
				case nil, bool:
				case string:
					if len(typed) > 1024 {
						return nil, fmt.Errorf("%w: custom_data string is too long", domain.ErrValidation)
					}
				default:
					return nil, fmt.Errorf("%w: custom_data values may only be strings, booleans, or null", domain.ErrValidation)
				}
			}
		default:
			return nil, fmt.Errorf("%w: metadata field %q is not mutable", domain.ErrValidation, key)
		}
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func validMetadataLocale(value string) bool {
	if len(value) < 2 || len(value) > 35 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, part := range strings.Split(value, "-") {
		if len(part) < 2 || len(part) > 8 {
			return false
		}
		for _, r := range part {
			if r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

func (s *Service) ListAssets(ctx context.Context, principal Principal) ([]domain.Asset, error) {
	if !principal.Allows("payments:read") {
		return nil, fmt.Errorf("forbidden: payments:read scope is required")
	}
	return s.store.ListAssets(ctx, principal)
}

func (s *Service) SubmitPaymentProof(ctx context.Context, cmd SubmitPaymentProof) (domain.PaymentProof, bool, error) {
	if !cmd.Principal.Allows("payments:write") {
		return domain.PaymentProof{}, false, fmt.Errorf("forbidden: payments:write scope is required")
	}
	if len(cmd.IdempotencyKey) < 8 || len(cmd.IdempotencyKey) > 255 || cmd.PaymentIntentID == "" || cmd.ChainID == "" || cmd.TransactionID == "" || len(cmd.TransactionID) > 256 {
		return domain.PaymentProof{}, false, fmt.Errorf("%w: valid idempotency key, intent, chain, and transaction are required", domain.ErrValidation)
	}
	if !ids.Valid(cmd.PaymentIntentID) {
		return domain.PaymentProof{}, false, fmt.Errorf("%w: payment_intent_id must be a canonical UUID", domain.ErrValidation)
	}
	hash := cmd.RequestHash
	if hash == "" {
		hash, _ = commandHash(struct{ Intent, Chain, Transaction string }{cmd.PaymentIntentID, cmd.ChainID, cmd.TransactionID})
	}
	return s.store.CreatePaymentProof(ctx, cmd, hash)
}
func (s *Service) GetPaymentProof(ctx context.Context, p Principal, id string) (domain.PaymentProof, error) {
	if !p.Allows("payments:read") {
		return domain.PaymentProof{}, fmt.Errorf("forbidden: payments:read scope is required")
	}
	if !ids.Valid(id) {
		return domain.PaymentProof{}, fmt.Errorf("%w: payment proof id must be a canonical UUID", domain.ErrValidation)
	}
	return s.store.GetPaymentProof(ctx, p, id)
}

func (s *Service) GetCheckoutSession(ctx context.Context, token string) (domain.CheckoutSession, error) {
	hash, err := checkout.Hash(token)
	if err != nil {
		return domain.CheckoutSession{}, domain.ErrNotFound
	}
	return s.store.GetCheckoutSession(ctx, hash)
}

func commandHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
