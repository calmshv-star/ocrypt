package retention

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func testSigningKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("retention-test-signing-key-v1"))
	return ed25519.NewKeyFromSeed(seed[:])
}

func testBatch() Batch {
	created := time.Date(2026, 8, 11, 6, 7, 8, 901, time.UTC)
	first := []byte(`{"amount":"639","event":"published"}`)
	second := []byte{0, 1, 2, 3, 255}
	firstDigest, secondDigest := sha256.Sum256(first), sha256.Sum256(second)
	return Batch{
		ID: "61000000-0000-7000-8000-000000000001", TenantID: "10000000-0000-7000-8000-000000000001",
		DataClass: PublishedOutboxBody, PolicyVersion: 3, Cutoff: created.Add(-90 * 24 * time.Hour), CreatedAt: created,
		ObjectRetentionUntil: created.Add(365 * 24 * time.Hour), LeaseToken: "62000000-0000-7000-8000-000000000001", Fence: 7,
		Records: []Record{
			{TenantID: "10000000-0000-7000-8000-000000000001", MerchantID: "20000000-0000-7000-8000-000000000002", SourceTable: "outbox_events", RecordID: "63000000-0000-7000-8000-000000000002", RecordedAt: created.Add(-time.Hour), OriginalSHA: secondDigest, CanonicalData: second},
			{TenantID: "10000000-0000-7000-8000-000000000001", MerchantID: "20000000-0000-7000-8000-000000000001", SourceTable: "outbox_events", RecordID: "63000000-0000-7000-8000-000000000001", RecordedAt: created.Add(-2 * time.Hour), OriginalSHA: firstDigest, CanonicalData: first},
		},
	}
}

func TestBuildArchiveIsDeterministicAcrossSourceOrder(t *testing.T) {
	batch := testBatch()
	first, err := BuildArchive(batch, "retention-v1", testSigningKey())
	if err != nil {
		t.Fatal(err)
	}
	batch.Records[0], batch.Records[1] = batch.Records[1], batch.Records[0]
	second, err := BuildArchive(batch, "retention-v1", testSigningKey())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.ObjectBody, second.ObjectBody) || first.ObjectSHA != second.ObjectSHA || first.Manifest.ManifestSHA256 != second.Manifest.ManifestSHA256 {
		t.Fatal("record query order changed the immutable archive identity")
	}
	if err = VerifyArchive(first.ObjectBody, testSigningKey().Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("verify deterministic archive: %v", err)
	}
}

func TestBuildArchiveRejectsTenantAndDigestDrift(t *testing.T) {
	for name, mutate := range map[string]func(*Batch){
		"cross tenant": func(batch *Batch) { batch.Records[0].TenantID = "10000000-0000-7000-8000-000000000099" },
		"digest":       func(batch *Batch) { batch.Records[0].CanonicalData = append(batch.Records[0].CanonicalData, 0) },
		"duplicate": func(batch *Batch) {
			batch.Records[1].SourceTable = batch.Records[0].SourceTable
			batch.Records[1].RecordID = batch.Records[0].RecordID
		},
	} {
		t.Run(name, func(t *testing.T) {
			batch := testBatch()
			mutate(&batch)
			if _, err := BuildArchive(batch, "retention-v1", testSigningKey()); err == nil {
				t.Fatal("unsafe archive unexpectedly admitted")
			}
		})
	}
}

func TestVerifyArchiveRejectsTampering(t *testing.T) {
	archive, err := BuildArchive(testBatch(), "retention-v1", testSigningKey())
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), archive.ObjectBody...)
	tampered[len(tampered)/2] ^= 1
	if err = VerifyArchive(tampered, testSigningKey().Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("tampered archive verified")
	}
}

func TestVerifyArchiveRejectsNonCanonicalOrRelabeledEnvelope(t *testing.T) {
	archive, err := BuildArchive(testBatch(), "retention-v1", testSigningKey())
	if err != nil {
		t.Fatal(err)
	}
	var envelope archiveEnvelopeWire
	if err = json.Unmarshal(archive.ObjectBody, &envelope); err != nil {
		t.Fatal(err)
	}
	relabeled := envelope
	relabeled.SigningKeyID = "retention-v2"
	relabeledBody, _ := json.Marshal(relabeled)
	relabeledBody = append(relabeledBody, '\n')
	tests := map[string][]byte{
		"case-folded key": bytes.Replace(archive.ObjectBody, []byte(`"format_version"`), []byte(`"Format_version"`), 1),
		"duplicate key":   append([]byte(`{"format_version":"retention-archive/v1",`), archive.ObjectBody[1:]...),
		"unknown key":     append([]byte(`{"unknown":true,`), archive.ObjectBody[1:]...),
		"extra newline":   append(append([]byte(nil), archive.ObjectBody...), '\n'),
		"relabeled key":   relabeledBody,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if verifyErr := VerifyArchive(body, testSigningKey().Public().(ed25519.PublicKey)); verifyErr == nil {
				t.Fatal("non-canonical or relabeled archive verified")
			}
		})
	}
}

func FuzzBuildArchiveDeterministic(f *testing.F) {
	f.Add([]byte("alpha"), []byte("beta"))
	f.Add([]byte{0, 255}, []byte(`{"b":2,"a":1}`))
	f.Fuzz(func(t *testing.T, left, right []byte) {
		if len(left) == 0 || len(right) == 0 || len(left)+len(right) > 4096 {
			t.Skip()
		}
		batch := testBatch()
		batch.Records[0].CanonicalData = append([]byte(nil), left...)
		batch.Records[0].OriginalSHA = sha256.Sum256(left)
		batch.Records[1].CanonicalData = append([]byte(nil), right...)
		batch.Records[1].OriginalSHA = sha256.Sum256(right)
		first, err := BuildArchive(batch, "retention-v1", testSigningKey())
		if err != nil {
			t.Fatal(err)
		}
		batch.Records[0], batch.Records[1] = batch.Records[1], batch.Records[0]
		second, err := BuildArchive(batch, "retention-v1", testSigningKey())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.ObjectBody, second.ObjectBody) {
			t.Fatal("archive is not deterministic")
		}
	})
}

func FuzzVerifyArchiveRejectsMutation(f *testing.F) {
	archive, _ := BuildArchive(testBatch(), "retention-v1", testSigningKey())
	f.Add(uint16(1), byte(1))
	f.Add(uint16(len(archive.ObjectBody)/2), byte(255))
	f.Fuzz(func(t *testing.T, offset uint16, value byte) {
		if len(archive.ObjectBody) == 0 {
			t.Fatal("empty fixture")
		}
		mutated := append([]byte(nil), archive.ObjectBody...)
		index := int(offset) % len(mutated)
		if mutated[index] == value {
			value ^= 1
		}
		mutated[index] = value
		if err := VerifyArchive(mutated, testSigningKey().Public().(ed25519.PublicKey)); err == nil {
			t.Fatal("mutated archive verified")
		}
	})
}
