package chains

import (
	"context"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"time"
)

type TRONTransaction struct {
	TransactionID      string
	BlockHeight        uint64
	BlockHash          string
	BlockTime          time.Time
	Success, Finalized bool
	Confirmations      uint64
	Transfers          []TRONTransfer
	RawEvidence        []byte
}
type TRONTransfer struct {
	Index                                      uint32
	From, To, AssetID, ReceivedAmount          string
	Decimals                                   uint8
	Mechanism, FeeDeductedAmount, FeeCollector string
}
type TRONSource interface {
	Transaction(context.Context, string) (TRONTransaction, error)
}
type TRONAdapter struct {
	ChainID string
	Source  TRONSource
	IDs     EventFactory
}

func (a TRONAdapter) Normalize(ctx context.Context, tx string) ([]domain.TransferEvent, error) {
	v, err := a.Source.Transaction(ctx, tx)
	if err != nil {
		return nil, err
	}
	if canonicalHexIdentifier(v.TransactionID) != canonicalHexIdentifier(tx) {
		return nil, fmt.Errorf("TRON provider returned a different transaction")
	}
	if !v.Success {
		return []domain.TransferEvent{}, nil
	}
	f := a.IDs
	var facts []Fact
	for _, x := range v.Transfers {
		kind := "token_transfer"
		if x.Mechanism == "permit" || x.Mechanism == "gasfree" {
			kind = "gasfree_permit_transfer"
		}
		facts = append(facts, Fact{TransactionID: canonicalHexIdentifier(v.TransactionID), EventIndex: fmt.Sprintf("transfer:%d", x.Index), Kind: kind, AssetID: x.AssetID, From: x.From, To: x.To, Amount: x.ReceivedAmount, Decimals: x.Decimals, BlockHeight: v.BlockHeight, BlockHash: canonicalHexIdentifier(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
		if x.FeeDeductedAmount != "" && x.FeeDeductedAmount != "0" {
			facts = append(facts, Fact{TransactionID: canonicalHexIdentifier(v.TransactionID), EventIndex: fmt.Sprintf("gasfree-fee:%d", x.Index), Kind: "gasfree_fee", AssetID: x.AssetID, From: x.From, To: x.FeeCollector, Amount: x.FeeDeductedAmount, Decimals: x.Decimals, BlockHeight: v.BlockHeight, BlockHash: canonicalHexIdentifier(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
		}
	}
	return normalizeFacts(a.ChainID, "tron-v1", f, facts)
}
