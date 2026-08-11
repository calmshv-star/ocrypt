package domain

import "time"

type CheckoutStatus string

const (
	CheckoutPending    CheckoutStatus = "pending"
	CheckoutDetected   CheckoutStatus = "detected"
	CheckoutConfirming CheckoutStatus = "confirming"
	CheckoutSettled    CheckoutStatus = "settled"
	CheckoutExpired    CheckoutStatus = "expired"
)

type CheckoutRoute struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	ProviderID      string `json:"provider_id,omitempty"`
	Network         string `json:"network,omitempty"`
	Asset           string `json:"asset"`
	Amount          string `json:"amount"`
	Address         string `json:"address,omitempty"`
	PaymentURL      string `json:"payment_url,omitempty"`
	TransactionHash string `json:"transaction_hash,omitempty"`
	ExplorerURL     string `json:"explorer_url,omitempty"`
}

type CheckoutSession struct {
	IntentID        string          `json:"intent_id"`
	OrderID         string          `json:"order_id"`
	Status          CheckoutStatus  `json:"status"`
	ExpiresAt       time.Time       `json:"expires_at"`
	SelectedRouteID string          `json:"selected_route_id"`
	Routes          []CheckoutRoute `json:"routes"`
	Version         int64           `json:"-"`
}

func CheckoutStatusForIntent(status IntentStatus, expiresAt, now time.Time) CheckoutStatus {
	if now.After(expiresAt) && status != IntentSettled {
		return CheckoutExpired
	}
	switch status {
	case IntentObserved, IntentPartiallyPaid, IntentNeedsReview:
		return CheckoutDetected
	case IntentConfirmed, IntentReorgReview:
		return CheckoutConfirming
	case IntentSettled, IntentOverpaid:
		return CheckoutSettled
	case IntentExpired, IntentCancelled, IntentReversed:
		return CheckoutExpired
	default:
		return CheckoutPending
	}
}
