package admin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/providers"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

const (
	walletProofEVM    = "evm_personal_sign"
	walletProofSolana = "solana_sign_message"
	walletProofTTL    = 5 * time.Minute
)

func normalizeWalletImport(kind, address string, wallets []WatchWalletImportItem) (string, []WatchWalletImportItem, error) {
	kind = strings.TrimSpace(kind)
	address = strings.TrimSpace(address)
	if len(wallets) == 0 || len(wallets) > 16 {
		return "", nil, ErrInvalid
	}
	items := append([]WatchWalletImportItem(nil), wallets...)
	seen := make(map[string]struct{}, len(items))
	for index := range items {
		item := &items[index]
		item.WalletID = strings.TrimSpace(item.WalletID)
		item.ChainID = strings.TrimSpace(item.ChainID)
		item.DisplayAddress = strings.TrimSpace(item.DisplayAddress)
		if item.DisplayAddress == "" {
			item.DisplayAddress = address
		}
		if item.WalletID == "" || item.ChainID == "" || item.ExpectedVersion < 1 || item.DisplayAddress != address {
			return "", nil, ErrInvalid
		}
		if _, duplicate := seen[item.WalletID]; duplicate {
			return "", nil, ErrInvalid
		}
		seen[item.WalletID] = struct{}{}
		canonical, err := providers.CanonicalWatchAddress(item.ChainID, item.DisplayAddress)
		if err != nil {
			return "", nil, ErrInvalid
		}
		switch kind {
		case walletProofEVM:
			if !strings.HasPrefix(item.ChainID, "eip155:") {
				return "", nil, ErrInvalid
			}
		case walletProofSolana:
			if item.ChainID != "solana:mainnet" {
				return "", nil, ErrInvalid
			}
		default:
			return "", nil, ErrInvalid
		}
		item.CanonicalAddress = canonical
	}
	canonicalAddress := items[0].CanonicalAddress
	for _, item := range items[1:] {
		if item.CanonicalAddress != canonicalAddress {
			return "", nil, ErrInvalid
		}
	}
	slices.SortFunc(items, func(left, right WatchWalletImportItem) int {
		if order := strings.Compare(left.ChainID, right.ChainID); order != 0 {
			return order
		}
		return strings.Compare(left.WalletID, right.WalletID)
	})
	return canonicalAddress, items, nil
}

func walletImportMessage(domain string, challenge WatchWalletImportChallenge) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || len(domain) > 255 || strings.ContainsAny(domain, "\r\n\x00") {
		return "", ErrInvalid
	}
	address, wallets, err := normalizeWalletImport(challenge.Kind, challenge.Address, challenge.Wallets)
	if err != nil {
		return "", err
	}
	if challenge.Nonce == "" || challenge.IssuedAt.IsZero() || challenge.ExpiresAt.IsZero() {
		return "", ErrInvalid
	}
	var message strings.Builder
	message.WriteString("Ocrypt receiving wallet import\n")
	message.WriteString("Domain: ")
	message.WriteString(domain)
	message.WriteString("\nAddress: ")
	message.WriteString(address)
	message.WriteString("\nProof: ")
	message.WriteString(challenge.Kind)
	message.WriteString("\nNetworks:")
	for _, wallet := range wallets {
		message.WriteString("\n- ")
		message.WriteString(wallet.ChainID)
		message.WriteByte('|')
		message.WriteString(wallet.WalletID)
		message.WriteByte('|')
		message.WriteString(strconv.FormatInt(wallet.ExpectedVersion, 10))
	}
	message.WriteString("\nNonce: ")
	message.WriteString(challenge.Nonce)
	message.WriteString("\nIssued at: ")
	message.WriteString(challenge.IssuedAt.UTC().Format(time.RFC3339))
	message.WriteString("\nExpires at: ")
	message.WriteString(challenge.ExpiresAt.UTC().Format(time.RFC3339))
	return message.String(), nil
}

func verifyWalletImportProof(domain string, challenge WatchWalletImportChallenge, signature string, now time.Time) error {
	if now.Before(challenge.IssuedAt.Add(-30*time.Second)) || !now.Before(challenge.ExpiresAt) || challenge.ExpiresAt.Sub(challenge.IssuedAt) != walletProofTTL {
		return ErrExpired
	}
	message, err := walletImportMessage(domain, challenge)
	if err != nil {
		return err
	}
	address, _, err := normalizeWalletImport(challenge.Kind, challenge.Address, challenge.Wallets)
	if err != nil {
		return err
	}
	switch challenge.Kind {
	case walletProofEVM:
		return verifyEVMPersonalSignature(address, message, signature)
	case walletProofSolana:
		return verifySolanaMessageSignature(address, message, signature)
	default:
		return ErrInvalid
	}
}

func verifyEVMPersonalSignature(address, message, signature string) error {
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
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len([]byte(message)))
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte(prefix))
	_, _ = hasher.Write([]byte(message))
	digest := hasher.Sum(nil)
	compact := make([]byte, 65)
	compact[0] = 27 + recovery
	copy(compact[1:33], raw[:32])
	copy(compact[33:], raw[32:64])
	publicKey, _, err := secp256k1ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return ErrInvalid
	}
	serialized := publicKey.SerializeUncompressed()
	addressHasher := sha3.NewLegacyKeccak256()
	_, _ = addressHasher.Write(serialized[1:])
	recovered := addressHasher.Sum(nil)
	want, err := providers.CanonicalWatchAddress("eip155:1", address)
	if err != nil || "0x"+hex.EncodeToString(recovered[len(recovered)-20:]) != want {
		return ErrForbidden
	}
	return nil
}

func verifySolanaMessageSignature(address, message, signature string) error {
	publicKey, err := providers.SolanaPublicKey(address)
	if err != nil {
		return ErrInvalid
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(signature))
	}
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrInvalid
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(message), raw) {
		return ErrForbidden
	}
	return nil
}
