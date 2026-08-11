package money

import (
	"encoding/json"
	"testing"
)

func TestAmountRejectsNonCanonicalOrLossyInput(t *testing.T) {
	for _, value := range []string{"", "-1", "+1", "01", "1.0", "1e6", " 1"} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
	var a Amount
	if err := json.Unmarshal([]byte(`9007199254740993`), &a); err == nil {
		t.Fatal("JSON numbers must be rejected")
	}
}

func TestAmountArithmeticIsExactBeyondFloatRange(t *testing.T) {
	a := MustParse("9007199254740993")
	b := MustParse("7")
	sum, err := a.Add(b)
	if err != nil || sum.String() != "9007199254741000" {
		t.Fatalf("unexpected sum %s, %v", sum.String(), err)
	}
	back, err := sum.Sub(b)
	if err != nil || back.Cmp(a) != 0 {
		t.Fatalf("round trip failed: %s, %v", back.String(), err)
	}
	encoded, _ := json.Marshal(sum)
	if string(encoded) != `"9007199254741000"` {
		t.Fatalf("unexpected JSON: %s", encoded)
	}
}

func TestParseDecimalUsesExactConfiguredScale(t *testing.T) {
	for input, want := range map[string]string{"499": "49900", "499.0": "49900", "499.00": "49900", "0.01": "1"} {
		got, err := ParseDecimal(input, 2)
		if err != nil || got.String() != want {
			t.Fatalf("ParseDecimal(%q) = %s, %v; want %s", input, got.String(), err, want)
		}
	}
	for _, input := range []string{"", "0", "00.10", ".10", "1.", "1.001", "1e2", " 1.00", "+1"} {
		if _, err := ParseDecimal(input, 2); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestMulDivFloorUsesWideIntermediate(t *testing.T) {
	maximum := MustParse("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	got, err := maximum.MulDivFloor(MustParse("10000"), MustParse("10000"))
	if err != nil || got.Cmp(maximum) != 0 {
		t.Fatalf("wide exact ratio failed: %s %v", got.String(), err)
	}
	third, err := MustParse("10").MulDivFloor(MustParse("1"), MustParse("3"))
	if err != nil || third.String() != "3" {
		t.Fatalf("floor contract failed: %s %v", third.String(), err)
	}
}

func FuzzAmountRoundTrip(f *testing.F) {
	for _, seed := range []string{"0", "1", "1000000", "9007199254740993"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		a, err := Parse(input)
		if err != nil {
			return
		}
		b, err := Parse(a.String())
		if err != nil || b.Cmp(a) != 0 {
			t.Fatalf("round trip failed")
		}
	})
}
