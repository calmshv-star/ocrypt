package migrationcontrol

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

type factProvider struct {
	observation ProviderObservation
	err         error
}

func (p factProvider) Observe(context.Context, VerificationRequest) (ProviderObservation, error) {
	return p.observation, p.err
}

func signedObservation(t *testing.T, fact VerificationFact, keyID string, key ed25519.PrivateKey) ProviderObservation {
	t.Helper()
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalForSigning(raw)
	if err != nil {
		t.Fatal(err)
	}
	// CanonicalForSigning uses the manifest domain, so obtain just canonical JSON.
	canonical, _, err = canonicalJSON(raw, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(key, append([]byte(verificationDomain), canonical...))
	return ProviderObservation{Fact: raw, KeyID: keyID, Signature: base64.RawStdEncoding.EncodeToString(signature)}
}

func TestQuorumVerifierRequiresTwoMatchingSignedFacts(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	ring, private := testKeys(t)
	fact := VerificationFact{SchemaVersion: "migration-verification-fact-v1", EventIdentity: "event-1", TransactionID: "tx-1", ChainID: "tron", AssetID: "usdt-trc20", ReceivingAddress: "TAddress", AmountAtomic: "1000000", Confirmations: 32, BlockHash: "00000000000000000000000000000001", ObservedAt: now}
	verifier := QuorumVerifier{Providers: []FactProvider{factProvider{observation: signedObservation(t, fact, "operator-a", private["operator-a"])}, factProvider{observation: signedObservation(t, fact, "operator-b", private["operator-b"])}}, Keys: ring, Quorum: 2, Version: 3, Now: func() time.Time { return now }}
	verified, err := verifier.Verify(context.Background(), VerificationRequest{MigrationID: "018f0f65-7a34-7cc4-9f36-7a86496ee462", SourceID: "paid-1"})
	if err != nil || len(verified.VerifierKeyIDs) != 2 || verified.Fact.AmountAtomic != fact.AmountAtomic {
		t.Fatalf("quorum verification failed: %#v err=%v", verified, err)
	}
}

func TestQuorumVerifierRejectsDivergence(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	ring, private := testKeys(t)
	first := VerificationFact{SchemaVersion: "migration-verification-fact-v1", EventIdentity: "event-1", TransactionID: "tx-1", ChainID: "tron", AssetID: "usdt-trc20", ReceivingAddress: "TAddress", AmountAtomic: "1000000", Confirmations: 32, BlockHash: "00000000000000000000000000000001", ObservedAt: now}
	second := first
	second.AmountAtomic = "2000000"
	verifier := QuorumVerifier{Providers: []FactProvider{factProvider{observation: signedObservation(t, first, "operator-a", private["operator-a"])}, factProvider{observation: signedObservation(t, second, "operator-b", private["operator-b"])}}, Keys: ring, Quorum: 2, Version: 3, Now: func() time.Time { return now }}
	if _, err := verifier.Verify(context.Background(), VerificationRequest{MigrationID: "018f0f65-7a34-7cc4-9f36-7a86496ee462", SourceID: "paid-1"}); err != ErrDependency {
		t.Fatalf("divergent facts result=%v", err)
	}
}
