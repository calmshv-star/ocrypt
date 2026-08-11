package retention

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

type DataClass string

const (
	CallbackEventBody    DataClass = "callback_event_body"
	PublishedOutboxBody  DataClass = "published_outbox_payload"
	EventHistoryPayload  DataClass = "event_history_payload"
	manifestVersion                = "retention-archive/v1"
	objectLockCompliance           = "COMPLIANCE"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)

func (c DataClass) Valid() bool {
	switch c {
	case CallbackEventBody, PublishedOutboxBody, EventHistoryPayload:
		return true
	default:
		return false
	}
}

func (c DataClass) Prunable() bool {
	return c == PublishedOutboxBody
}

type Record struct {
	TenantID      string
	MerchantID    string
	SourceTable   string
	RecordID      string
	RecordedAt    time.Time
	OriginalSHA   [sha256.Size]byte
	CanonicalData []byte
}

func (r Record) validate() error {
	if !ids.Valid(r.TenantID) || !ids.Valid(r.MerchantID) || !ids.Valid(r.RecordID) || !identifierPattern.MatchString(r.SourceTable) || r.RecordedAt.IsZero() || len(r.CanonicalData) == 0 {
		return errors.New("archive record identity or canonical data is invalid")
	}
	actual := sha256.Sum256(r.CanonicalData)
	if actual != r.OriginalSHA {
		return errors.New("archive record digest does not match canonical data")
	}
	return nil
}

type Batch struct {
	ID                   string
	TenantID             string
	DataClass            DataClass
	PolicyVersion        int64
	Cutoff               time.Time
	CreatedAt            time.Time
	ObjectRetentionUntil time.Time
	LeaseToken           string
	Fence                int64
	Records              []Record
}

func (b Batch) validate() error {
	if !ids.Valid(b.ID) || !ids.Valid(b.TenantID) || !b.DataClass.Valid() || b.PolicyVersion < 1 || b.Cutoff.IsZero() || b.CreatedAt.IsZero() || !b.ObjectRetentionUntil.After(b.CreatedAt) || !ids.Valid(b.LeaseToken) || b.Fence < 1 || len(b.Records) == 0 {
		return errors.New("archive batch is invalid")
	}
	for _, record := range b.Records {
		if record.TenantID != b.TenantID || record.SourceTable != b.DataClass.sourceTable() {
			return errors.New("archive batch contains a cross-tenant record")
		}
		if err := record.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (c DataClass) sourceTable() string {
	switch c {
	case CallbackEventBody:
		return "callback_events"
	case PublishedOutboxBody:
		return "outbox_events"
	case EventHistoryPayload:
		return "event_history"
	default:
		return ""
	}
}

type ObjectEvidence struct {
	Key            string
	VersionID      string
	ByteLength     int64
	SHA256         [sha256.Size]byte
	ObjectLockMode string
	RetentionUntil time.Time
	AttestedAt     time.Time
}

type PutRequest struct {
	Key            string
	Body           []byte
	SHA256         [sha256.Size]byte
	RetentionUntil time.Time
}

type VerifyRequest struct {
	Key            string
	VersionID      string
	ByteLength     int64
	SHA256         [sha256.Size]byte
	RetentionUntil time.Time
}

type ObjectStore interface {
	PutImmutable(context.Context, PutRequest) (ObjectEvidence, error)
	VerifyImmutable(context.Context, VerifyRequest) (ObjectEvidence, error)
	Ready(context.Context) error
}

type ManifestEvidence struct {
	ManifestSHA256 [sha256.Size]byte
	SigningKeyID   string
	Signature      []byte
}

type PruneClaim struct {
	BatchID    string
	TenantID   string
	DataClass  DataClass
	LeaseToken string
	Fence      int64
	NotBefore  time.Time
	FirstCheck bool
}

type PruneOutcome string

const (
	PruneGraceStarted PruneOutcome = "grace_started"
	PruneCompleted    PruneOutcome = "pruned"
	PruneArchiveOnly  PruneOutcome = "archive_only"
	PruneBlocked      PruneOutcome = "blocked"
)

type Health struct {
	Ready          bool       `json:"ready"`
	LastCycleAt    *time.Time `json:"last_cycle_at,omitempty"`
	PendingBatches int64      `json:"pending_batches"`
	StaleLeases    int64      `json:"stale_leases"`
}

type Repository interface {
	ClaimArchive(context.Context, string, time.Time, time.Duration, int) (Batch, bool, error)
	AcknowledgeArchive(context.Context, Batch, ObjectEvidence, ManifestEvidence, time.Time) error
	FailArchive(context.Context, Batch, string, time.Time) error
	ClaimPrune(context.Context, string, time.Time, time.Duration) (PruneClaim, bool, error)
	AdvancePrune(context.Context, PruneClaim, time.Time) (PruneOutcome, error)
	Health(context.Context, time.Time, time.Duration) (Health, error)
}
