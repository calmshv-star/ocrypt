package providers

import (
	"errors"
	"strings"
)

// CanonicalWatchAddress validates a public receiving address with the same
// chain-specific rules used by the scanner. It never accepts signer material.
func CanonicalWatchAddress(chainID, value string) (string, error) {
	chainID = strings.TrimSpace(chainID)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty watch address")
	}
	switch {
	case strings.HasPrefix(chainID, "eip155:"):
		return canonicalEVMAddress(value)
	case chainID == "tron:mainnet":
		return canonicalTRONAddress(value)
	case chainID == "ton:mainnet":
		return canonicalTONAddress(value)
	case chainID == "solana:mainnet":
		if !validBase58Length(value, 32) {
			return "", errors.New("invalid Solana address")
		}
		return value, nil
	case strings.HasPrefix(chainID, "aptos:"):
		return canonicalAptosAddress(value)
	default:
		return "", errors.New("unsupported watch-only chain")
	}
}

// SolanaPublicKey returns the 32-byte public key represented by a canonical
// Solana address. It is intentionally public-key-only and is used to verify
// wallet ownership proofs without ever accepting signer material.
func SolanaPublicKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if !validBase58Length(value, 32) {
		return nil, errors.New("invalid Solana public key")
	}
	decoded, err := decodeBase58(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid Solana public key")
	}
	return decoded, nil
}
