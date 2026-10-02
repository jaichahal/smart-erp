package approvals

import (
	"context"
	"math/big"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

type fraudFacts struct {
	UsualAmount          string   `json:"usual_amount"`
	OriginalAmount       string   `json:"original_amount"`
	OriginalApprovers    []string `json:"original_approvers"`
	RejectionChain       int      `json:"rejection_chain"`
	CorrectionsThisMonth int      `json:"corrections_this_month"`
	OriginalDocID        string   `json:"original_doc_id"`
}

type fraudConfig struct {
	VariancePercent        string
	RoundAbove             string
	RoundStep              string
	RepeatedRejectionMin   int
	CorrectionsPerMonthMin int
}

func defaultFraud() fraudConfig {
	return fraudConfig{
		VariancePercent:        "10",
		RoundAbove:             "10000",
		RoundStep:              "1000",
		RepeatedRejectionMin:   3,
		CorrectionsPerMonthMin: 3,
	}
}

func hints(ctx context.Context, amount string, facts fraudFacts, cfg fraudConfig, viewerID string) []string {
	out := []string{}
	amt, err := parseAmount(ctx, amount)
	if err != nil {
		return out
	}
	if base := comparisonBase(ctx, facts); base != nil {
		pct, perr := parseAmount(ctx, cfg.VariancePercent)
		if perr == nil && varianceOver(amt, base, pct) {
			out = append(out, t(ctx, "fraud.variance", cfg.VariancePercent))
		}
	}
	minAmt, errMin := parseAmount(ctx, cfg.RoundAbove)
	step, errStep := parseAmount(ctx, cfg.RoundStep)
	if errMin == nil && errStep == nil && isRound(amt, step, minAmt) {
		out = append(out, t(ctx, "fraud.round", cfg.RoundAbove))
	}
	if facts.RejectionChain >= cfg.RepeatedRejectionMin && cfg.RepeatedRejectionMin > 0 {
		out = append(out, t(ctx, "fraud.rejections", facts.RejectionChain))
	}
	if facts.CorrectionsThisMonth >= cfg.CorrectionsPerMonthMin && cfg.CorrectionsPerMonthMin > 0 {
		out = append(out, t(ctx, "fraud.corrections", facts.CorrectionsThisMonth))
	}
	for _, id := range facts.OriginalApprovers {
		if id == viewerID && viewerID != "" {
			out = append(out, t(ctx, "fraud.original_approver"))
			break
		}
	}
	return out
}

func comparisonBase(ctx context.Context, facts fraudFacts) *big.Rat {
	if facts.OriginalAmount != "" {
		if r, err := parseAmount(ctx, facts.OriginalAmount); err == nil && r.Sign() != 0 {
			return r
		}
	}
	if facts.UsualAmount != "" {
		if r, err := parseAmount(ctx, facts.UsualAmount); err == nil && r.Sign() != 0 {
			return r
		}
	}
	return nil
}

func varianceOver(amount, base, percent *big.Rat) bool {
	diff := new(big.Rat).Sub(amount, base)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	ratio := new(big.Rat).Quo(diff, new(big.Rat).Set(base))
	scaled := new(big.Rat).Mul(ratio, big.NewRat(100, 1))
	return scaled.Cmp(percent) > 0
}

func isRound(amount, step, minAmt *big.Rat) bool {
	if step.Sign() == 0 || amount.Cmp(minAmt) < 0 {
		return false
	}
	q := new(big.Rat).Quo(amount, step)
	return q.IsInt()
}

func cardSeverity(docType string, thresholdCrossed bool, hintCount int) oapi.Severity {
	if docType == docBreakGlass || docType == docVendorBank || docType == docBankChange {
		return oapi.CRITICAL
	}
	if thresholdCrossed || hintCount > 0 {
		return oapi.HIGH
	}
	return oapi.MEDIUM
}
