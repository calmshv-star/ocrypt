package chains

import (
	"context"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"strings"
	"time"
)

type SolanaTransaction struct {
	Signature          string
	Slot               uint64
	BlockHash          string
	BlockTime          time.Time
	Success, Finalized bool
	Confirmations      uint64
	Transfers          []SolanaTransfer
	RawEvidence        []byte
}
type SolanaTransfer struct {
	OuterIndex                         uint32
	InnerIndex                         *uint32
	Program, From, To, AssetID, Amount string
	Decimals                           uint8
	Native                             bool
}
type SolanaSource interface {
	Transaction(context.Context, string) (SolanaTransaction, error)
}
type SolanaAdapter struct {
	ChainID string
	Source  SolanaSource
	IDs     EventFactory
}

func (a SolanaAdapter) Normalize(ctx context.Context, signature string) ([]domain.TransferEvent, error) {
	v, err := a.Source.Transaction(ctx, signature)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(v.Signature) != strings.TrimSpace(signature) {
		return nil, fmt.Errorf("Solana provider returned a different transaction")
	}
	if !v.Success {
		return []domain.TransferEvent{}, nil
	}
	f := a.IDs
	var facts []Fact
	for _, x := range v.Transfers {
		index := fmt.Sprintf("instruction:%d", x.OuterIndex)
		if x.InnerIndex != nil {
			index = fmt.Sprintf("instruction:%d/inner:%d", x.OuterIndex, *x.InnerIndex)
		}
		kind := "spl_transfer"
		if x.Native {
			kind = "native_top_level"
		} else if x.Program == "spl-token-2022" {
			kind = "token2022_transfer"
		}
		facts = append(facts, Fact{TransactionID: v.Signature, EventIndex: index, Kind: kind, AssetID: x.AssetID, From: x.From, To: x.To, Amount: x.Amount, Decimals: x.Decimals, BlockHeight: v.Slot, BlockHash: v.BlockHash, BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
	}
	return normalizeFacts(a.ChainID, "solana-v1", f, facts)
}
