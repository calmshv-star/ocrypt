package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func nativeEvidenceFixture() (domain.TransferEvent, []byte) {
	event := domain.TransferEvent{
		ID:       "26df7b59-9da8-8d93-b096-5502d3f554f4",
		Identity: domain.EventIdentity{ChainID: "eip155:1", TransactionID: "0x" + strings.Repeat("a", 64), EventIndex: "native:0", AssetID: "eth-ethereum", ToAddress: "0x" + strings.Repeat("c", 40)},
		Kind:     "native_top_level", FromAddress: "0x" + strings.Repeat("b", 40), Amount: money.MustParse("100"), AssetDecimals: 18,
		BlockHeight: 42, BlockHash: "0x" + strings.Repeat("d", 64), OnChainTime: time.Unix(1000, 0).UTC(), Confirmations: 50, Status: domain.TransferFinalized, ParserVersion: "evm-v1",
	}
	tx := fmt.Sprintf(`{"hash":%q,"from":%q,"to":%q,"value":"0x64","transactionIndex":"0x2"}`, event.Identity.TransactionID, event.FromAddress, event.Identity.ToAddress)
	receipt := fmt.Sprintf(`{"transactionHash":%q,"blockHash":%q,"blockNumber":"0x2a","status":"0x1","transactionIndex":"0x2","logs":[]}`, event.Identity.TransactionID, event.BlockHash)
	event.NativeEVMRawEvidence = []byte(`{"transaction":` + tx + `,"receipt":` + receipt + `}`)
	current := sha256.Sum256(event.NativeEVMRawEvidence)
	event.EvidenceHash = hex.EncodeToString(current[:])
	legacy := sha256.Sum256([]byte(`{"receipt":` + receipt + `}`))
	return event, legacy[:]
}

func TestNativeEVMLegacyEvidenceRequiresBothExactEncodings(t *testing.T) {
	event, legacy := nativeEvidenceFixture()
	if !matchesLegacyNativeEVMEvidence(event, legacy) {
		t.Fatal("same complete receipt in the two supported encodings was rejected")
	}
	for name, mutate := range map[string]func(*domain.TransferEvent){
		"another chain":         func(e *domain.TransferEvent) { e.Identity.ChainID = "ton:mainnet" },
		"invalid EVM chain":     func(e *domain.TransferEvent) { e.Identity.ChainID = "eip155:garbage" },
		"token kind":            func(e *domain.TransferEvent) { e.Kind = "token_transfer" },
		"log identity":          func(e *domain.TransferEvent) { e.Identity.EventIndex = "log:0" },
		"internal transfer":     func(e *domain.TransferEvent) { e.Kind, e.Identity.EventIndex = "native_internal", "trace:0" },
		"different parser":      func(e *domain.TransferEvent) { e.ParserVersion = "evm-v2" },
		"token decimals":        func(e *domain.TransferEvent) { e.AssetDecimals = 6 },
		"not finalized":         func(e *domain.TransferEvent) { e.Status = domain.TransferObserved },
		"different transaction": func(e *domain.TransferEvent) { e.Identity.TransactionID = "0x" + strings.Repeat("e", 64) },
		"different recipient":   func(e *domain.TransferEvent) { e.Identity.ToAddress = "0x" + strings.Repeat("e", 40) },
		"different sender":      func(e *domain.TransferEvent) { e.FromAddress = "0x" + strings.Repeat("e", 40) },
		"different amount":      func(e *domain.TransferEvent) { e.Amount = money.MustParse("101") },
		"different block":       func(e *domain.TransferEvent) { e.BlockHash = "0x" + strings.Repeat("e", 64) },
		"different height":      func(e *domain.TransferEvent) { e.BlockHeight++ },
		"arbitrary new hash":    func(e *domain.TransferEvent) { e.EvidenceHash = strings.Repeat("f", 64) },
		"no proof":              func(e *domain.TransferEvent) { e.NativeEVMRawEvidence = nil },
		"trailing JSON": func(e *domain.TransferEvent) {
			e.NativeEVMRawEvidence = append(e.NativeEVMRawEvidence, []byte(`{}`)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := event
			bad.NativeEVMRawEvidence = append([]byte(nil), event.NativeEVMRawEvidence...)
			mutate(&bad)
			if matchesLegacyNativeEVMEvidence(bad, legacy) {
				t.Fatal("unsupported or changed evidence was accepted")
			}
		})
	}
	if matchesLegacyNativeEVMEvidence(event, bytes.Repeat([]byte{1}, 32)) {
		t.Fatal("arbitrary stored alias was accepted")
	}
}

