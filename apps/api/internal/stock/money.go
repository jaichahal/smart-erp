package stock

import (
	"math/big"
	"strings"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

func parseQty(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, apierr.New(apierr.ValidationError, "quantity is not a decimal")
	}
	return r, nil
}

func formatQty(r *big.Rat) string {
	return roundHalfUp(r, 6).FloatString(6)
}

func roundHalfUp(r *big.Rat, places int) *big.Rat {
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(pow))
	num := new(big.Int).Set(scaled.Num())
	den := new(big.Int).Set(scaled.Denom())
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(new(big.Int).Abs(rem), 1)
	if twice.Cmp(den) >= 0 {
		if num.Sign() >= 0 {
			quo.Add(quo, big.NewInt(1))
		} else {
			quo.Sub(quo, big.NewInt(1))
		}
	}
	return new(big.Rat).SetFrac(quo, pow)
}

func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func sub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }
func neg(a *big.Rat) *big.Rat    { return new(big.Rat).Neg(a) }
func quo(a, b *big.Rat) *big.Rat { return new(big.Rat).Quo(a, b) }
