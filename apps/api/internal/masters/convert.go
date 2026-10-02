package masters

import (
	"math/big"
	"strings"
)

// quantize rounds half away from zero to scale decimal places and renders the
// trailing zeros. Every unit conversion rounds with this function once, at the
// end, so purchase, stock, production, and sales paths agree.
func quantize(r *big.Rat, scale int) string {
	if r == nil {
		r = new(big.Rat)
	}
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	num := new(big.Int).Mul(abs.Num(), pow)
	den := new(big.Int).Set(abs.Denom())
	// (2*num + den) / (2*den) is half away from zero for a positive value.
	sum := new(big.Int).Add(new(big.Int).Lsh(num, 1), den)
	q := new(big.Int).Quo(sum, new(big.Int).Lsh(den, 1))
	digits := q.String()
	if scale == 0 {
		if neg && digits != "0" {
			return "-" + digits
		}
		return digits
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	point := len(digits) - scale
	out := digits[:point] + "." + digits[point:]
	if neg && out != "0."+strings.Repeat("0", scale) {
		return "-" + out
	}
	return out
}

func rat(s string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	return r, ok
}

func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func quo(a, b *big.Rat) *big.Rat { return new(big.Rat).Quo(a, b) }

// convertQty converts a quantity between units through the base unit and
// rounds once. factor_to_base is the size of that unit in base units.
func convertQty(qty, fromFactor, toFactor string) (string, bool) {
	q, ok1 := rat(qty)
	from, ok2 := rat(fromFactor)
	to, ok3 := rat(toFactor)
	if !ok1 || !ok2 || !ok3 || to.Sign() == 0 {
		return "", false
	}
	base := mul(q, from)
	return quantize(quo(base, to), qtyScale), true
}
