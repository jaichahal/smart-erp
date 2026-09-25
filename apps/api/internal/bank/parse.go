package bank

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

type importedLine struct {
	reference string
	amount    int64
	bookedOn  time.Time
	text      string
}

func parseStatement(format, body string) ([]importedLine, error) {
	switch format {
	case "csv":
		return parseCSV(body)
	case "mt940":
		return parseMT940(body)
	default:
		return nil, apierr.New(apierr.ValidationError, "Statement format must be csv or mt940.")
	}
}

func parseCSV(body string) ([]importedLine, error) {
	sc := bufio.NewScanner(strings.NewReader(body))
	var lines []importedLine
	first := true
	for sc.Scan() {
		row := strings.TrimSpace(sc.Text())
		if row == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(strings.ToLower(row), "date,") {
				continue
			}
		}
		parts := strings.Split(row, ",")
		if len(parts) < 3 {
			return nil, apierr.New(apierr.ValidationError, "CSV rows need date, amount, and reference.")
		}
		day, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, apierr.New(apierr.ValidationError, "CSV date must be YYYY-MM-DD.")
		}
		amount, err := ParseAmount(strings.TrimSpace(parts[1]), "AED")
		if err != nil {
			return nil, err
		}
		text := ""
		if len(parts) > 3 {
			text = parts[3]
		}
		lines = append(lines, importedLine{reference: strings.TrimSpace(parts[2]), amount: amount, bookedOn: day, text: text})
	}
	return lines, sc.Err()
}

func parseMT940(body string) ([]importedLine, error) {
	var lines []importedLine
	for _, raw := range strings.Split(body, "\n") {
		row := strings.TrimSpace(raw)
		if !strings.HasPrefix(row, ":61:") {
			continue
		}
		rest := strings.TrimPrefix(row, ":61:")
		if len(rest) < 7 {
			return nil, apierr.New(apierr.ValidationError, "MT940 line 61 is too short.")
		}
		day, err := time.Parse("060102", rest[:6])
		if err != nil {
			return nil, apierr.New(apierr.ValidationError, "MT940 value date is invalid.")
		}
		sign := rest[6]
		amountPart := rest[7:]
		ref := ""
		if i := strings.Index(amountPart, "//"); i >= 0 {
			ref = amountPart[i+2:]
			amountPart = amountPart[:i]
		}
		num := amountPart
		for i, c := range amountPart {
			if (c < '0' || c > '9') && c != ',' {
				num = amountPart[:i]
				if ref == "" {
					ref = strings.TrimLeft(amountPart[i:], "NTRF")
				}
				break
			}
		}
		fils, err := mtAmount(num)
		if err != nil {
			return nil, err
		}
		if sign == 'D' {
			fils = -fils
		}
		if ref == "" {
			return nil, apierr.New(apierr.ValidationError, "MT940 line has no reference.")
		}
		lines = append(lines, importedLine{reference: ref, amount: fils, bookedOn: day})
	}
	return lines, nil
}

func mtAmount(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ",")
	if len(parts) > 2 || parts[0] == "" {
		return 0, apierr.New(apierr.ValidationError, "MT940 amount is invalid.")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, apierr.New(apierr.ValidationError, "MT940 amount is invalid.")
	}
	frac := int64(0)
	if len(parts) == 2 {
		fracText := parts[1]
		if len(fracText) == 1 {
			fracText += "0"
		}
		if len(fracText) != 2 {
			return 0, apierr.New(apierr.ValidationError, "MT940 amount is invalid.")
		}
		frac, err = strconv.ParseInt(fracText, 10, 64)
		if err != nil {
			return 0, apierr.New(apierr.ValidationError, "MT940 amount is invalid.")
		}
	}
	return whole*100 + frac, nil
}
