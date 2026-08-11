package webhook

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type PaymentIntentSnapshot struct {
	ID              string       `json:"id"`
	MerchantOrderID string       `json:"merchant_order_id"`
	Status          string       `json:"status"`
	AmountMinor     money.Amount `json:"amount_minor"`
	Currency        string       `json:"currency"`
}

type Settlement struct {
	SettlementID      string                   `json:"settlement_id"`
	AssetID           string                   `json:"asset_id"`
	Network           string                   `json:"network,omitempty"`
	ExpectedRaw       money.Amount             `json:"expected_raw"`
	ReceivedRaw       money.Amount             `json:"received_raw"`
	CreditedRaw       money.Amount             `json:"credited_raw"`
	TransactionHash   string                   `json:"transaction_hash,omitempty"`
	EventIndex        string                   `json:"event_index,omitempty"`
	BlockHeight       string                   `json:"block_height,omitempty"`
	BlockTime         time.Time                `json:"block_time,omitempty"`
	ProviderID        string                   `json:"provider_id,omitempty"`
	ProviderReference string                   `json:"provider_reference,omitempty"`
	ProviderEventID   string                   `json:"provider_event_id,omitempty"`
	ProviderEvidence  string                   `json:"provider_evidence_sha256,omitempty"`
	Finality          string                   `json:"finality"`
	ManualResolution  bool                     `json:"manual_resolution"`
	PolicyVersion     int64                    `json:"policy_version,omitempty"`
	EvidenceHash      string                   `json:"evidence_sha256,omitempty"`
	GasFreeFeesRaw    *money.Amount            `json:"gasfree_fees_raw,omitempty"`
	Contributions     []SettlementContribution `json:"contributions,omitempty"`
}

type SettlementContribution struct {
	TransactionHash string       `json:"transaction_hash"`
	EventIndex      string       `json:"event_index"`
	Role            string       `json:"role"`
	ReceivedRaw     money.Amount `json:"received_raw"`
	CreditedRaw     money.Amount `json:"credited_raw"`
	BlockHeight     string       `json:"block_height"`
	BlockHash       string       `json:"block_hash"`
	BlockTime       time.Time    `json:"block_time"`
	EvidenceHash    string       `json:"source_evidence_sha256"`
}

// Observation is the public, bounded view of a canonical on-chain transfer.
// It deliberately excludes provider response bodies, parser internals and
// operator data. Amounts and heights remain decimal strings so consumers do
// not lose precision in languages whose JSON number type is floating point.
type Observation struct {
	ObservationID         string       `json:"observation_id"`
	PaymentRouteID        string       `json:"payment_route_id"`
	Network               string       `json:"network"`
	AssetID               string       `json:"asset_id"`
	TransactionHash       string       `json:"transaction_hash"`
	EventIndex            string       `json:"event_index"`
	FromAddress           string       `json:"from_address"`
	ToAddress             string       `json:"to_address"`
	AmountRaw             money.Amount `json:"amount_raw"`
	AssetDecimals         uint8        `json:"asset_decimals"`
	BlockHeight           string       `json:"block_height"`
	BlockHash             string       `json:"block_hash"`
	BlockTime             time.Time    `json:"block_time"`
	Confirmations         uint64       `json:"confirmations"`
	RequiredConfirmations uint64       `json:"required_confirmations"`
	Finality              string       `json:"finality"`
	EvidenceHash          string       `json:"evidence_sha256"`
}

// Resolution is the public state of a manual matching decision. Actor IDs,
// free-text reasons, verifier responses and credentials are intentionally not
// part of the merchant webhook contract.
type Resolution struct {
	ResolutionID       string `json:"resolution_id"`
	UnmatchedPaymentID string `json:"unmatched_payment_id"`
	TransferEventID    string `json:"transfer_event_id"`
	PaymentRouteID     string `json:"payment_route_id"`
	Status             string `json:"status"`
	Version            int64  `json:"version"`
	ApprovalRequired   bool   `json:"approval_required"`
	Approved           bool   `json:"approved"`
	AcceptShortfall    bool   `json:"accept_shortfall"`
	AcceptLatePayment  bool   `json:"accept_late_payment"`
	AcceptCrossAsset   bool   `json:"accept_cross_asset"`
	EvidenceVerified   bool   `json:"evidence_verified"`
}

