package management

import (
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
)

var (
	evmAddressPattern    = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	evmChainPattern      = regexp.MustCompile(`^eip155:([1-9][0-9]{0,17})$`)
	solanaAddressPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	tonAddressPattern    = regexp.MustCompile(`^(?:[A-Za-z0-9_-]{48}|-?[0-9]:[0-9a-fA-F]{64})$`)
)

// paymentURI builds the "open in wallet" link for an on-chain route (CHK-2):
// EIP-681 for EVM native coins and ERC-20 tokens, Solana Pay on mainnet, and
// ton://transfer for native TON on mainnet. It returns "" when the network
// has no widely supported standard (TRON, TON jettons) or any input is not
// exact, so the page falls back to the address and amount.
func paymentURI(family, chainID, assetKind, contract, recipient string, amount *big.Int, decimals int16) string {
	if amount == nil || amount.Sign() <= 0 || decimals < 0 {
		return ""
	}
	switch family {
	case "evm":
		chain := evmChainPattern.FindStringSubmatch(chainID)
		if chain == nil || !evmAddressPattern.MatchString(recipient) {
			return ""
		}
		switch assetKind {
		case "native":
			return fmt.Sprintf("ethereum:%s@%s?value=%s", recipient, chain[1], amount.String())
		case "fungible_token":
			if !evmAddressPattern.MatchString(contract) {
				return ""
			}
			return fmt.Sprintf("ethereum:%s@%s/transfer?address=%s&uint256=%s", contract, chain[1], recipient, amount.String())
		}
	case "solana":
		// Solana Pay carries no cluster, so only mainnet routes get a link.
		if chainID != "solana:mainnet" || !solanaAddressPattern.MatchString(recipient) {
			return ""
		}
		link := fmt.Sprintf("solana:%s?amount=%s", recipient, formatAtomicCheckoutAmount(amount, int(decimals)))
		switch assetKind {
		case "native":
			return link
		case "fungible_token":
			if !solanaAddressPattern.MatchString(contract) {
				return ""
			}
			return link + "&spl-token=" + contract
		}
	case "ton":
		if chainID != "ton:mainnet" || assetKind != "native" || !tonAddressPattern.MatchString(recipient) {
			return ""
		}
		return fmt.Sprintf("ton://transfer/%s?amount=%s", url.PathEscape(recipient), amount.String())
	}
	return ""
}

// paymentURIAmount is what the payer still has to send: the remainder of a
// partially paid route, otherwise the expected amount.
func paymentURIAmount(expectedRaw, receivedRaw string) *big.Int {
	expected, ok := new(big.Int).SetString(strings.TrimSpace(expectedRaw), 10)
	if !ok || expected.Sign() <= 0 {
		return nil
	}
	received, ok := new(big.Int).SetString(strings.TrimSpace(receivedRaw), 10)
	if !ok || received.Sign() <= 0 {
		return expected
	}
	remainder := new(big.Int).Sub(expected, received)
	if remainder.Sign() <= 0 {
		return nil
	}
	return remainder
}
