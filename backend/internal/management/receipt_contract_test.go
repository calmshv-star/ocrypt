package management

import (
	"os"
	"strings"
	"testing"
)

func TestReceiptEvidenceIsDigestOnlyImmutableAndQueuesIndependentProof(t *testing.T) {
	migration, err := os.ReadFile("../../migrations/000029_payment_receipt_evidence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(migration)
	for _, required := range []string{
		"image_sha256 bytea NOT NULL",
		"analysis_sha256 bytea NOT NULL",
		"FOREIGN KEY (proof_id,tenant_id) REFERENCES payment_proofs(id,tenant_id)",
		"payment_receipt_evidence_immutable()",
		"ALTER TABLE payment_receipt_evidence FORCE ROW LEVEL SECURITY",
		"GRANT SELECT,INSERT ON payment_receipt_evidence TO merchant_management_runtime",
		"GRANT SELECT,INSERT ON payment_proofs,idempotency_records TO merchant_management_runtime",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("receipt evidence invariant missing: %s", required)
		}
	}
	for _, forbidden := range []string{"image_body", "image_bytes", "raw_image", "GRANT UPDATE ON payment_receipt_evidence", "GRANT DELETE ON payment_receipt_evidence"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("receipt migration persists mutable/raw evidence: %s", forbidden)
		}
	}
	repository, err := os.ReadFile("postgres_receipts.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(repository)
	for _, required := range []string{"CreatePaymentProof", "receipt-proof-v1", "imageDigest[:]", "ResolveReceiptTarget", "unique_amount_time_transfer", "LIMIT 2"} {
		if !strings.Contains(implementation, required) {
			t.Fatalf("receipt verification boundary missing: %s", required)
		}
	}
	if strings.Contains(string(repository), "FROM payment_receipt_evidence WHERE merchant_id=$1 AND idempotency_key=$2 FOR UPDATE") {
		t.Fatal("immutable receipt evidence must be serialized by advisory lock, not mutable row privilege")
	}
}
