package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type merchantOrderRequest struct {
	OrderID       string          `json:"order_id"`
	CustomerID    string          `json:"customer_id,omitempty"`
	Amount        string          `json:"amount"`
	Currency      string          `json:"currency"`
	CurrencyScale *uint8          `json:"currency_scale,omitempty"`
	Network       string          `json:"network"`
	Asset         string          `json:"asset"`
	Description   string          `json:"description,omitempty"`
	ExpiresIn     uint32          `json:"expires_in,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type merchantPayment struct {
	RouteID              string `json:"route_id"`
	Network              string `json:"network"`
	Asset                string `json:"asset"`
	Address              string `json:"address"`
	Memo                 string `json:"memo,omitempty"`
	Amount               string `json:"amount"`
	ExpectedAmountAtomic string `json:"expected_amount_atomic"`
	ReceivedAmount       string `json:"received_amount,omitempty"`
	RemainingAmount      string `json:"remaining_amount,omitempty"`
	ExcessAmount         string `json:"excess_amount,omitempty"`
	PaymentCount         int64  `json:"payment_count,omitempty"`
	RequiredFinality     uint64 `json:"required_finality"`
	TopUpAllowed         bool   `json:"top_up_allowed"`
}

type merchantOrderResponse struct {
	PaymentID    string           `json:"payment_id"`
	OrderID      string           `json:"order_id"`
	CustomerID   string           `json:"customer_id,omitempty"`
	Status       string           `json:"status"`
	StatusReason string           `json:"status_reason,omitempty"`
	Amount       string           `json:"amount"`
	Currency     string           `json:"currency"`
	Payment      *merchantPayment `json:"payment,omitempty"`
	CheckoutURL  string           `json:"checkout_url,omitempty"`
	ReceiptURL   string           `json:"receipt_url,omitempty"`
	ExpiresAt    time.Time        `json:"expires_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	Version      int64            `json:"version"`
}

