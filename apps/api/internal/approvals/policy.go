package approvals

import (
	"context"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
)

const (
	deptAccounts   = "accounts"
	roleAccountant = "accountant"

	stageBelow = "below"
	stageFirst = "first"
	stageFinal = "final"
	stageVote  = "vote"
	stageDone  = "done"

	statePending   = "pending"
	stateApproved  = "approved"
	stateRejected  = "rejected"
	stateDelegated = "delegated"
	statePosted    = "posted"

	postingTTL = 5 * time.Minute

	docVendorBank  = "vendor_bank_change"
	docBankChange  = "bank_change"
	docPaymentRun  = "payment_run"
	docPaymentRel  = "payment_release"
	docCorrection  = "correction"
	docCorrRequest = "correction_request"
	docBreakGlass  = "break_glass"
)

var (
	amountRe   = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	docTypeRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	ulidRe     = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// NonEmptyReason is the shared reason rule for the API, the workflow and any UI
// that rejects without a reason (R2.4, D5). Whitespace is empty.
func NonEmptyReason(ctx context.Context, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return apierr.New(apierr.ValidationError, t(ctx, "reason.required")).WithDetails(map[string]any{"field": "reason"})
	}
	if len(reason) > 2000 {
		return apierr.New(apierr.ValidationError, t(ctx, "reason.too_long")).WithDetails(map[string]any{"field": "reason"})
	}
	return nil
}

func parseAmount(ctx context.Context, raw string) (*big.Rat, error) {
	raw = strings.TrimSpace(raw)
	if !amountRe.MatchString(raw) {
		return nil, apierr.New(apierr.ValidationError, t(ctx, "validation.amount")).WithDetails(map[string]any{"field": "amount"})
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, apierr.New(apierr.ValidationError, t(ctx, "validation.amount")).WithDetails(map[string]any{"field": "amount"})
	}
	return r, nil
}

func contentHash(v any) (string, error) {
	b, err := canon.Marshal(v)
	if err != nil {
		return "", err
	}
	return canon.Hash(b, ""), nil
}

func hasRole(roles []string, want ...string) bool {
	for _, r := range roles {
		for _, w := range want {
			if strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(w)) {
				return true
			}
		}
	}
	return false
}

func inAccounts(department string) bool {
	return strings.EqualFold(strings.TrimSpace(department), deptAccounts)
}

func terminal(state string) bool {
	switch state {
	case stateApproved, stateRejected, statePosted, "cancelled":
		return true
	default:
		return false
	}
}

func alwaysStepUp(docType string) bool {
	switch docType {
	case docVendorBank, docBankChange, docPaymentRun, docPaymentRel, docCorrection, docCorrRequest, docBreakGlass:
		return true
	default:
		return false
	}
}

func stepUpAction(docType string) string {
	switch docType {
	case docVendorBank, docBankChange:
		return "bank_change"
	case docPaymentRun, docPaymentRel:
		return "payment_release"
	case docCorrection, docCorrRequest:
		return "correction"
	case docBreakGlass:
		return "break_glass"
	default:
		return "approval"
	}
}

// Clock supplies the time used for posting-token expiry and delegation windows.
type Clock interface {
	Now() time.Time
}

// RealClock is the wall clock.
type RealClock struct{}

// Now returns the current UTC time.
func (RealClock) Now() time.Time { return time.Now().UTC() }

// FakeClock is a test clock. It is safe for concurrent readers.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a clock frozen at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{t: t.UTC()}
}

// Now returns the frozen time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t.UTC()
	c.mu.Unlock()
}

// Advance moves the clock forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
