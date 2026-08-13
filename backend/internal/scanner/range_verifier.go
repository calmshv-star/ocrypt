package scanner

import (
	"context"
	"errors"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

// RangeVerifier independently re-reads the finalized block containing an
// expected transfer. Its Source is normally a direct provider quorum, so it
// does not trust the scanner database copy being resolved.
type RangeVerifier struct{ source Source }

func NewRangeVerifier(source Source) (*RangeVerifier, error) {
	if source == nil {
		return nil, errors.New("range verifier requires a source")
	}
	return &RangeVerifier{source: source}, nil
}

func (*RangeVerifier) VerifyTransfer(context.Context, domain.EventIdentity) (domain.TransferEvent, error) {
	return domain.TransferEvent{}, errors.New("range verifier requires expected block evidence")
}

func (v *RangeVerifier) VerifyExpectedTransfer(ctx context.Context, expected domain.TransferEvent) (domain.TransferEvent, error) {
	if err := expected.Identity.Validate(); err != nil || expected.BlockHeight == 0 {
		return domain.TransferEvent{}, errors.New("invalid expected transfer block evidence")
	}
	batch, err := v.source.ScanRange(ctx, expected.BlockHeight, expected.BlockHeight)
	if err != nil {
		return domain.TransferEvent{}, err
	}
	wanted, _ := expected.Identity.Key()
	var found *domain.TransferEvent
	for index := range batch.Events {
		candidate := &batch.Events[index]
		key, keyErr := candidate.Identity.Key()
		if keyErr != nil || key != wanted {
			continue
		}
		if found != nil {
			return domain.TransferEvent{}, errors.New("provider range returned duplicate transfer identity")
		}
		copy := *candidate
		found = &copy
	}
	if found == nil {
		return domain.TransferEvent{}, errors.New("provider range did not contain expected transfer")
	}
	return *found, nil
}
