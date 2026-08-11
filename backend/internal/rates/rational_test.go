package rates

import (
	"math/big"
	"math/rand/v2"
	"testing"
)

func mustRate(t *testing.T, numerator, denominator string) Rational {
	t.Helper()
	value, err := NewRational(numerator, denominator)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMedianAndSpreadAreExact(t *testing.T) {
	values := []Rational{mustRate(t, "1", "3"), mustRate(t, "2", "3")}
	center, err := median(values)
	if err != nil {
		t.Fatal(err)
	}
	if center.Numerator.String() != "1" || center.Denominator.String() != "3" {
		t.Fatalf("unexpected lower median %s/%s", center.Numerator, center.Denominator)
	}
	spread, allowed, err := spreadBPS(values, center, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if spread != 10000 || !allowed {
		t.Fatalf("spread=%d allowed=%v", spread, allowed)
	}
	_, allowed, err = spreadBPS(values, center, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("rounded boundary must use exact comparison")
	}
}

func TestRationalCanonicalInput(t *testing.T) {
	for _, input := range [][2]string{{"0", "1"}, {"01", "1"}, {"1", "0"}, {"1.0", "1"}, {"-1", "2"}, {"1e2", "1"}} {
		if _, err := NewRational(input[0], input[1]); err == nil {
			t.Fatalf("accepted %q/%q", input[0], input[1])
		}
	}
	value := mustRate(t, "100", "250")
	if value.Numerator.String() != "2" || value.Denominator.String() != "5" {
		t.Fatal("GCD normalization failed")
	}
}

func TestRationalOrderingMatchesBigRatProperty(t *testing.T) {
	for range 5000 {
		a, b, c, d := rand.Int64N(1_000_000)+1, rand.Int64N(1_000_000)+1, rand.Int64N(1_000_000)+1, rand.Int64N(1_000_000)+1
		left := mustRate(t, big.NewInt(a).String(), big.NewInt(b).String())
		right := mustRate(t, big.NewInt(c).String(), big.NewInt(d).String())
		want := new(big.Rat).SetFrac(big.NewInt(a), big.NewInt(b)).Cmp(new(big.Rat).SetFrac(big.NewInt(c), big.NewInt(d)))
		if got := left.Cmp(right); (got < 0) != (want < 0) || (got == 0) != (want == 0) || (got > 0) != (want > 0) {
			t.Fatalf("cmp mismatch")
		}
	}
}

func FuzzRationalNeverAcceptsNonCanonicalOrPanics(f *testing.F) {
	for _, seed := range [][2]string{{"1", "1"}, {"999999999999999999", "3"}, {"0", "0"}, {"NaN", "2"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, n, d string) {
		value, err := NewRational(n, d)
		if err == nil {
			if !canonicalPositiveInteger(n) || !canonicalPositiveInteger(d) || !value.valid() {
				t.Fatal("invalid rational admitted")
			}
			g := new(big.Int).GCD(nil, nil, value.Numerator, value.Denominator)
			if g.Cmp(big.NewInt(1)) != 0 {
				t.Fatal("not normalized")
			}
		}
	})
}
