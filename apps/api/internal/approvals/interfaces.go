package approvals

import (
	"context"
	"strings"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// StepUpVerifier checks a step_up_token issued by identity (A1).
//
// The agreed method is Verify(ctx, principal, token, action). action is one of
// approval, bank_change, payment_release, correction, break_glass. A token is
// valid for two minutes and one action; that lifetime belongs to A1. Until A1
// lands, StubStepUp accepts only the exact token "stepup.<action>.<userID>".
type StepUpVerifier interface {
	Verify(ctx context.Context, p rls.Principal, token, action string) error
}

// StubStepUp is the stand-in verifier. Replace it in cmd wiring when A1 ships.
type StubStepUp struct{}

// Verify accepts the documented stub token and refuses anything else.
func (StubStepUp) Verify(ctx context.Context, p rls.Principal, token, action string) error {
	want := "stepup." + action + "." + p.UserID
	if strings.TrimSpace(token) == "" || token != want {
		return apierr.New(apierr.StepUpRequired, t(ctx, "stepup.required")).WithDetails(map[string]any{"action": action})
	}
	return nil
}

// PostingGate is what the posting service calls. Approvals does not import it.
type PostingGate interface {
	ConsumePostingToken(ctx context.Context, p rls.Principal, token string, content any) error
}
