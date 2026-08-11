package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var unsignedInteger = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// Amount is an exact non-negative integer number of atomic asset units. It is
// deliberately serialized as a JSON string and never passes through float64.
type Amount struct{ n big.Int }

func Parse(s string) (Amount, error) {
	if !unsignedInteger.MatchString(s) {
		return Amount{}, errors.New("amount must be a canonical unsigned integer string")
	}
	var n big.Int
	if _, ok := n.SetString(s, 10); !ok {
		return Amount{}, errors.New("invalid amount")
	}
	if n.BitLen() > 256 {
		return Amount{}, errors.New("amount exceeds 256 bits")
	}
	return Amount{n: n}, nil
}

func MustParse(s string) Amount {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// ParseDecimal converts a canonical human-readable decimal into atomic units
// without ever passing through a floating-point value. It accepts exactly the
// configured scale (or fewer fractional digits) and rejects signs, exponent
// notation, whitespace, and non-canonical leading zeroes.
func ParseDecimal(value string, scale uint8) (Amount, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "+-eE") {
		return Amount{}, errors.New("amount must be a canonical decimal string")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || !unsignedInteger.MatchString(parts[0]) {
		return Amount{}, errors.New("amount must be a canonical decimal string")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > int(scale) {
			return Amount{}, errors.New("amount has more fractional digits than currency scale")
		}
		for _, character := range fraction {
			if character < '0' || character > '9' {
				return Amount{}, errors.New("amount must contain only decimal digits")
			}
		}
	}
	if scale == 0 && len(parts) == 2 {
		return Amount{}, errors.New("amount has a fraction for a zero-scale currency")
	}
	atomic := parts[0] + fraction + strings.Repeat("0", int(scale)-len(fraction))
	atomic = strings.TrimLeft(atomic, "0")
	if atomic == "" {
		atomic = "0"
	}
	parsed, err := Parse(atomic)
	if err != nil || parsed.IsZero() {
		return Amount{}, errors.New("amount must be positive and fit uint256")
	}
	return parsed, nil
}

func Zero() Amount { return MustParse("0") }

func (a Amount) String() string   { return a.n.String() }
func (a Amount) IsZero() bool     { return a.n.Sign() == 0 }
func (a Amount) Cmp(b Amount) int { return a.n.Cmp(&b.n) }

func (a Amount) Add(b Amount) (Amount, error) {
	var n big.Int
	n.Add(&a.n, &b.n)
	if n.BitLen() > 256 {
		return Amount{}, errors.New("amount overflow")
	}
	return Amount{n: n}, nil
}

func (a Amount) Sub(b Amount) (Amount, error) {
	if a.Cmp(b) < 0 {
		return Amount{}, errors.New("amount underflow")
	}
	var n big.Int
	n.Sub(&a.n, &b.n)
	return Amount{n: n}, nil
}

func (a Amount) MulPow10(power uint8) (Amount, error) {
	var factor big.Int
	factor.Exp(big.NewInt(10), big.NewInt(int64(power)), nil)
	var n big.Int
	n.Mul(&a.n, &factor)
	if n.BitLen() > 256 {
		return Amount{}, errors.New("amount overflow")
	}
	return Amount{n: n}, nil
}

func (a Amount) Mul(b Amount) (Amount, error) {
	var n big.Int
	n.Mul(&a.n, &b.n)
	if n.BitLen() > 256 {
		return Amount{}, errors.New("amount overflow")
	}
	return Amount{n: n}, nil
}

// MulDivFloor computes floor(a*multiplier/divisor) with an unbounded
// intermediate. This avoids false overflow when exact ratio policies operate
// on uint256 values but the final result still fits uint256.
func (a Amount) MulDivFloor(multiplier, divisor Amount) (Amount, error) {
	if divisor.IsZero() {
		return Amount{}, errors.New("division by zero")
	}
	var product, quotient big.Int
	product.Mul(&a.n, &multiplier.n)
	quotient.Quo(&product, &divisor.n)
	if quotient.BitLen() > 256 {
		return Amount{}, errors.New("amount overflow")
	}
	return Amount{n: quotient}, nil
}

// MulDivCeil computes ceil(a*multiplier/divisor) with an unbounded
// intermediate. Quotes use this when the payer amount must never be rounded
// below the exact rational charge.
func (a Amount) MulDivCeil(multiplier, divisor Amount) (Amount, error) {
	if divisor.IsZero() {
		return Amount{}, errors.New("division by zero")
	}
	var product, quotient, remainder big.Int
	product.Mul(&a.n, &multiplier.n)
	quotient.QuoRem(&product, &divisor.n, &remainder)
	if remainder.Sign() > 0 {
		quotient.Add(&quotient, big.NewInt(1))
	}
	if quotient.BitLen() > 256 {
		return Amount{}, errors.New("amount overflow")
	}
	return Amount{n: quotient}, nil
}

func (a Amount) DivCeil(divisor Amount) (Amount, error) {
	if divisor.IsZero() {
		return Amount{}, errors.New("division by zero")
	}
	var quotient, remainder big.Int
	quotient.QuoRem(&a.n, &divisor.n, &remainder)
	if remainder.Sign() > 0 {
		quotient.Add(&quotient, big.NewInt(1))
	}
	return Amount{n: quotient}, nil
}

// FiatMinorToAssetAtomic converts a fiat amount expressed in minor units into
// asset atomic units using a market price expressed as quote-fiat units per one
// whole base asset. Intermediate arithmetic is unbounded; only the final
// atomic amount is constrained to uint256. The result is rounded up so a quote
// never undercharges the merchant.
func FiatMinorToAssetAtomic(fiatMinor Amount, fiatScale, assetDecimals uint8, priceNumerator, priceDenominator Amount, spreadBPS uint32) (Amount, error) {
	if fiatMinor.IsZero() || priceNumerator.IsZero() || priceDenominator.IsZero() || fiatScale > 77 || assetDecimals > 77 || spreadBPS > 10000 {
		return Amount{}, errors.New("invalid fiat-to-asset conversion")
	}
	assetScale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(assetDecimals)), nil)
	fiatDivisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(fiatScale)), nil)
	numerator := new(big.Int).Mul(&fiatMinor.n, &priceDenominator.n)
	numerator.Mul(numerator, assetScale)
	numerator.Mul(numerator, big.NewInt(int64(10000+spreadBPS)))
	denominator := new(big.Int).Mul(&priceNumerator.n, fiatDivisor)
	denominator.Mul(denominator, big.NewInt(10000))
	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if quotient.Sign() <= 0 || quotient.BitLen() > 256 {
		return Amount{}, errors.New("fiat-to-asset conversion outside uint256")
	}
	return Amount{n: *quotient}, nil
}

func (a Amount) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

func (a *Amount) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("amount must be a JSON string")
	}
	v, err := Parse(s)
	if err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	*a = v
	return nil
}
