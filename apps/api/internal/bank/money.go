package bank

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// Money is the wire amount in fils on the books.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Posting is one side of a balanced journal entry, named by account role.
type Posting struct {
	Role   string `json:"role"`
	Debit  Money  `json:"debit"`
	Credit Money  `json:"credit"`
}

// AED formats fils as a two-decimal AED amount.
func AED(fils int64) Money { return Money{Amount: Format(fils), Currency: "AED"} }

// Format renders fils with a sign and two decimal places.
func Format(fils int64) string {
	sign := ""
	if fils < 0 {
		sign = "-"
		fils = -fils
	}
	return sign + strconv.FormatInt(fils/100, 10) + "." + two(fils%100)
}

func two(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// ParseAmount reads a non-negative AED decimal with two fractional digits.
func ParseAmount(amount, currency string) (int64, error) {
	if currency != "" && currency != "AED" {
		return 0, apierr.New(apierr.ValidationError, "Currency must be AED.")
	}
	if strings.HasPrefix(amount, "-") || amount == "" {
		return 0, apierr.New(apierr.ValidationError, "Amount must be a positive decimal.")
	}
	parts := strings.Split(amount, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, apierr.New(apierr.ValidationError, "Amount must be a positive decimal.")
	}
	if len(parts) == 2 && len(parts[1]) != 2 {
		return 0, apierr.New(apierr.ValidationError, "Amount must have two decimal places.")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, apierr.New(apierr.ValidationError, "Amount must be a positive decimal.")
	}
	var frac int64
	if len(parts) == 2 {
		frac, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, apierr.New(apierr.ValidationError, "Amount must be a positive decimal.")
		}
	}
	return whole*100 + frac, nil
}

// SimpleInterest is principal * annual basis points * days / 365, rounded half up.
func SimpleInterest(principal, annualBP, days int64) int64 {
	if principal <= 0 || annualBP <= 0 || days <= 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(principal), big.NewInt(annualBP))
	num.Mul(num, big.NewInt(days))
	den := big.NewInt(10000 * 365)
	half := new(big.Int).Div(den, big.NewInt(2))
	num.Add(num, half)
	return num.Div(num, den).Int64()
}

func side(role string, debit, credit int64) Posting {
	return Posting{Role: role, Debit: AED(debit), Credit: AED(credit)}
}
