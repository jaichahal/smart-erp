package sales

import (
	"math/big"
	"strings"
)

func parseAmt(raw string) (*big.Rat, error) {
	raw = strings.TrimSpace(raw)
	r, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, errAmt
	}
	return r, nil
}

var errAmt = errAmount()

func errAmount() error { return errText("invalid amount") }

type textError string

func (e textError) Error() string { return string(e) }

func errText(s string) error { return textError(s) }

func money(r *big.Rat) string {
	return formatScale(roundHalfUp(r, 2), 2)
}

func formatQty(r *big.Rat) string {
	if r.Sign() == 0 {
		return "0"
	}
	if r.IsInt() {
		return r.Num().String()
	}
	return formatScale(roundHalfUp(r, 4), 4)
}

func roundHalfUp(r *big.Rat, places int) *big.Rat {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	n := new(big.Rat).Mul(new(big.Rat).Set(r), new(big.Rat).SetInt(scale))
	num := new(big.Int).Set(n.Num())
	den := new(big.Int).Set(n.Denom())
	half := new(big.Int).Quo(den, big.NewInt(2))
	if num.Sign() >= 0 {
		num.Add(num, half)
	} else {
		num.Sub(num, half)
	}
	q := new(big.Int).Quo(num, den)
	return new(big.Rat).SetFrac(q, scale)
}

func formatScale(r *big.Rat, places int) string {
	s := new(big.Rat).Set(r)
	f := s.FloatString(places)
	return f
}

func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func sub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }

func cmp(a, b *big.Rat) int { return a.Cmp(b) }

func zero() *big.Rat { return new(big.Rat) }
