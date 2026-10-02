package journeys

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

// forbiddenInput are top-level input keys the client must not be able to write.
// They are dropped before anything is stored or evaluated.
var forbiddenInput = map[string]struct{}{
	"permissions":                {},
	"permission":                 {},
	"roles":                      {},
	"personas":                   {},
	"persona":                    {},
	"workflow_state":             {},
	"granted":                    {},
	"status":                     {},
	"state":                      {},
	"decision":                   {},
	"approval_state":             {},
	"approval_status":            {},
	"approval_ref":               {},
	"approval_outcome":           {},
	"state_version":              {},
	"trial_balance_nets_to_zero": {},
	"unlocked":                   {},
	"server_state":               {},
}

func stripInput(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if _, bad := forbiddenInput[k]; bad {
			continue
		}
		out[k] = v
	}
	return out
}

type stepDef struct {
	ID          string
	Position    int
	Kind        string
	TitleKey    string
	InputSchema map[string]any
	Guard       map[string]any
}

type definition struct {
	Slug     string
	TitleKey string
	GroupKey string
	Personas []string
	Steps    []stepDef
}

func (d definition) step(id string) (stepDef, bool) {
	for _, s := range d.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return stepDef{}, false
}

func (d definition) first() stepDef { return d.Steps[0] }

func (d definition) next(id string) (stepDef, bool) {
	for i, s := range d.Steps {
		if s.ID == id && i+1 < len(d.Steps) {
			return d.Steps[i+1], true
		}
	}
	return stepDef{}, false
}

type instance struct {
	ID            uuid.UUID
	CompanyID     uuid.UUID
	Slug          string
	UserID        string
	Persona       string
	Status        string
	CurrentStepID string
	ServerState   map[string]any
	StateVersion  int64
	UpdatedAt     time.Time
}

func (s stepDef) view(lang string) oapi.JourneyStep {
	schema := s.InputSchema
	if schema == nil {
		schema = map[string]any{}
	}
	out := oapi.JourneyStep{
		StepId:      s.ID,
		Kind:        oapi.JourneyKind(s.Kind),
		Title:       text(lang, s.TitleKey),
		InputSchema: schema,
	}
	if len(s.Guard) > 0 {
		g := s.Guard
		out.Guard = &g
	}
	return out
}

func problemsOrEmpty(in []oapi.JourneyProblem) []oapi.JourneyProblem {
	if in == nil {
		return []oapi.JourneyProblem{}
	}
	return in
}

func codePtr(c oapi.JourneyStepCode) *oapi.JourneyStepCode { return &c }

func strPtr(s string) *string { return &s }

func stepPtr(s oapi.JourneyStep) *oapi.JourneyStep { return &s }

func versionMap(version int64, status string) map[string]any {
	return map[string]any{"state_version": version, "status": status}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