// Event is the stable external webhook schema. It is deliberately distinct
// from the internal event envelope so infrastructure metadata cannot leak into
// a merchant contract by accident.
type Event struct {
	EventID       string                `json:"event_id"`
	EventType     string                `json:"event_type"`
	SchemaVersion string                `json:"schema_version"`
	Sequence      int64                 `json:"sequence"`
	OccurredAt    time.Time             `json:"occurred_at"`
	MerchantID    string                `json:"merchant_id"`
	Livemode      bool                  `json:"livemode"`
	PaymentIntent PaymentIntentSnapshot `json:"payment_intent"`
	Settlement    *Settlement           `json:"settlement,omitempty"`
	Observation   *Observation          `json:"observation,omitempty"`
	Resolution    *Resolution           `json:"resolution,omitempty"`
}

type Acknowledgement struct {
	AcknowledgedEventID string `json:"acknowledged_event_id"`
}

var SupportedEventTypes = []string{
	"payment.intent.created",
	"payment.route.created",
	"payment.observed",
	"payment.confirming",
	"payment.partially_paid",
	"payment.needs_review",
	"payment.settled",
	"payment.overpaid",
	"payment.expired",
	"payment.cancelled",
	"payment.reorged",
	"payment.resolution.updated",
}

func CanonicalBody(event Event) ([]byte, error) {
	if event.EventID == "" || event.EventType == "" || event.SchemaVersion == "" || event.Sequence < 1 || event.MerchantID == "" || event.PaymentIntent.ID == "" {
		return nil, errors.New("webhook event is incomplete")
	}
	supported := false
	for _, eventType := range SupportedEventTypes {
		if event.EventType == eventType {
			supported = true
			break
		}
	}
	if !supported || event.SchemaVersion != "1" {
		return nil, errors.New("unsupported webhook event type or schema version")
	}
	if (event.EventType == "payment.observed" || event.EventType == "payment.confirming") && (event.Observation == nil || event.Settlement != nil || event.Resolution != nil) {
		return nil, errors.New("on-chain lifecycle webhook requires observation or settlement")
	}
	if event.EventType == "payment.reorged" && ((event.Observation == nil) == (event.Settlement == nil) || event.Resolution != nil) {
		return nil, errors.New("reorg webhook requires exactly one observation or settlement")
	}
	if event.EventType == "payment.settled" && (event.Settlement == nil || event.Observation != nil || event.Resolution != nil) {
		return nil, errors.New("settled webhook requires settlement")
	}
	if event.EventType == "payment.resolution.updated" && (event.Resolution == nil || event.Observation != nil || event.Settlement != nil) {
		return nil, errors.New("resolution webhook requires resolution")
	}
	if event.Observation != nil {
		o := event.Observation
		evidence, err := hex.DecodeString(o.EvidenceHash)
		if o.ObservationID == "" || o.PaymentRouteID == "" || o.Network == "" || o.AssetID == "" || o.TransactionHash == "" || o.EventIndex == "" || o.FromAddress == "" || o.ToAddress == "" || o.AmountRaw.IsZero() || o.BlockHeight == "" || o.BlockHash == "" || o.BlockTime.IsZero() || o.RequiredConfirmations < 1 || err != nil || len(evidence) != 32 || hex.EncodeToString(evidence) != o.EvidenceHash || (o.Finality != "observed" && o.Finality != "confirmed" && o.Finality != "finalized" && o.Finality != "reorged") {
			return nil, errors.New("observation webhook is incomplete")
		}
	}
	if event.Resolution != nil {
		r := event.Resolution
		if r.ResolutionID == "" || r.UnmatchedPaymentID == "" || r.TransferEventID == "" || r.PaymentRouteID == "" || r.Status == "" || r.Version < 1 {
			return nil, errors.New("resolution webhook is incomplete")
		}
	}
	return json.Marshal(event)
}

func ValidateAcknowledgement(body []byte, expectedEventID string) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("invalid webhook acknowledgement")
	}
	acknowledged := ""
	seen := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errors.New("invalid webhook acknowledgement")
		}
		key, ok := keyToken.(string)
		if !ok || key != "acknowledged_event_id" || seen {
			return errors.New("invalid webhook acknowledgement")
		}
		if err := decoder.Decode(&acknowledged); err != nil {
			return errors.New("invalid webhook acknowledgement")
		}
		seen = true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("invalid webhook acknowledgement")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return errors.New("invalid webhook acknowledgement")
	}
	if !seen || acknowledged != expectedEventID {
		return errors.New("webhook acknowledgement event ID mismatch")
	}
	return nil
}
