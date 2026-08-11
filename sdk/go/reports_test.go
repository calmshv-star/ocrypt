package merchantplatform

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"
)

func TestVerifyReconciliationReport(t *testing.T) {
	raw := []byte("{\"record_type\":\"header\"}\n{\"record_type\":\"footer\"}\n")
	digest := sha256.Sum256(raw)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	report := ReconciliationReport{ID: "report-id", Status: "ready", SnapshotLedgerSequence: "42", ObjectSHA256: hex.EncodeToString(digest[:]), ObjectSizeBytes: strconv.Itoa(len(raw)), SigningKeyID: "key-2026", Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, ReconciliationSignatureMessage("report-id", "42", digest[:])))}
	if err = VerifyReconciliationReport(bytes.NewReader(raw), report, map[string]ed25519.PublicKey{"key-2026": public}); err != nil {
		t.Fatal(err)
	}
	report.SigningKeyID = "unknown"
	if VerifyReconciliationReport(bytes.NewReader(raw), report, map[string]ed25519.PublicKey{"key-2026": public}) == nil {
		t.Fatal("unknown historical key was accepted")
	}
}
