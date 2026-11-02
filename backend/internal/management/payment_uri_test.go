package management

import (
	"math/big"
	"testing"
	"time"
)

func TestPaymentURIFollowsWalletStandards(t *testing.T) {
	usdt := big.NewInt(12_500_000)
	cases := []struct {
		name, family, chain, kind, contract, recipient string
		decimals                                       int16
		want                                           string
	}{
		{"erc20 on bsc", "evm", "eip155:56", "fungible_token", "0x55d398326f99059fF775485246999027B3197955", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", 6,
			"ethereum:0x55d398326f99059fF775485246999027B3197955@56/transfer?address=0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89&uint256=12500000"},
		{"native eth", "evm", "eip155:1", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", 18,
			"ethereum:0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89@1?value=12500000"},
		{"spl token", "solana", "solana:mainnet", "fungible_token", "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", 6,
			"solana:9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM?amount=12.5&spl-token=Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"},
		{"native sol", "solana", "solana:mainnet", "native", "", "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", 9, "solana:9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM?amount=0.0125"},
		{"native ton", "ton", "ton:mainnet", "native", "", "UQBvW8Z5huBkMJYdnfAEM5JqTNkuWX3diqYENkWsIL0XggGG", 9, "ton://transfer/UQBvW8Z5huBkMJYdnfAEM5JqTNkuWX3diqYENkWsIL0XggGG?amount=12500000"},
	}
	for _, c := range cases {
		if got := paymentURI(c.family, c.chain, c.kind, c.contract, c.recipient, usdt, c.decimals); got != c.want {
			t.Fatalf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestPaymentURIRefusesWhatWalletsWouldMisread(t *testing.T) {
	amount := big.NewInt(1)
	for name, got := range map[string]string{
		"tron has no standard":  paymentURI("tron", "tron:mainnet", "fungible_token", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "TWb4A6kVtQJ4z9Yp2mR7sX8cN1hL5uD3eF", amount, 6),
		"ton jetton":            paymentURI("ton", "ton:mainnet", "fungible_token", "EQCxE6mUtQJKFnGfaROTKOt1lZbDiiX1kCixRv7Nw2Id_sDs", "UQBvW8Z5huBkMJYdnfAEM5JqTNkuWX3diqYENkWsIL0XggGG", amount, 6),
		"solana devnet":         paymentURI("solana", "solana:devnet", "native", "", "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", amount, 9),
		"non-numeric evm chain": paymentURI("evm", "ethereum:mainnet", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", amount, 18),
		"bad evm recipient":     paymentURI("evm", "eip155:1", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce8", amount, 18),
		"bad token contract":    paymentURI("evm", "eip155:56", "fungible_token", "native", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", amount, 18),
		"injected recipient":    paymentURI("evm", "eip155:1", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89&value=1", amount, 18),
		"zero amount":           paymentURI("evm", "eip155:1", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", big.NewInt(0), 18),
		"nil amount":            paymentURI("evm", "eip155:1", "native", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", nil, 18),
		"unknown asset kind":    paymentURI("evm", "eip155:1", "nft", "", "0x8077444bed90f3ca9157ab8bf8d2c51103b2ce89", amount, 18),
	} {
		if got != "" {
			t.Fatalf("%s: got %q", name, got)
		}
	}
}

func TestPaymentURIAmountAsksOnlyForTheRemainder(t *testing.T) {
	for _, c := range []struct{ expected, received, want string }{
		{"1000", "0", "1000"}, {"1000", "", "1000"}, {"1000", "400", "600"},
	} {
		if got := paymentURIAmount(c.expected, c.received); got == nil || got.String() != c.want {
			t.Fatalf("%s-%s: got %v", c.expected, c.received, got)
		}
	}
	for _, c := range [][2]string{{"1000", "1000"}, {"1000", "1200"}, {"0", "0"}, {"x", "0"}} {
		if got := paymentURIAmount(c[0], c[1]); got != nil {
			t.Fatalf("%v: expected no link, got %v", c, got)
		}
	}
}

func TestPaymentURIOnlyForRoutesThatStillAcceptATransfer(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	open, closed := now.Add(time.Minute), now.Add(-time.Second)
	for _, c := range []struct {
		name                string
		routeStatus, public string
		received, topUp     bool
		deadline            time.Time
		want                bool
	}{
		{"untouched pending route", "active", "pending", false, false, open, true},
		{"top-up allowed", "active", "partially_paid", true, true, open, true},
		{"partial without top-up", "active", "partially_paid", true, false, open, false},
		{"something received while pending", "active", "pending", true, false, open, false},
		{"detected", "active", "detected", false, false, open, false},
		{"needs review", "active", "needs_review", false, false, open, false},
		{"settled route", "settled", "settled", true, false, open, false},
		{"expired route", "expired", "expired", false, false, closed, false},
		{"deadline passed", "active", "pending", false, false, closed, false},
		{"top-up after deadline", "active", "partially_paid", true, true, closed, false},
	} {
		if got := routeAcceptsTransfer(c.routeStatus, c.public, c.received, c.topUp, c.deadline, now); got != c.want {
			t.Fatalf("%s: got %v", c.name, got)
		}
	}
}
