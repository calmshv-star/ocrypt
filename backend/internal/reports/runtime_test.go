package reports

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

const (
	testTenantID = "018f22b0-4db4-7c58-8f18-4d2f9d7b6a11"
	testReportID = "018f22b0-4db4-7c58-8f18-4d2f9d7b6a12"
)

func TestDirectoryPromotionIsCrashReplayAndConcurrencySafe(t *testing.T) {
	store, err := NewDirectoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("{\"record_type\":\"header\"}\n{\"record_type\":\"footer\"}\n")
	digest := sha256.Sum256(body)
	key := objectKey(testTenantID, testReportID)
	sourceDirectory := t.TempDir()
	promote := func(content []byte, expected []byte) error {
		file, err := os.CreateTemp(sourceDirectory, "report-*.tmp")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		if _, err = file.Write(content); err != nil {
			return err
		}
		return store.Promote(context.Background(), key, file, expected, int64(len(content)))
	}
	// Simulates a worker crash after immutable promotion and before DB Complete.
	if err = promote(body, digest[:]); err != nil {
		t.Fatal(err)
	}
	if err = promote(body, digest[:]); err != nil {
		t.Fatalf("identical crash replay was not reusable: %v", err)
	}

	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- promote(body, digest[:])
		}()
	}
	wait.Wait()
	close(errorsFound)
	for promoteErr := range errorsFound {
		if promoteErr != nil {
			t.Fatalf("concurrent identical promotion failed: %v", promoteErr)
		}
	}
	conflict := []byte("different")
	conflictDigest := sha256.Sum256(conflict)
	if err = promote(conflict, conflictDigest[:]); err == nil {
		t.Fatal("different content reused an immutable report object")
	}
}

func TestVerificationKeyRingPreservesOldReportsAndRejectsWrongKeyID(t *testing.T) {
	store, err := NewDirectoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldPublic, oldPrivate, _ := ed25519.GenerateKey(rand.Reader)
	newPublic, newPrivate, _ := ed25519.GenerateKey(rand.Reader)
	runtime, err := NewVerifiedRuntime(store, map[string]ed25519.PublicKey{"recon-2026-01": oldPublic, "recon-2026-08": newPublic}, VerifiedRuntimeConfig{MaxObjectBytes: 1 << 20, TemporaryDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id      string
		keyID   string
		private ed25519.PrivateKey
	}{
		{"018f22b0-4db4-7c58-8f18-4d2f9d7b6a12", "recon-2026-01", oldPrivate},
		{"018f22b0-4db4-7c58-8f18-4d2f9d7b6a13", "recon-2026-08", newPrivate},
	} {
		body := []byte("{\"record_type\":\"header\",\"report_id\":\"" + fixture.id + "\"}\n")
		digest := sha256.Sum256(body)
		file, err := os.CreateTemp(t.TempDir(), "report-*.tmp")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write(body)
		if err = store.Promote(context.Background(), objectKey(testTenantID, fixture.id), file, digest[:], int64(len(body))); err != nil {
			t.Fatal(err)
		}
		file.Close()
		report := domain.ReconciliationReport{ID: fixture.id, TenantID: testTenantID, MerchantID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a14", Status: domain.ReconciliationReportReady, SnapshotLedgerSequence: "42", ObjectSizeBytes: stringInt(len(body)), ObjectSHA256: hex.EncodeToString(digest[:]), SigningKeyID: fixture.keyID}
		report.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.private, signatureMessage(report.ID, report.SnapshotLedgerSequence, digest[:])))
		reader, err := runtime.Open(context.Background(), report)
		if err != nil {
			t.Fatalf("historical key %s did not verify: %v", fixture.keyID, err)
		}
		actual, _ := io.ReadAll(reader)
		reader.Close()
		if string(actual) != string(body) {
			t.Fatal("verified report bytes changed")
		}
		report.SigningKeyID = "unknown-key"
		if _, err = runtime.Open(context.Background(), report); err == nil {
			t.Fatal("signature was accepted under an unknown key ID")
		}
	}
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 20)
	for value > 0 {
		buffer = append(buffer, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}
