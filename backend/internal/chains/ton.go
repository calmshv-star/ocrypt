package chains

import (
	"context"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"strings"
	"time"
)

type TONTransaction struct {
	Hash               string
	LT                 uint64
	BlockHash          string
	BlockTime          time.Time
	Success, Finalized bool
	Confirmations      uint64
	Messages           []TONMessage
	RawEvidence        []byte
}
type TONMessage struct {
	Index                     uint32
	From, To, AssetID, Amount string
	Decimals                  uint8
	Jetton                    bool
	QueryID                   string
}
type TONSource interface {
	Transaction(context.Context, string) (TONTransaction, error)
}
type TONAdapter struct {
	ChainID string
	Source  TONSource
	IDs     EventFactory
}

func (a TONAdapter) Normalize(ctx context.Context, hash string) ([]domain.TransferEvent, error) {
	v, err := a.Source.Transaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(v.Hash) != strings.TrimSpace(hash) {
		return nil, fmt.Errorf("TON provider returned a different transaction")
	}
	if !v.Success {
		return []domain.TransferEvent{}, nil
	}
	f := a.IDs
	var facts []Fact
	for _, x := range v.Messages {
		kind := "native_message"
		if x.Jetton {
			kind = "jetton_transfer"
		}
		evidence := append(append([]byte(nil), v.RawEvidence...), []byte(x.QueryID)...)
		facts = append(facts, Fact{TransactionID: v.Hash, EventIndex: fmt.Sprintf("message:%d", x.Index), Kind: kind, AssetID: x.AssetID, From: x.From, To: x.To, Amount: x.Amount, Decimals: x.Decimals, BlockHeight: v.LT, BlockHash: v.BlockHash, BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: evidence, Confirmations: v.Confirmations})
	}
	return normalizeFacts(a.ChainID, "ton-v1", f, facts)
}
