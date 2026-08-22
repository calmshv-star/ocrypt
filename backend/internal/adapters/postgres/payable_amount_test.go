package postgres

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestRoundPayableAmountUsesWalletCompatibleAssetPrecision(t *testing.T) {
	tests := []struct {
		name, assetID, input, atomic, display, step string
		decimals                                    uint8
	}{
		{name: "USDT TRC20", assetID: "usdt-tron", input: "14512510", decimals: 6, atomic: "14520000", display: "14.52", step: "10000"},
		{name: "USDC Solana", assetID: "usdc-solana", input: "4297581", decimals: 6, atomic: "4300000", display: "4.3", step: "10000"},
		{name: "TRX", assetID: "trx-tron", input: "17814051", decimals: 6, atomic: "17815000", display: "17.815", step: "1000"},
		{name: "TON", assetID: "ton-ton", input: "4510115000", decimals: 9, atomic: "4511000000", display: "4.511", step: "1000000"},
		{name: "SOL", assetID: "sol-solana", input: "69371600", decimals: 9, atomic: "69400000", display: "0.0694", step: "100000"},
		{name: "ETH", assetID: "eth-ethereum", input: "3164246118977993", decimals: 18, atomic: "3165000000000000", display: "0.003165", step: "1000000000000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rounded, step, err := roundPayableAmount(money.MustParse(test.input), test.decimals, test.assetID)
			if err != nil || rounded.String() != test.atomic || step.String() != test.step || displayAtomic(rounded.String(), test.decimals) != test.display {
				t.Fatalf("unexpected rounding: rounded=%s step=%s display=%s err=%v", rounded.String(), step.String(), displayAtomic(rounded.String(), test.decimals), err)
			}
		})
	}
}

func TestRoundPayableAmountKeepsCollisionSuffixVisible(t *testing.T) {
	tests := []struct {
		assetID, input, rounded, step, next string
		decimals                            uint8
	}{
		{assetID: "usdt-tron", input: "14512510", decimals: 6, rounded: "14.52", step: "10000", next: "14.53"},
		{assetID: "eth-ethereum", input: "3164246118977993", decimals: 18, rounded: "0.003165", step: "1000000000000", next: "0.003166"},
	}
	for _, test := range tests {
		rounded, step, err := roundPayableAmount(money.MustParse(test.input), test.decimals, test.assetID)
		if err != nil || step.String() != test.step || displayAtomic(rounded.String(), test.decimals) != test.rounded {
			t.Fatalf("unexpected %s rounding: rounded=%s step=%s err=%v", test.assetID, displayAtomic(rounded.String(), test.decimals), step.String(), err)
		}
		next, err := rounded.Add(step)
		if err != nil || displayAtomic(next.String(), test.decimals) != test.next {
			t.Fatalf("%s collision suffix was not visible: %s %v", test.assetID, displayAtomic(next.String(), test.decimals), err)
		}
	}
}

func TestPayerFractionalDigitsCoversSupportedAssets(t *testing.T) {
	tests := map[string]uint8{
		"eth-ethereum": 6, "eth-base": 6, "eth-arbitrum": 6, "eth-optimism": 6,
		"bnb-bsc": 6, "avax-avalanche": 4, "sol-solana": 4,
		"trx-tron": 3, "ton-ton": 3, "pol-polygon": 3,
		"usdt-tron": 2, "usdc-solana": 2, "usdt-ton": 2,
		"usdc-ethereum": 2, "usdce-polygon": 2, "usdt-plasma": 2,
	}
	for assetID, expected := range tests {
		if actual := payerFractionalDigits(assetID, 18); actual != expected {
			t.Errorf("payerFractionalDigits(%q)=%d, want %d", assetID, actual, expected)
		}
	}
	if actual := payerFractionalDigits("future-native", 6); actual != 6 {
		t.Fatalf("asset scale must cap the fallback precision: %d", actual)
	}
}

func TestRoundPayableAmountPreservesNativeTokenPrecisionAndTrimsZeros(t *testing.T) {
	rounded, step, err := roundPayableAmount(money.MustParse("10500000"), 6, "future-native")
	if err != nil || rounded.String() != "10500000" || step.String() != "1" || displayAtomic(rounded.String(), 6) != "10.5" {
		t.Fatalf("six-decimal asset changed unexpectedly: rounded=%s step=%s display=%s err=%v", rounded.String(), step.String(), displayAtomic(rounded.String(), 6), err)
	}
}

func TestRoundPayableAmountAlwaysRoundsUp(t *testing.T) {
	rounded, _, err := roundPayableAmount(money.MustParse("1"), 18, "future-native")
	if err != nil || rounded.String() != "10000000000" || displayAtomic(rounded.String(), 18) != "0.00000001" {
		t.Fatalf("tiny amount was not rounded up safely: %s %s %v", rounded.String(), displayAtomic(rounded.String(), 18), err)
	}
}