func (s *Server) createMerchantOrder(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	body, principal, ok := s.authenticateBody(w, r, requestID)
	if !ok {
		return
	}
	var input merchantOrderRequest
	if err := decodeStrict(body, &input); err != nil {
		writeError(w, requestID, err)
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 220 {
		writeError(w, requestID, fmt.Errorf("%w: Idempotency-Key length must be 8..220", domain.ErrValidation))
		return
	}
	scale := uint8(2)
	if input.CurrencyScale != nil {
		scale = *input.CurrencyScale
	}
	amountMinor, err := money.ParseDecimal(input.Amount, scale)
	if err != nil {
		writeError(w, requestID, fmt.Errorf("%w: invalid amount: %v", domain.ErrValidation, err))
		return
	}
	if err = domain.ValidateCurrency(input.Currency, scale); err != nil {
		writeError(w, requestID, err)
		return
	}
	asset, err := s.resolveMerchantAsset(r.Context(), principal, input.Network, input.Asset)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	expiresIn := input.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 30 * 60
	}
	if expiresIn < 60 || expiresIn > 24*60*60 {
		writeError(w, requestID, fmt.Errorf("%w: expires_in must be 60..86400 seconds", domain.ErrValidation))
		return
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	selector := domain.RouteSelector{Provider: domain.RouteProviderOnChain, ChainID: asset.ChainID, AssetID: asset.ID}
	intentHash := merchantFingerprint("intent", body)
	intent, intentReplay, err := s.service.CreateIntent(r.Context(), application.CreateIntent{
		Principal: principal, IdempotencyKey: "merchant-intent:" + idempotencyKey, MerchantOrderID: input.OrderID,
		CustomerReference: input.CustomerID, AmountMinor: amountMinor, Currency: input.Currency,
		CurrencyScale: scale, Description: input.Description, Metadata: input.Metadata,
		AllowedRoutes: []domain.RouteSelector{selector}, ExpiresAt: expiresAt,
		CorrelationID: requestID, RequestHash: intentHash,
	})
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	routeKey := "merchant-route:" + idempotencyKey
	routeHash := merchantFingerprint("route", body)
	_, routeReplay, err := s.service.FindRouteReplay(r.Context(), principal, routeKey, routeHash)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if !routeReplay {
		plan := RoutePlanRequest{Provider: domain.RouteProviderOnChain, ChainID: asset.ChainID, AssetID: asset.ID, ExpiresIn: time.Duration(expiresIn) * time.Second, IdempotencyKey: routeKey, RequestHash: routeHash}
		command, planErr := s.planner.Plan(r.Context(), principal, intent, plan)
		if planErr != nil {
			writeError(w, requestID, planErr)
			return
		}
		command.IdempotencyKey, command.CorrelationID, command.RequestHash = routeKey, requestID, routeHash
		_, routeReplay, err = s.service.CreateRoute(r.Context(), command)
		if err != nil {
			if releaser, supported := s.planner.(routePlanReleaser); supported {
				cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
				cleanupErr := releaser.ReleasePlan(cleanupContext, principal, intent.ID, plan)
				cancel()
				if cleanupErr != nil {
					err = errors.Join(err, fmt.Errorf("release failed merchant route plan: %w", cleanupErr))
				}
			}
			writeError(w, requestID, err)
			return
		}
	}
	checkoutToken := intent.CheckoutToken
	intent, err = s.service.GetIntent(r.Context(), principal, intent.ID)
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	if intentReplay && routeReplay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeEnvelope(w, http.StatusCreated, requestID, s.merchantResponse(intent, checkoutToken))
}

func (s *Server) getMerchantOrder(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	principal, ok := s.authenticateEmpty(w, r, requestID)
	if !ok {
		return
	}
	intent, err := s.service.GetIntent(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		writeError(w, requestID, err)
		return
	}
	writeEnvelope(w, http.StatusOK, requestID, s.merchantResponse(intent, ""))
}

func (s *Server) resolveMerchantAsset(ctx context.Context, principal application.Principal, network, symbol string) (domain.Asset, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	aliases := map[string]string{"ethereum": "eip155:1", "eth": "eip155:1", "tron": "tron:mainnet", "trx": "tron:mainnet", "solana": "solana:mainnet", "sol": "solana:mainnet", "ton": "ton:mainnet"}
	if canonical := aliases[network]; canonical != "" {
		network = canonical
	}
	assets, err := s.service.ListAssets(ctx, principal)
	if err != nil {
		return domain.Asset{}, err
	}
	var matches []domain.Asset
	for _, candidate := range assets {
		if candidate.ChainID == network && (candidate.Symbol == symbol || strings.EqualFold(candidate.ID, symbol)) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return domain.Asset{}, fmt.Errorf("%w: network and asset must resolve to one active route", domain.ErrValidation)
	}
	return matches[0], nil
}

func (s *Server) merchantResponse(intent domain.PaymentIntent, checkoutToken string) merchantOrderResponse {
	result := merchantOrderResponse{PaymentID: intent.ID, OrderID: intent.MerchantOrderID, CustomerID: intent.CustomerReference, Status: string(intent.Status), StatusReason: intent.StatusReason, Amount: formatMinorAmount(intent.AmountMinor.String(), intent.CurrencyScale), Currency: intent.Currency, ExpiresAt: intent.ExpiresAt, UpdatedAt: intent.UpdatedAt, Version: intent.Version}
	if len(intent.Routes) == 1 {
		route := intent.Routes[0]
		result.Payment = &merchantPayment{RouteID: route.ID, Network: route.ChainID, Asset: route.AssetID, Address: route.Address, Memo: route.Memo, Amount: route.DisplayAmount, ExpectedAmountAtomic: route.ExpectedAmount.String(), ReceivedAmount: route.ReceivedAmount, RemainingAmount: route.RemainingAmount, ExcessAmount: route.ExcessAmount, PaymentCount: route.PaymentCount, RequiredFinality: route.RequiredFinality, TopUpAllowed: intent.Status == domain.IntentPartiallyPaid && route.RemainingAmount != "" && route.RemainingAmount != "0" && time.Now().UTC().Before(route.ExpiresAt)}
	}
	if checkoutToken != "" {
		base := s.checkoutPublicBaseURL
		if base == "" {
			base = "https://pay.example.com"
		}
		result.CheckoutURL = base + "/checkout?token=" + url.QueryEscape(checkoutToken)
		result.ReceiptURL = base + "/v1/checkout-sessions/" + url.PathEscape(checkoutToken) + "/receipt"
	}
	return result
}

func merchantFingerprint(operation string, body []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("merchant-order-v1\x00" + operation + "\x00"))
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func formatMinorAmount(value string, scale uint8) string {
	if scale == 0 {
		return value
	}
	for len(value) <= int(scale) {
		value = "0" + value
	}
	cut := len(value) - int(scale)
	return value[:cut] + "." + value[cut:]
}