func TestNativeEVMLegacyProofCannotChangeBoundTransactionOrReceipt(t *testing.T) {
	event, legacy := nativeEvidenceFixture()
	for name, replacement := range map[string][2]string{
		"value":          {`"value":"0x64"`, `"value":"0x65"`},
		"sender":         {`"from":"0x` + strings.Repeat("b", 40) + `"`, `"from":"0x` + strings.Repeat("e", 40) + `"`},
		"failed receipt": {`"status":"0x1"`, `"status":"0x0"`},
		"receipt body":   {`"logs":[]`, `"logs":null`},
		"unknown field":  {`"value":"0x64"`, `"value":"0x64","alias":"anything"`},
		"duplicate key":  {`"value":"0x64"`, `"value":"0x65","value":"0x64"`},
	} {
		t.Run(name, func(t *testing.T) {
			bad := event
			bad.NativeEVMRawEvidence = []byte(strings.Replace(string(event.NativeEVMRawEvidence), replacement[0], replacement[1], 1))
			digest := sha256.Sum256(bad.NativeEVMRawEvidence)
			bad.EvidenceHash = hex.EncodeToString(digest[:])
			if matchesLegacyNativeEVMEvidence(bad, legacy) {
				t.Fatal("valid new digest incorrectly concealed different canonical evidence")
			}
		})
	}
}

func TestNativeEVMProofBytesSurviveJSONBStyleObjectRoundTrip(t *testing.T) {
	event, legacy := nativeEvidenceFixture()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["native_evm_raw_evidence"].(string); !ok {
		t.Fatal("evidence must be base64 text, not a JSON object rewritten by JSONB")
	}
	reordered, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var restored domain.TransferEvent
	if err = json.Unmarshal(reordered, &restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.NativeEVMRawEvidence, event.NativeEVMRawEvidence) || !matchesLegacyNativeEVMEvidence(restored, legacy) {
		t.Fatal("queue serialization changed the proof bytes")
	}
}

type nativeEvidenceRow struct{ values []any }

func (r nativeEvidenceRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan shape")
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}

type nativeEvidenceTx struct {
	pgx.Tx
	existing domain.TransferEvent
	evidence []byte
	native   bool
	updates  []string
}

func (tx *nativeEvidenceTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(sql, "INSERT INTO transfer_events") {
		return pgconn.NewCommandTag("INSERT 0 0"), nil
	}
	if strings.HasPrefix(sql, "UPDATE transfer_events SET status=") {
		tx.updates = append(tx.updates, sql)
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected financial or history write: %s", sql)
}

func (tx *nativeEvidenceTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.Contains(sql, "FROM assets") {
		return nativeEvidenceRow{[]any{tx.native}}
	}
	e := tx.existing
	return nativeEvidenceRow{[]any{e.ID, e.Kind, e.FromAddress, e.Amount.String(), e.AssetDecimals, e.BlockHash, fmt.Sprint(e.BlockHeight), e.OnChainTime, e.Confirmations, string(e.Status), e.ParserVersion, tx.evidence}}
}

func TestCanonicalNativeEVMLegacyReplayPreservesHistory(t *testing.T) {
	event, legacy := nativeEvidenceFixture()
	tx := &nativeEvidenceTx{existing: event, evidence: legacy, native: true}
	for _, confirmations := range []uint64{event.Confirmations, event.Confirmations + 1} {
		reported := event
		reported.Confirmations = confirmations
		actionable, id, canonical, err := insertCanonicalTransfer(t.Context(), tx, reported)
		if err != nil {
			t.Fatal(err)
		}
		if actionable != (confirmations > event.Confirmations) || id != event.ID || canonical.EvidenceHash != hex.EncodeToString(legacy) || canonical.NativeEVMRawEvidence != nil {
			t.Fatalf("wrong canonical replay: actionable=%v id=%s event=%+v", actionable, id, canonical)
		}
	}
	if len(tx.updates) != 1 || strings.Contains(tx.updates[0], "evidence_hash") {
		t.Fatal("replay may only advance finality/confirmations, not replace historical evidence")
	}
	for name, mutate := range map[string]func(*nativeEvidenceTx){
		"token asset":           func(tx *nativeEvidenceTx) { tx.native = false },
		"stored sender changed": func(tx *nativeEvidenceTx) { tx.existing.FromAddress = "other" },
		"stored amount changed": func(tx *nativeEvidenceTx) { tx.existing.Amount = money.MustParse("101") },
		"stored block changed":  func(tx *nativeEvidenceTx) { tx.existing.BlockHeight++ },
		"stored time changed":   func(tx *nativeEvidenceTx) { tx.existing.OnChainTime = tx.existing.OnChainTime.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			tx := &nativeEvidenceTx{existing: event, evidence: legacy, native: true}
			mutate(tx)
			if _, _, _, err := insertCanonicalTransfer(t.Context(), tx, event); !errors.Is(err, domain.ErrInvariantViolation) {
				t.Fatalf("changed canonical facts accepted: %v", err)
			}
			if len(tx.updates) != 0 {
				t.Fatal("rejected replay wrote changes")
			}
		})
	}
}
