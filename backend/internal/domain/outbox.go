package domain

import (
	"encoding/json"
	"time"
)

type EventEnvelope struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	SchemaVersion    string          `json:"schema_version"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateVersion int64           `json:"aggregate_version"`
	Sequence         int64           `json:"sequence"`
	TenantID         string          `json:"tenant_id"`
	MerchantID       string          `json:"merchant_id"`
	CorrelationID    string          `json:"correlation_id,omitempty"`
	CausationID      string          `json:"causation_id,omitempty"`
	OccurredAt       time.Time       `json:"occurred_at"`
	RecordedAt       time.Time       `json:"recorded_at"`
	Payload          json.RawMessage `json:"payload"`
}
