package admin

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// TronLink signMessageV2: keccak256 of the TRON prefix, the message length
// in bytes and the message, signed with the account's secp256k1 key.
const walletProofTRON = "tron_sign_message_v2"

// GoLiveProofChallenge asks the owner to sign one receiving address for one
// merchant. The token seals every field, so nothing the browser returns can
// change what was signed.
type GoLiveProofChallenge struct {
	Kind       string    `json:"kind"`
	ChainID    string    `json:"chain_id"`
	Address    string    `json:"address"`
	Display    string    `json:"display_address"`
	TenantID   string    `json:"tenant_id"`
	MerchantID string    `json:"merchant_id"`
	Nonce      string    `json:"nonce"`
	IssuedAt   time.Time `json:"issued_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Message    string    `json:"message,omitempty"`
	Token      string    `json:"token,omitempty"`
}

// goLiveProofKind is the only proof a chain accepts; TON has none yet.
func goLiveProofKind(chainID string) (string, bool) {
	switch {
	case strings.HasPrefix(chainID, "eip155:"):
		return walletProofEVM, true
	case chainID == "solana:mainnet":
		return walletProofSolana, true
	case chainID == "tron:mainnet":
		return walletProofTRON, true
	default:
		return "", false
	}
}

func goLiveProofMessage(domain string, challenge GoLiveProofChallenge) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || len(domain) > 255 || strings.ContainsAny(domain, "\r\n\x00") || challenge.Nonce == "" || challenge.IssuedAt.IsZero() || challenge.ExpiresAt.IsZero() {
		return "", ErrInvalid
	}
	if kind, ok := goLiveProofKind(challenge.ChainID); !ok || kind != challenge.Kind {
		return "", ErrInvalid
	}
	if canonical, err := providers.CanonicalWatchAddress(challenge.ChainID, challenge.Display); err != nil || canonical != challenge.Address {
		return "", ErrInvalid
	}
	return strings.Join([]string{
		"Ocrypt receiving address proof",
		"Domain: " + domain,
		"Merchant: " + challenge.MerchantID,
		"Network: " + challenge.ChainID,
		"Address: " + challenge.Display,
		"Proof: " + challenge.Kind,
		"Nonce: " + challenge.Nonce,
		"Issued at: " + challenge.IssuedAt.UTC().Format(time.RFC3339),
		"Expires at: " + challenge.ExpiresAt.UTC().Format(time.RFC3339),
	}, "\n"), nil
}

func verifyGoLiveProof(domain string, challenge GoLiveProofChallenge, signature string, now time.Time) (string, error) {
	if now.Before(challenge.IssuedAt.Add(-30*time.Second)) || !now.Before(challenge.ExpiresAt) || challenge.ExpiresAt.Sub(challenge.IssuedAt) != walletProofTTL {
		return "", ErrExpired
	}
	message, err := goLiveProofMessage(domain, challenge)
	if err != nil {
		return "", err
	}
	switch challenge.Kind {
	case walletProofEVM:
		err = verifyEVMPersonalSignature(challenge.Display, message, signature)
	case walletProofSolana:
		err = verifySolanaMessageSignature(challenge.Display, message, signature)
	case walletProofTRON:
		err = verifyTRONMessageSignature(challenge.Display, message, signature)
	default:
		err = ErrInvalid
	}
	return message, err
}

func verifyTRONMessageSignature(address, message, signature string) error {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(signature), "0x"))
	if err != nil || len(raw) != 65 {
		return ErrInvalid
	}
	recovery := raw[64]
	if recovery == 27 || recovery == 28 {
		recovery -= 27
	}
	if recovery > 1 {
		return ErrInvalid
	}
	var scalar secp256k1.ModNScalar
	if scalar.SetByteSlice(raw[32:64]) || scalar.IsZero() || scalar.IsOverHalfOrder() {
		return ErrInvalid
	}
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte(fmt.Sprintf("\x19TRON Signed Message:\n%d", len([]byte(message)))))
	_, _ = hasher.Write([]byte(message))
	compact := make([]byte, 65)
	compact[0] = 27 + recovery
	copy(compact[1:33], raw[:32])
	copy(compact[33:], raw[32:64])
	publicKey, _, err := secp256k1ecdsa.RecoverCompact(compact, hasher.Sum(nil))
	if err != nil {
		return ErrInvalid
	}
	serialized := publicKey.SerializeUncompressed()
	keyHasher := sha3.NewLegacyKeccak256()
	_, _ = keyHasher.Write(serialized[1:])
	digest := keyHasher.Sum(nil)
	recovered, err := providers.CanonicalWatchAddress("tron:mainnet", hex.EncodeToString(digest[len(digest)-20:]))
	if err != nil {
		return ErrInvalid
	}
	want, err := providers.CanonicalWatchAddress("tron:mainnet", address)
	if err != nil || recovered != want {
		return ErrForbidden
	}
	return nil
}
