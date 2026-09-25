package journeys

import (
	"context"
	"encoding/json"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

type guardSpec struct {
	Kind string      `json:"kind"`
	Name string      `json:"name"`
	All  []guardSpec `json:"all"`
}

type guardFailure struct {
	Code       oapi.JourneyStepCode
	MessageKey string
	Problems   []oapi.JourneyProblem
	// Stop means the run ends (rejection). Pending means the step stays put.
	Stop    bool
	Pending bool
}

func parseGuard(raw map[string]any) (guardSpec, error) {
	if len(raw) == 0 {
		return guardSpec{}, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return guardSpec{}, err
	}
	var g guardSpec
	if err := json.Unmarshal(b, &g); err != nil {
		return guardSpec{}, err
	}
	return g, nil
}

func (g guardSpec) empty() bool {
	return g.Kind == "" && len(g.All) == 0
}

// evalGuard re-reads every check from the ports. Stored flags and client input
// are not arguments.
func evalGuard(ctx context.Context, g guardSpec, prov Providers, userID, companyID, approvalRef, lang string) (guardFailure, error) {
	if g.empty() {
		return guardFailure{}, nil
	}
	if len(g.All) > 0 {
		for _, child := range g.All {
			fail, err := evalGuard(ctx, child, prov, userID, companyID, approvalRef, lang)
			if err != nil || fail.Code != "" {
				return fail, err
			}
		}
		return guardFailure{}, nil
	}
	switch g.Kind {
	case "permission":
		ok, err := prov.Permissions.Allowed(ctx, userID, companyID, g.Name)
		if err != nil {
			return guardFailure{}, err
		}
		if !ok {
			return guardFailure{Code: oapi.JourneyStepCodePERMISSIONDENIED, MessageKey: "journey.permission_denied"}, nil
		}
		return guardFailure{}, nil
	case "opening_trial_balance_zero":
		ok, err := prov.Books.TrialBalanceNetsToZero(ctx, companyID)
		if err != nil {
			return guardFailure{}, err
		}
		if !ok {
			return guardFailure{
				Code:       oapi.JourneyStepCodeVALIDATIONERROR,
				MessageKey: "journey.trial_balance",
				Problems: []oapi.JourneyProblem{{
					Code:    "TRIAL_BALANCE",
					Message: text(lang, "journey.trial_balance"),
				}},
			}, nil
		}
		return guardFailure{}, nil
	case "approval_approved":
		d, err := prov.Approvals.Status(ctx, companyID, approvalRef)
		if err != nil {
			return guardFailure{}, err
		}
		switch d {
		case Approved:
			return guardFailure{}, nil
		case Rejected:
			return guardFailure{Code: oapi.JourneyStepCodeREJECTED, MessageKey: "journey.rejected", Stop: true}, nil
		default:
			return guardFailure{Code: oapi.JourneyStepCodePENDING, MessageKey: "journey.pending", Pending: true}, nil
		}
	default:
		return guardFailure{Code: oapi.JourneyStepCodeVALIDATIONERROR, MessageKey: "journey.validation"}, nil
	}
}

func validateInput(schema map[string]any, input map[string]any, lang string) []oapi.JourneyProblem {
	req, _ := schema["required"].([]any)
	var out []oapi.JourneyProblem
	for _, item := range req {
		key, ok := item.(string)
		if !ok {
			continue
		}
		if _, present := input[key]; !present {
			field := key
			out = append(out, oapi.JourneyProblem{
				Code:    "required",
				Message: text(lang, "journey.field_required"),
				Field:   &field,
			})
		}
	}
	return out
}
