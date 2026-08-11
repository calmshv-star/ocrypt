package rates

import (
	"errors"
	"math/big"
	"sort"
)

const maxIntegerDigits = 78

func NewRational(numerator, denominator string) (Rational, error) {
	if !canonicalPositiveInteger(numerator) || !canonicalPositiveInteger(denominator) {
		return Rational{}, ErrInvalidConfig
	}
	n, okN := new(big.Int).SetString(numerator, 10)
	d, okD := new(big.Int).SetString(denominator, 10)
	if !okN || !okD || n.Sign() <= 0 || d.Sign() <= 0 {
		return Rational{}, ErrInvalidConfig
	}
	value, err := normalizeBig(n, d)
	if err != nil || value.Numerator.BitLen() > 256 || value.Denominator.BitLen() > 256 {
		return Rational{}, ErrInvalidConfig
	}
	return value, nil
}

func normalizeBig(n, d *big.Int) (Rational, error) {
	if n == nil || d == nil || n.Sign() <= 0 || d.Sign() <= 0 {
		return Rational{}, ErrInvalidConfig
	}
	gcd := new(big.Int).GCD(nil, nil, n, d)
	return Rational{Numerator: new(big.Int).Quo(new(big.Int).Set(n), gcd), Denominator: new(big.Int).Quo(new(big.Int).Set(d), gcd)}, nil
}

func canonicalPositiveInteger(value string) bool {
	if value == "" || len(value) > maxIntegerDigits || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (r Rational) valid() bool {
	return r.Numerator != nil && r.Denominator != nil && r.Numerator.Sign() > 0 && r.Denominator.Sign() > 0
}

func (r Rational) Cmp(other Rational) int {
	left := new(big.Int).Mul(r.Numerator, other.Denominator)
	right := new(big.Int).Mul(other.Numerator, r.Denominator)
	return left.Cmp(right)
}

func median(values []Rational) (Rational, error) {
	if len(values) == 0 {
		return Rational{}, errors.New("empty median")
	}
	ordered := append([]Rational(nil), values...)
	for _, value := range ordered {
		if !value.valid() {
			return Rational{}, ErrInvalidConfig
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Cmp(ordered[j]) < 0 })
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle], nil
	}
	// The lower median is a deterministic observed-source selection. Unlike a
	// midpoint it never synthesizes a wider rational than the provider contract,
	// so it fits the existing exact uint256 planner contract without rounding.
	return ordered[middle-1], nil
}

// spreadBPS returns ceil((max-min)/median*10000), and compares the exact
// rational expression before rounding so the policy boundary is deterministic.
func spreadBPS(values []Rational, center Rational, maximum int64) (int64, bool, error) {
	if len(values) == 0 || !center.valid() || maximum < 0 || maximum > 10000 {
		return 0, false, ErrInvalidConfig
	}
	minimum, maximumValue := values[0], values[0]
	for _, value := range values[1:] {
		if value.Cmp(minimum) < 0 {
			minimum = value
		}
		if value.Cmp(maximumValue) > 0 {
			maximumValue = value
		}
	}
	// diff=(maxN*minD-minN*maxD)/(maxD*minD)
	diffN := new(big.Int).Sub(new(big.Int).Mul(maximumValue.Numerator, minimum.Denominator), new(big.Int).Mul(minimum.Numerator, maximumValue.Denominator))
	diffD := new(big.Int).Mul(maximumValue.Denominator, minimum.Denominator)
	// diff*10000 <= center*maximum
	left := new(big.Int).Mul(diffN, big.NewInt(10000))
	left.Mul(left, center.Denominator)
	right := new(big.Int).Mul(diffD, center.Numerator)
	right.Mul(right, big.NewInt(maximum))
	allowed := left.Cmp(right) <= 0
	// ceil(left ratio before comparison): diffN*10000*centerD / (diffD*centerN)
	denominator := new(big.Int).Mul(diffD, center.Numerator)
	quotient, remainder := new(big.Int).QuoRem(left, denominator, new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, false, ErrDivergent
	}
	return quotient.Int64(), allowed, nil
}
