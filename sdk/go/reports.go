package merchantplatform

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
)

func ReconciliationSignatureMessage(reportID, snapshotSequence string, digest []byte) []byte {
	message := append([]byte("merchant-reconciliation-jsonl-v1\x00"), []byte(reportID)...)
	message = append(message, 0)
	message = append(message, []byte(snapshotSequence)...)
	message = append(message, 0)
	return append(message, digest...)
}

// VerifyReconciliationReport consumes raw, checking exact bytes, size and the key ID frozen on the ready report.
func VerifyReconciliationReport(raw io.Reader, report ReconciliationReport, publicKeys map[string]ed25519.PublicKey) error {
	if report.Status != "ready" {
		return errors.New("report is not ready")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, raw)
	if err != nil {
		return err
	}
	digest := hash.Sum(nil)
	if hex.EncodeToString(digest) != report.ObjectSHA256 {
		return errors.New("reconciliation report digest mismatch")
	}
	if report.ObjectSizeBytes != "" {
		expected, err := strconv.ParseInt(report.ObjectSizeBytes, 10, 64)
		if err != nil || expected != size {
			return errors.New("reconciliation report size mismatch")
		}
	}
	key, ok := publicKeys[report.SigningKeyID]
	if !ok {
		return fmt.Errorf("unknown reconciliation signing key: %s", report.SigningKeyID)
	}
	signature, err := base64.RawURLEncoding.DecodeString(report.Signature)
	if err != nil || !ed25519.Verify(key, ReconciliationSignatureMessage(report.ID, report.SnapshotLedgerSequence, digest), signature) {
		return errors.New("reconciliation report signature mismatch")
	}
	return nil
}
