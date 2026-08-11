package chains

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

type EVMReceipt struct {
	TransactionID      string
	BlockHeight        uint64
	BlockHash          string
	BlockTime          time.Time
	Success, Finalized bool
	Confirmations      uint64
	Native             *EVMNative
	Logs               []EVMLog
	Traces             []EVMTrace
	RawEvidence        []byte
}
type EVMNative struct {
	From, To, Amount, AssetID string
	Decimals                  uint8
}
type EVMLog struct {
	Index                     uint32
	From, To, Amount, AssetID string
	Decimals                  uint8
	Transfer                  bool
}
type EVMTrace struct {
	TraceAddress              []uint32
	From, To, Amount, AssetID string
	Decimals                  uint8
	Success                   bool
}
type EVMSource interface {
	Receipt(context.Context, string) (EVMReceipt, error)
}
type EVMAdapter struct {
	ChainID string
	Source  EVMSource
	IDs     EventFactory
}

func (a EVMAdapter) Normalize(ctx context.Context, tx string) ([]domain.TransferEvent, error) {
	v, err := a.Source.Receipt(ctx, tx)
	if err != nil {
		return nil, err
	}
	if canonicalEVMHex(v.TransactionID) != canonicalEVMHex(tx) {
		return nil, fmt.Errorf("EVM provider returned a different transaction")
	}
	if !v.Success {
		return []domain.TransferEvent{}, nil
	}
	f := a.IDs
	var facts []Fact
	if n := v.Native; n != nil && n.Amount != "0" {
		facts = append(facts, Fact{TransactionID: canonicalEVMHex(v.TransactionID), EventIndex: "native:0", Kind: "native_top_level", AssetID: n.AssetID, From: canonicalEVMHex(n.From), To: canonicalEVMHex(n.To), Amount: n.Amount, Decimals: n.Decimals, BlockHeight: v.BlockHeight, BlockHash: canonicalEVMHex(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
	}
	for _, x := range v.Logs {
		if x.Transfer {
			facts = append(facts, Fact{TransactionID: canonicalEVMHex(v.TransactionID), EventIndex: fmt.Sprintf("log:%d", x.Index), Kind: "token_transfer", AssetID: x.AssetID, From: canonicalEVMHex(x.From), To: canonicalEVMHex(x.To), Amount: x.Amount, Decimals: x.Decimals, BlockHeight: v.BlockHeight, BlockHash: canonicalEVMHex(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
		}
	}
	for _, x := range v.Traces {
		if !x.Success || x.Amount == "0" {
			continue
		}
		parts := make([]string, len(x.TraceAddress))
		for i, n := range x.TraceAddress {
			parts[i] = fmt.Sprint(n)
		}
		facts = append(facts, Fact{TransactionID: canonicalEVMHex(v.TransactionID), EventIndex: "trace:" + strings.Join(parts, ","), Kind: "native_internal", AssetID: x.AssetID, From: canonicalEVMHex(x.From), To: canonicalEVMHex(x.To), Amount: x.Amount, Decimals: x.Decimals, BlockHeight: v.BlockHeight, BlockHash: canonicalEVMHex(v.BlockHash), BlockTime: v.BlockTime, Status: finality(v.Finalized), Evidence: v.RawEvidence, Confirmations: v.Confirmations})
	}
	return normalizeFacts(a.ChainID, "evm-v1", f, facts)
}

func canonicalEVMHex(value string) string {
	return canonicalHexIdentifier(value)
}
