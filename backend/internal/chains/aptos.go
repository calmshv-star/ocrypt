package chains

import (
	"context"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"time"
)

type AptosTransaction struct {
	Hash               string
	Version            uint64
	BlockHash          string
	BlockTime          time.Time
	Success, Finalized bool
	Confirmations      uint64
	Transfers          []AptosTransfer
	RawEvidence        []byte
}
type AptosTransfer struct {
	EventIndex                uint32
	From, To, AssetID, Amount string
	Decimals                  uint8
	FungibleAsset             bool
}
type AptosSource interface {
	Transaction(context.Context, string) (AptosTransaction, error)
}
type AptosAdapter struct {
	ChainID string
	Source  AptosSource
	IDs     EventFactory
}

func (a AptosAdapter) Normalize(ctx context.Context, hash string) ([]domain.TransferEvent, error) {
	v, err := a.Source.Transaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	if canonicalHexIdentifier(v.Hash) != canonicalHexIdentifier(hash) {
		return nil, fmt.Errorf("Aptos provider returned a different transaction")
	}
	if !v.Success {
		return []domain.TransferEvent{}, nil
	}
	f := a.IDs
	var facts []Fact
	for _, x := range v.Transfers {
		kind := "aptos_native_transfer"
		if x.FungibleAsset {
			kind = "aptos_fungible_asset_transfer"
		}
		facts = append(facts, Fact{TransactionID: canonicalHexIdentifier(v.Hash), EventIndex: fmt.Sprintf("event:%d", x.EventIndex), Kind: kind, AssetID: x.AssetID, From: canonicalHexIdentifier(x.From), To: canonicalHexIdentifier(x.To), Amount: x.Amount, Decimals: x.Decimals, BlockHeight: v.Version, BlockHash: canonicalHexIdentifier(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
	}
	return normalizeFacts(a.ChainID, "aptos-v1", f, facts)
}
