package admin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

func goLiveChallenge(t *testing.T, chain, display string, now time.Time) GoLiveProofChallenge {
	t.Helper()
	kind, ok := goLiveProofKind(chain)
	if !ok {
		t.Fatalf("no proof for %s", chain)
	}
	canonical, err := providers.CanonicalWatchAddress(chain, display)
	if err != nil {
		t.Fatal(err)
	}
	return GoLiveProofChallenge{Kind: kind, ChainID: chain, Address: canonical, Display: display, TenantID: "11111111-1111-4111-8111-111111111111", MerchantID: "22222222-2222-4222-8222-222222222222", Nonce: "018f22b0-4db4-7c58-8f18-4d2f9d7b6999", IssuedAt: now, ExpiresAt: now.Add(walletProofTTL)}
}

// testTRONSignature signs like TronLink signMessageV2 with private key 1.
func testTRONSignature(t *testing.T, message string) (string, string) {
	t.Helper()
	privateKey := secp256k1.PrivKeyFromBytes(append(make([]byte, 31), 1))
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte("\x19TRON Signed Message:\n" + big.NewInt(int64(len([]byte(message)))).String()))
	_, _ = hasher.Write([]byte(message))
	compact := secp256k1ecdsa.SignCompact(privateKey, hasher.Sum(nil), false)
	keyHasher := sha3.NewLegacyKeccak256()
	_, _ = keyHasher.Write(privateKey.PubKey().SerializeUncompressed()[1:])
	digest := keyHasher.Sum(nil)
	address, err := providers.CanonicalWatchAddress("tron:mainnet", hex.EncodeToString(digest[len(digest)-20:]))
	if err != nil {
		t.Fatal(err)
	}
	return address, "0x" + hex.EncodeToString(append(append([]byte(nil), compact[1:]...), compact[0]))
}

func TestGoLiveProofsBindDomainMerchantNetworkAndAddress(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	// EVM: the key-1 address and a personal_sign signature.
	evm := goLiveChallenge(t, "eip155:56", "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf", now)
	message, err := goLiveProofMessage("admin.example", evm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyGoLiveProof("admin.example", evm, testEVMSignature(t, message), now.Add(time.Minute)); err != nil {
		t.Fatalf("valid EVM proof rejected: %v", err)
	}
	// Solana: ed25519 over the message bytes.
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	solana := goLiveChallenge(t, "solana:mainnet", testBase58(publicKey), now)
	message, _ = goLiveProofMessage("admin.example", solana)
	if _, err := verifyGoLiveProof("admin.example", solana, base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(message))), now.Add(time.Minute)); err != nil {
		t.Fatalf("valid Solana proof rejected: %v", err)
	}
	// TRON: signMessageV2.
	tronAddress, _ := testTRONSignature(t, "")
	tron := goLiveChallenge(t, "tron:mainnet", tronAddress, now)
	message, _ = goLiveProofMessage("admin.example", tron)
	_, tronSignature := testTRONSignature(t, message)
	if _, err := verifyGoLiveProof("admin.example", tron, tronSignature, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid TRON proof rejected: %v", err)
	}

	evmMessage, _ := goLiveProofMessage("admin.example", evm)
	evmSignature := testEVMSignature(t, evmMessage)
	otherMerchant := evm
	otherMerchant.MerchantID = "33333333-3333-4333-8333-333333333333"
	otherNetwork := evm
	otherNetwork.ChainID = "eip155:1"
	otherSigner := goLiveChallenge(t, "eip155:56", "0x8077444bEd90f3cA9157ab8BF8d2C51103b2CE89", now)
	for name, c := range map[string]struct {
		domain    string
		challenge GoLiveProofChallenge
		signature string
		at        time.Time
		want      error
	}{
		"another domain":        {"other.example", evm, evmSignature, now.Add(time.Minute), ErrForbidden},
		"another merchant":      {"admin.example", otherMerchant, evmSignature, now.Add(time.Minute), ErrForbidden},
		"another network":       {"admin.example", otherNetwork, evmSignature, now.Add(time.Minute), ErrForbidden},
		"another signer":        {"admin.example", otherSigner, evmSignature, now.Add(time.Minute), ErrForbidden},
		"expired":               {"admin.example", evm, evmSignature, now.Add(walletProofTTL), ErrExpired},
		"malformed":             {"admin.example", evm, "0x1234", now.Add(time.Minute), ErrInvalid},
		"EVM signature on TRON": {"admin.example", tron, testEVMSignature(t, message), now.Add(time.Minute), ErrForbidden},
	} {
		if _, err := verifyGoLiveProof(c.domain, c.challenge, c.signature, c.at); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v, want %v", name, err, c.want)
		}
	}
	wrongKind := evm
	wrongKind.Kind = walletProofSolana
	if _, err := goLiveProofMessage("admin.example", wrongKind); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a proof kind must match its network: %v", err)
	}
	if _, ok := goLiveProofKind("ton:mainnet"); ok {
		t.Fatal("TON has no supported ownership proof yet")
	}
}
