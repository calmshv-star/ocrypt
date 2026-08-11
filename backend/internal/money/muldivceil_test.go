package money

import "testing"

func TestMulDivCeilDoesNotUnderchargeNonDivisibleQuote(t *testing.T) {
	result, err := MustParse("5").MulDivCeil(MustParse("10"), MustParse("3"))
	if err != nil {
		t.Fatal(err)
	}
	if result.String() != "17" {
		t.Fatalf("ceil quote = %s, want 17", result.String())
	}
	wide := MustParse("115792089237316195423570985008687907853269984665640564039457584007913129639934")
	result, err = wide.MulDivCeil(MustParse("2"), MustParse("2"))
	if err != nil || result.Cmp(wide) != 0 {
		t.Fatalf("wide exact quote = %s, %v", result.String(), err)
	}
}
