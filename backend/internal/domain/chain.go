package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type TransferStatus string

const (
	TransferObserved    TransferStatus = "observed"
	TransferConfirmed   TransferStatus = "confirmed"
	TransferFinalized   TransferStatus = "finalized"
	TransferReorged     TransferStatus = "reorged"
	TransferInvalidated TransferStatus = "invalidated"
)

type EventIdentity struct {
	ChainID       string
	TransactionID string
	EventIndex    string
	AssetID       string
	ToAddress     string
}

func (i EventIdentity) Validate() error {
	if i.ChainID == "" || i.TransactionID == "" || i.EventIndex == "" || i.AssetID == "" || i.ToAddress == "" {
		return errors.New("all transfer identity fields are required")
	}
	return nil
}

// Key distinguishes multiple transfers in one transaction. Chain adapters must
// provide canonical values; this layer never lowercases non-EVM addresses.
func (i EventIdentity) Key() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	payload := strings.Join([]string{i.ChainID, i.TransactionID, i.EventIndex, i.AssetID, i.ToAddress}, "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), nil
}

type TransferEvent struct {
	ID            string         `json:"id"`
	Identity      EventIdentity  `json:"identity"`
	Kind          string         `json:"kind"`
	FromAddress   string         `json:"from_address"`
	Amount        money.Amount   `json:"amount_atomic"`
	AssetDecimals uint8          `json:"asset_decimals"`
	BlockHeight   uint64         `json:"block_height"`
	BlockHash     string         `json:"block_hash"`
	OnChainTime   time.Time      `json:"on_chain_time"`
	Confirmations uint64         `json:"confirmations"`
	Status        TransferStatus `json:"status"`
	ParserVersion string         `json:"parser_version"`
	EvidenceHash  string         `json:"evidence_hash"`
}

func CanTransitionTransfer(from, to TransferStatus) bool {
	switch from {
	case TransferObserved:
		return to == TransferConfirmed || to == TransferFinalized || to == TransferReorged || to == TransferInvalidated
	case TransferConfirmed:
		return to == TransferFinalized || to == TransferReorged || to == TransferInvalidated
	case TransferFinalized:
		return to == TransferReorged
	default:
		return false
	}
}
