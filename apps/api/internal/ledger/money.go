package ledger

import (
	"fmt"
	"strconv"
	"strings"
)

// fils is one hundredth of an AED.
type fils int64

func parseScale4(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	sign := int64(1)
	if strings.HasPrefix(raw, "-") {
		sign = -1
		raw = raw[1:]
	}
	whole, frac, found := strings.Cut(raw, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 4 {
		return 0, fmt.Errorf("more than four decimal places")
	}
	for len(frac) < 4 {
		frac += "0"
	}
	if !found {
		frac = "0000"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	return sign * (w*10000 + f), nil
}

func roundToFils(scale4 int64) fils {
	return fils(divRound(scale4, 100))
}

func divRound(n, d int64) int64 {
	if d < 0 {
		n, d = -n, -d
	}
	q, r := n/d, n%d
	if r < 0 {
		r = -r
	}
	if r*2 >= d {
		if n >= 0 {
			q++
		} else {
			q--
		}
	}
	return q
}

func (f fils) String() string {
	v := int64(f)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// mulRate rounds fils * rate to the nearest fils, half away from zero.
func mulRate(amount fils, rate string) (fils, error) {
	num, den, err := rateParts(rate)
	if err != nil {
		return 0, err
	}
	if den == 0 {
		return 0, fmt.Errorf("tax rate is invalid")
	}
	return fils(divRound(int64(amount)*num, den)), nil
}

func rateParts(rate string) (num, den int64, err error) {
	rate = strings.TrimSpace(rate)
	if rate == "" {
		return 0, 1, nil
	}
	whole, frac, found := strings.Cut(rate, ".")
	if !found {
		num, err = strconv.ParseInt(whole, 10, 64)
		return num, 1, err
	}
	den = 1
	for i := 0; i < len(frac); i++ {
		den *= 10
	}
	num, err = strconv.ParseInt(whole+frac, 10, 64)
	return num, den, err
}
