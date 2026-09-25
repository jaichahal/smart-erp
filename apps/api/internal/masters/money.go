package masters

import "math/big"

func newRat(n int64) *big.Rat { return big.NewRat(n, 1) }

func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }

func neg(a *big.Rat) *big.Rat { return new(big.Rat).Neg(a) }

func mustRat(s string) *big.Rat {
	r, ok := rat(s)
	if !ok {
		return newRat(0)
	}
	return r
}
