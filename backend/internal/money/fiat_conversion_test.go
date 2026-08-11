package money

import "testing"

func TestFiatMinorToAssetAtomicUsesInverseMarketPriceAndScales(t *testing.T) {
	// 499.00 RUB / 6256.26 RUB per SOL = 0.079760112... SOL.
	got, err := FiatMinorToAssetAtomic(MustParse("49900"), 2, 9, MustParse("625626"), MustParse("100"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "79760113" {
		t.Fatalf("SOL atomic amount = %s, want 79760113", got.String())
	}
	// A 1% spread increases the crypto amount and remains ceil-rounded.
	withSpread, err := FiatMinorToAssetAtomic(MustParse("49900"), 2, 9, MustParse("625626"), MustParse("100"), 100)
	if err != nil || withSpread.String() != "80557714" {
		t.Fatalf("spread amount = %s, %v", withSpread.String(), err)
	}
}

func TestFiatMinorToAssetAtomicRejectsInvalidOrOverflowingValues(t *testing.T) {
	if _, err := FiatMinorToAssetAtomic(MustParse("1"), 2, 18, Zero(), MustParse("1"), 0); err == nil {
		t.Fatal("zero market price admitted")
	}
	maximum := MustParse("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	if _, err := FiatMinorToAssetAtomic(maximum, 0, 77, MustParse("1"), MustParse("1"), 0); err == nil {
		t.Fatal("overflowing conversion admitted")
	}
}
