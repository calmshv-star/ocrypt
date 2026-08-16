package postgres

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestRoundPayableAmountLimitsWalletInputToEightDecimals(t *testing.T) {
	rounded, step, err := roundPayableAmount(money.MustParse("3164246118977993"), 18)
	if err != nil || rounded.String() != "3164250000000000" || step.String() != "10000000000" || displayAtomic(rounded.String(), 18) != "0.00316425" {
		t.Fatalf("unexpected ETH rounding: rounded=%s step=%s display=%s err=%v", rounded.String(), step.String(), displayAtomic(rounded.String(), 18), err)
	}
	next, err := rounded.Add(step)
	if err != nil || displayAtomic(next.String(), 18) != "0.00316426" {
		t.Fatalf("user-visible reservation step was not preserved: %s %v", displayAtomic(next.String(), 18), err)
	}
}

func TestRoundPayableAmountPreservesNativeTokenPrecisionAndTrimsZeros(t *testing.T) {
	rounded, step, err := roundPayableAmount(money.MustParse("10500000"), 6)
	if err != nil || rounded.String() != "10500000" || step.String() != "1" || displayAtomic(rounded.String(), 6) != "10.5" {
		t.Fatalf("six-decimal asset changed unexpectedly: rounded=%s step=%s display=%s err=%v", rounded.String(), step.String(), displayAtomic(rounded.String(), 6), err)
	}
}

func TestRoundPayableAmountAlwaysRoundsUp(t *testing.T) {
	rounded, _, err := roundPayableAmount(money.MustParse("1"), 18)
	if err != nil || rounded.String() != "10000000000" || displayAtomic(rounded.String(), 18) != "0.00000001" {
		t.Fatalf("tiny amount was not rounded up safely: %s %s %v", rounded.String(), displayAtomic(rounded.String(), 18), err)
	}
}
