package rates

import (
	"encoding/json"
	"errors"
	"math/big"
	"time"
)

var (
	ErrInvalidConfig = errors.New("invalid active rate configuration")
	ErrUnavailable   = errors.New("rate dependency unavailable")
	ErrNoQuorum      = errors.New("rate source quorum not reached")
	ErrStale         = errors.New("rate observation is stale")
	ErrFuture        = errors.New("rate observation timestamp is in the future")
	ErrDivergent     = errors.New("rate source spread exceeds policy")
	ErrLeaseLost     = errors.New("rate collection lease lost")
	ErrDisabled      = errors.New("rate workload identity disabled")
)

// Rational is an exact, positive price. Both integers are normalized by GCD.
// It deliberately has no float conversion API.
type Rational struct {
	Numerator   *big.Int
	Denominator *big.Int
}

type Target struct {
	TenantID  string `json:"tenant_id,omitempty"`
	PolicyKey string `json:"policy_key"`
}

type SourceConfig struct {
	Key              string
	ProviderRef      string
	Endpoint         string
	BaseAsset        string
	QuoteAsset       string
	CredentialRef    string
	MaxAge           time.Duration
	Timeout          time.Duration
	MaxResponseBytes int64
	SnapshotID       string
	FenceToken       int64
}

type PolicyConfig struct {
	Key             string
	BaseAsset       string
	QuoteAsset      string
	SourceKeys      []string
	Quorum          int
	MaxAge          time.Duration
	MaxSpreadBPS    int64
	FutureTolerance time.Duration
	PollInterval    time.Duration
	SnapshotID      string
	SnapshotVersion int64
	FenceToken      int64
}

type RuntimeConfig struct {
	TenantID string
	Policy   PolicyConfig
	Sources  []SourceConfig
}

type ProviderResult struct {
	BaseAsset             string    `json:"base_asset"`
	QuoteAsset            string    `json:"quote_asset"`
	PriceNumerator        string    `json:"price_numerator"`
	PriceDenominator      string    `json:"price_denominator"`
	ObservedAt            time.Time `json:"observed_at"`
	ProviderObservationID string    `json:"provider_observation_id"`
	Raw                   []byte    `json:"-"`
}

type Observation struct {
	ID                    string
	TenantID              string
	PolicyKey             string
	SourceKey             string
	ProviderRef           string
	ProviderObservationID string
	BaseAsset             string
	QuoteAsset            string
	Price                 Rational
	ProviderObservedAt    time.Time
	ReceivedAt            time.Time
	RawResponseHash       [32]byte
	SourceSnapshotID      string
	SourceFenceToken      int64
}

type Tick struct {
	ID               string
	TenantID         string
	PolicyKey        string
	BaseAsset        string
	QuoteAsset       string
	Price            Rational
	ObservedAt       time.Time
	AdmittedAt       time.Time
	ExpiresAt        time.Time
	SpreadBPS        int64
	Quorum           int
	SourceCount      int
	PolicySnapshotID string
	PolicyFenceToken int64
	SourcesDigest    [32]byte
}

type Claim struct {
	Target     Target
	ClaimToken int64
	Attempts   int
}

type Collection struct {
	Claim        Claim
	Config       RuntimeConfig
	Observations []Observation
	Tick         Tick
}

type DeadLetter struct {
	Target    Target
	ErrorCode string
	Attempts  int
	At        time.Time
}

type Health struct {
	Ready               bool      `json:"ready"`
	DatabaseReady       bool      `json:"database_ready"`
	ConfiguredTargets   int       `json:"configured_targets"`
	FreshTargets        int       `json:"fresh_targets"`
	DeadLetteredTargets int       `json:"dead_lettered_targets"`
	OldestTickAt        time.Time `json:"oldest_tick_at,omitempty"`
}

// Config payload structs are intentionally private to keep snapshots from
// leaking provider-specific or secret-bearing fields into the domain model.
type sourcePayload struct {
	ProviderRef      string `json:"provider_ref"`
	Endpoint         string `json:"endpoint"`
	BaseAsset        string `json:"base_asset"`
	QuoteAsset       string `json:"quote_asset"`
	CredentialRef    string `json:"credential_ref,omitempty"`
	MaxAgeSeconds    int64  `json:"max_age_seconds"`
	TimeoutMS        int64  `json:"timeout_ms,omitempty"`
	MaxResponseBytes int64  `json:"max_response_bytes,omitempty"`
}

type policyPayload struct {
	BaseAsset              string   `json:"base_asset"`
	QuoteAsset             string   `json:"quote_asset"`
	Sources                []string `json:"sources"`
	Quorum                 int      `json:"quorum"`
	MaxAgeSeconds          int64    `json:"max_age_seconds"`
	MaxSpreadBPS           int64    `json:"max_spread_bps"`
	FutureToleranceSeconds *int64   `json:"future_tolerance_seconds,omitempty"`
	PollIntervalSeconds    *int64   `json:"poll_interval_seconds,omitempty"`
}

func strictDecode(raw json.RawMessage, target any) error {
	return decodeStrict(raw, target)
}
