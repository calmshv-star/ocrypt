package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type IntentStatus string

const (
	IntentCreated                IntentStatus = "created"
	IntentAwaitingRouteSelection IntentStatus = "awaiting_route_selection"
	IntentPending                IntentStatus = "pending"
	IntentObserved               IntentStatus = "observed"
	IntentPartiallyPaid          IntentStatus = "partially_paid"
	IntentConfirmed              IntentStatus = "confirmed"
	IntentSettled                IntentStatus = "settled"
	IntentExpired                IntentStatus = "expired"
	IntentNeedsReview            IntentStatus = "needs_review"
	IntentOverpaid               IntentStatus = "overpaid"
	IntentReorgReview            IntentStatus = "reorg_review"
	IntentReversed               IntentStatus = "reversed"
	IntentCancelled              IntentStatus = "cancelled"
)

var intentTransitions = map[IntentStatus]map[IntentStatus]bool{
	IntentCreated:                {IntentAwaitingRouteSelection: true, IntentPending: true, IntentExpired: true, IntentCancelled: true},
	IntentAwaitingRouteSelection: {IntentPending: true, IntentCancelled: true, IntentExpired: true},
	IntentPending:                {IntentObserved: true, IntentExpired: true, IntentCancelled: true, IntentNeedsReview: true},
	IntentObserved:               {IntentPartiallyPaid: true, IntentConfirmed: true, IntentNeedsReview: true},
	IntentPartiallyPaid:          {IntentObserved: true, IntentConfirmed: true, IntentNeedsReview: true, IntentExpired: true},
	IntentConfirmed:              {IntentSettled: true, IntentNeedsReview: true, IntentReorgReview: true},
	IntentExpired:                {IntentNeedsReview: true},
	IntentNeedsReview:            {IntentSettled: true, IntentReversed: true},
	IntentSettled:                {IntentOverpaid: true, IntentReorgReview: true},
	IntentOverpaid:               {IntentReorgReview: true},
	IntentReorgReview:            {IntentObserved: true, IntentConfirmed: true, IntentSettled: true, IntentReversed: true},
}

func CanTransitionIntent(from, to IntentStatus) bool { return intentTransitions[from][to] }

type PaymentIntent struct {
	ID                string          `json:"id"`
	CheckoutToken     string          `json:"checkout_token,omitempty"`
	TenantID          string          `json:"-"`
	MerchantID        string          `json:"merchant_id"`
	MerchantOrderID   string          `json:"merchant_order_id"`
	CustomerReference string          `json:"customer_reference,omitempty"`
	AmountMinor       money.Amount    `json:"amount_minor"`
	Currency          string          `json:"currency"`
	CurrencyScale     uint8           `json:"currency_scale"`
	Description       string          `json:"description,omitempty"`
	Status            IntentStatus    `json:"status"`
	StatusReason      string          `json:"status_reason,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	AllowedRoutes     []RouteSelector `json:"allowed_routes"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
	SettledAt         *time.Time      `json:"settled_at,omitempty"`
	CancelledAt       *time.Time      `json:"cancelled_at,omitempty"`
	Routes            []PaymentRoute  `json:"routes"`
}

type RouteSelector struct {
	Provider   string `json:"provider,omitempty"`
	ChainID    string `json:"chain_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	AssetID    string `json:"asset_id"`
}

func (p *PaymentIntent) Transition(to IntentStatus, reason string, now time.Time) error {
	if !CanTransitionIntent(p.Status, to) {
		return fmt.Errorf("%w: payment intent %s cannot transition from %s to %s", ErrStateConflict, p.ID, p.Status, to)
	}
	p.Status = to
	p.StatusReason = reason
	p.Version++
	p.UpdatedAt = now.UTC()
	if to == IntentSettled {
		t := now.UTC()
		p.SettledAt = &t
	}
	if to == IntentCancelled {
		t := now.UTC()
		p.CancelledAt = &t
	}
	return nil
}

func ValidateCurrency(currency string, scale uint8) error {
	if len(currency) != 3 || strings.ToUpper(currency) != currency {
		return fmt.Errorf("%w: currency must be a three-letter uppercase code", ErrValidation)
	}
	for i := range currency {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return fmt.Errorf("%w: currency must contain only ASCII letters", ErrValidation)
		}
	}
	if scale > 9 {
		return fmt.Errorf("%w: currency scale cannot exceed 9", ErrValidation)
	}
	return nil
}

type RouteStatus string

const (
	RouteProviderOnChain       = "on_chain"
	RouteProviderHostedGateway = "hosted_gateway"
)

const (
	RouteActive     RouteStatus = "active"
	RouteExpired    RouteStatus = "expired"
	RouteSuperseded RouteStatus = "superseded"
	RouteSettled    RouteStatus = "settled"
	RouteCancelled  RouteStatus = "cancelled"
)

type PaymentRoute struct {
	ID                  string       `json:"id"`
	IntentID            string       `json:"intent_id"`
	QuoteID             string       `json:"quote_id,omitempty"`
	AddressAssignmentID string       `json:"address_assignment_id,omitempty"`
	ChainID             string       `json:"chain_id,omitempty"`
	AssetID             string       `json:"asset_id"`
	Provider            string       `json:"provider"`
	ProviderID          string       `json:"provider_id,omitempty"`
	ProviderOrderID     string       `json:"provider_order_id,omitempty"`
	ProviderReference   string       `json:"provider_reference,omitempty"`
	PaymentURL          string       `json:"payment_url,omitempty"`
	ExpectedAmount      money.Amount `json:"expected_amount_atomic"`
	AssetDecimals       uint8        `json:"asset_decimals"`
	DisplayAmount       string       `json:"display_amount"`
	Address             string       `json:"address,omitempty"`
	Memo                string       `json:"memo,omitempty"`
	RequiredFinality    uint64       `json:"required_finality"`
	Status              RouteStatus  `json:"status"`
	Version             int64        `json:"version"`
	StartsAt            time.Time    `json:"starts_at"`
	ExpiresAt           time.Time    `json:"expires_at"`
	GraceEndsAt         time.Time    `json:"grace_ends_at"`
	ReceivedAmount      string       `json:"received_amount,omitempty"`
	RemainingAmount     string       `json:"remaining_amount,omitempty"`
	ExcessAmount        string       `json:"excess_amount,omitempty"`
	PaymentCount        int64        `json:"payment_count,omitempty"`
}

func (r PaymentRoute) ReservationKey() string {
	return strings.Join([]string{r.ChainID, r.Address, r.AssetID, r.ExpectedAmount.String()}, "\x1f")
}
