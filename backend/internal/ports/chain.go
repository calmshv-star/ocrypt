package ports

import (
	"context"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type ChainHead struct {
	Height    uint64
	Hash      string
	Finalized uint64
}

type ScanCursor struct {
	Height uint64
	Token  string
	Hash   string
}

type ScanBatch struct {
	Events     []domain.TransferEvent
	NextCursor ScanCursor
}

// ChainAdapter converts provider-specific data into deterministic canonical
// events. Implementations must make Scan replay-safe and must not advance a
// durable cursor themselves.
type ChainAdapter interface {
	ChainID() string
	CanonicalAddress(input string) (string, error)
	CanonicalTransactionID(input string) (string, error)
	Head(context.Context) (ChainHead, error)
	Scan(context.Context, ScanCursor, uint64) (ScanBatch, error)
	VerifyTransfer(context.Context, domain.EventIdentity) (domain.TransferEvent, error)
}
