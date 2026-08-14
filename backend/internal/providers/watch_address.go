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
