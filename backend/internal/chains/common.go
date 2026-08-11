package chains

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type EventFactory interface{ NewID() (string, error) }

type Fact struct {
	TransactionID, EventIndex, Kind, AssetID, From, To, Amount string
	Decimals                                                   uint8
	BlockHeight                                                uint64
	BlockHash                                                  string
	BlockTime                                                  time.Time
	Status                                                     domain.TransferStatus
	Evidence                                                   []byte
	Confirmations                                              uint64
}

func normalize(chainID, parser string, factory EventFactory, f Fact) (domain.TransferEvent, error) {
	chainID = strings.TrimSpace(chainID)
	parser = strings.TrimSpace(parser)
	f.TransactionID = strings.TrimSpace(f.TransactionID)
	f.EventIndex = strings.TrimSpace(f.EventIndex)
	f.Kind = strings.TrimSpace(f.Kind)
	f.AssetID = strings.TrimSpace(f.AssetID)
	f.From = strings.TrimSpace(f.From)
	f.To = strings.TrimSpace(f.To)
	f.BlockHash = strings.TrimSpace(f.BlockHash)
	amount, err := money.Parse(f.Amount)
	if err != nil || amount.IsZero() {
		return domain.TransferEvent{}, fmt.Errorf("invalid transfer amount")
	}
	if chainID == "" || parser == "" || f.TransactionID == "" || f.EventIndex == "" || f.Kind == "" || f.AssetID == "" || f.To == "" || f.BlockHash == "" || f.BlockTime.IsZero() {
		return domain.TransferEvent{}, fmt.Errorf("incomplete chain fact")
	}
	identity := domain.EventIdentity{
		ChainID:       chainID,
		TransactionID: f.TransactionID,
		EventIndex:    f.EventIndex,
		AssetID:       f.AssetID,
		ToAddress:     f.To,
	}
	id, err := canonicalEventID(identity)
	if factory != nil {
		id, err = factory.NewID()
	}
	if err != nil {
		return domain.TransferEvent{}, err
	}
	digest := sha256.Sum256(f.Evidence)
	return domain.TransferEvent{ID: id, Identity: identity, Kind: f.Kind, FromAddress: f.From, Amount: amount, AssetDecimals: f.Decimals, BlockHeight: f.BlockHeight, BlockHash: f.BlockHash, OnChainTime: f.BlockTime.UTC().Truncate(time.Microsecond), Confirmations: f.Confirmations, Status: f.Status, ParserVersion: parser, EvidenceHash: hex.EncodeToString(digest[:])}, nil
}

// canonicalHexIdentifier lowercases identifiers only when their complete
// payload is hexadecimal. Base58/base64 identifiers remain case-sensitive.
func canonicalHexIdentifier(value string) string {
	value = strings.TrimSpace(value)
	prefix := ""
	payload := value
	if strings.HasPrefix(payload, "0x") || strings.HasPrefix(payload, "0X") {
		prefix = "0x"
		payload = payload[2:]
	}
	if payload == "" {
		return value
	}
	for _, character := range payload {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return value
		}
	}
	return prefix + strings.ToLower(payload)
}

// canonicalEventID makes adapter retries replay-safe without relying on process
// state. It is an RFC 9562 version-8 UUID derived exclusively from the public
// canonical transfer identity. PostgreSQL's unique identity constraint remains
// the final guard against parser disagreement.
func canonicalEventID(identity domain.EventIdentity) (string, error) {
	key, err := identity.Key()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("merchant-platform/transfer-event/v1\x00" + key))
	b := digest[:16]
	b[6] = (b[6] & 0x0f) | 0x80
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func normalizeFacts(chainID, parser string, factory EventFactory, facts []Fact) ([]domain.TransferEvent, error) {
	events := make([]domain.TransferEvent, 0, len(facts))
	for _, fact := range facts {
		event, err := normalize(chainID, parser, factory, fact)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
func finality(finalized bool) domain.TransferStatus {
	if finalized {
		return domain.TransferFinalized
	}
	return domain.TransferObserved
}
