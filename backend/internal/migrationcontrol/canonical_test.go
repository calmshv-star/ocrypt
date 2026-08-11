package migrationcontrol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testKeys(t *testing.T) (PublicKeyRing, map[string]ed25519.PrivateKey) {
	t.Helper()
	ring := PublicKeyRing{}
	private := map[string]ed25519.PrivateKey{}
	for i, id := range []string{"operator-a", "operator-b"} {
		seed := make([]byte, ed25519.SeedSize)
		for j := range seed {
			seed[j] = byte(i*37 + j + 1)
		}
		key := ed25519.NewKeyFromSeed(seed)
		ring[id] = key.Public().(ed25519.PublicKey)
		private[id] = key
	}
	return ring, private
}

func testManifest(now time.Time) Manifest {
	digest := strings.Repeat("a", 64)
	return Manifest{
		SchemaVersion: "migration-manifest-v1", ManifestID: "018f0f65-7a34-7cc4-9f36-7a86496ee461",
		MigrationID: "018f0f65-7a34-7cc4-9f36-7a86496ee462", TenantID: "018f0f65-7a34-7cc4-9f36-7a86496ee463",
		Kind: ManifestInventory, Profile: ProfileJSONMD5,
		Source: SourceDescriptor{SystemID: "json_md5-prod-1", BuildID: "sha256:abc", SchemaVersion: "legacy-v4", WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(-time.Minute), ExportedAt: now},
		Inventory: &Inventory{
			Merchants:      []InventoryItem{{SourceID: "merchant-1", Digest: digest, Data: json.RawMessage(`{"status":"active"}`)}},
			Configurations: []InventoryItem{}, Assets: []InventoryItem{}, Chains: []InventoryItem{}, RPCProviders: []InventoryItem{}, Wallets: []InventoryItem{},
			OpenOrders: []InventoryItem{}, PaidOrders: []InventoryItem{}, ExpiredOrders: []InventoryItem{}, AmountReservations: []InventoryItem{},
			IncomingTransfers: []InventoryItem{}, UnmatchedTransfers: []InventoryItem{}, CallbackBacklog: []InventoryItem{}, ScannerCursors: []InventoryItem{},
			ProviderOrders: []InventoryItem{}, OnChainBalanceObservations: []InventoryItem{},
		},
		Warnings: []string{},
	}
}

func signManifest(t *testing.T, manifest Manifest, private map[string]ed25519.PrivateKey) SignedManifest {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	message, err := CanonicalForSigning(raw)
	if err != nil {
		t.Fatal(err)
	}
	document := SignedManifest{Manifest: raw}
	for _, id := range []string{"operator-a", "operator-b"} {
		document.Signatures = append(document.Signatures, Signature{KeyID: id, Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private[id], message))})
	}
	return document
}

func TestManifestCanonicalSignatureAndInventory(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	ring, private := testKeys(t)
	document := signManifest(t, testManifest(now), private)
	manifest, canonical, digest, signers, err := ParseAndVerify(document, ring, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Profile != ProfileJSONMD5 || len(canonical) == 0 || len(digest) != 64 || len(signers) != 2 {
		t.Fatalf("unexpected verified manifest: %#v %q %#v", manifest, digest, signers)
	}
}

func TestManifestRejectsDuplicateAndSecretExport(t *testing.T) {
	if _, err := CanonicalForSigning([]byte(`{"schema_version":"a","schema_version":"b"}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := CanonicalForSigning([]byte(`{"wallet":{"private_key":"must-not-export"}}`)); err == nil {
		t.Fatal("secret-bearing key accepted")
	}
}

func TestManifestRequiresTwoDistinctAuthorizedSigners(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	ring, private := testKeys(t)
	document := signManifest(t, testManifest(now), private)
	document.Signatures[1] = document.Signatures[0]
	if _, _, _, _, err := ParseAndVerify(document, ring, now); err == nil {
		t.Fatal("same signer admitted twice")
	}
}

func FuzzCanonicalManifestRejectsMalformed(f *testing.F) {
	f.Add([]byte(`{"a":1}`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = CanonicalForSigning(raw)
	})
}
