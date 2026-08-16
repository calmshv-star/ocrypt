package chains

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"strconv"
	"strings"
	"time"
)

const tonMainnetChainID = "ton:mainnet"

// DisplayAddress converts TON's internal raw account form into the
// non-bounceable mainnet form accepted by payer wallets. Other chains and
// already display-ready values are returned unchanged. Matching and scanner
// code continues to use the canonical raw address stored in payment_routes.
func DisplayAddress(chainID, address string) string {
	if chainID != tonMainnetChainID {
		return address
	}
	friendly, err := TONFriendlyAddress(address)
	if err != nil {
		return address
	}
	return friendly
}

// TONFriendlyAddress encodes a raw workchain:account address as a
// non-bounceable, non-testnet friendly address. Non-bounceable UQ addresses
// are the safest payment instruction for wallet applications because the
// destination account does not have to be initialized first.
func TONFriendlyAddress(raw string) (string, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid raw TON address")
	}
	workchain, err := strconv.ParseInt(parts[0], 10, 8)
	if err != nil {
		return "", fmt.Errorf("invalid raw TON workchain: %w", err)
	}
	account, err := hex.DecodeString(parts[1])
	if err != nil || len(account) != 32 {
		return "", fmt.Errorf("invalid raw TON account")
	}
	payload := make([]byte, 36)
	payload[0] = 0x51 // non-bounceable, mainnet
	payload[1] = byte(int8(workchain))
	copy(payload[2:34], account)
	checksum := tonCRC16XMODEM(payload[:34])
	payload[34] = byte(checksum >> 8)
	payload[35] = byte(checksum)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func tonCRC16XMODEM(data []byte) uint16 {
	var crc uint16
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

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
