package admin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

func proofChallenge(kind, address, chain string, now time.Time) WatchWalletImportChallenge {
	return WatchWalletImportChallenge{
		Kind: kind, Address: address, Nonce: "018f22b0-4db4-7c58-8f18-4d2f9d7b6999", IssuedAt: now, ExpiresAt: now.Add(walletProofTTL),
		Wallets: []WatchWalletImportItem{{WalletID: "018f22b0-4db4-7c58-8f18-4d2f9d7b6a55", ChainID: chain, DisplayAddress: address, ExpectedVersion: 3}},
	}
}

func TestEVMPersonalProofBindsAddressNetworksAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	challenge := proofChallenge(walletProofEVM, "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf", "eip155:1", now)
	message, err := walletImportMessage("admin.example", challenge)
	if err != nil {
		t.Fatal(err)
	}
	encoded := testEVMSignature(t, message)
	if err = verifyWalletImportProof("admin.example", challenge, encoded, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid EVM ownership proof rejected: %v", err)
	}
	challenge.Wallets[0].ExpectedVersion++
	if err = verifyWalletImportProof("admin.example", challenge, encoded, now.Add(time.Minute)); err == nil {
		t.Fatal("proof was not bound to the selected wallet version")
	}
	challenge.Wallets[0].ExpectedVersion--
	if err = verifyWalletImportProof("admin.example", challenge, encoded, challenge.ExpiresAt); err != ErrExpired {
		t.Fatalf("expired proof result = %v, want %v", err, ErrExpired)
	}
}

func testEVMSignature(t *testing.T, message string) string {
	t.Helper()
	privateKey := secp256k1.PrivKeyFromBytes(append(make([]byte, 31), 1))
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte("\x19Ethereum Signed Message:\n" + big.NewInt(int64(len([]byte(message)))).String()))
	_, _ = hasher.Write([]byte(message))
	compact := secp256k1ecdsa.SignCompact(privateKey, hasher.Sum(nil), false)
	signature := append(append([]byte(nil), compact[1:]...), compact[0]-27)
	return "0x" + hex.EncodeToString(signature)
}

func TestSolanaMessageProofUsesAddressPublicKey(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	address := testBase58(publicKey)
	challenge := proofChallenge(walletProofSolana, address, "solana:mainnet", now)
	message, err := walletImportMessage("admin.example", challenge)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(message)))
	if err = verifyWalletImportProof("admin.example", challenge, signature, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid Solana ownership proof rejected: %v", err)
	}
	if err = verifyWalletImportProof("other.example", challenge, signature, now.Add(time.Minute)); err == nil {
		t.Fatal("Solana proof was not bound to the admin domain")
	}
}

func testBase58(value []byte) string {
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	number := new(big.Int).SetBytes(value)
	base := big.NewInt(58)
	zero := new(big.Int)
	mod := new(big.Int)
	encoded := make([]byte, 0, 44)
	for number.Cmp(zero) > 0 {
		number.DivMod(number, base, mod)
		encoded = append(encoded, alphabet[mod.Int64()])
	}
	for _, item := range value {
		if item != 0 {
			break
		}
		encoded = append(encoded, '1')
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}
