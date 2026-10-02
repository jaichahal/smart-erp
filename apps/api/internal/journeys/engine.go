package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Engine runs journey instances. It stores no instance state of its own:
// every read and every transition goes to PostgreSQL, so a new Engine value
// resumes an instance after a restart.
type Engine struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

// NewEngine builds an engine over the application pool and an insert-only River client.
func NewEngine(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *Engine {
	return &Engine{pool: pool, river: riverClient}
}

// List returns definitions grouped by the personas the server has evaluated.
// filter is the requested persona, or empty for every persona the caller holds.
func (e *Engine) List(ctx context.Context, p rls.Principal, filter, lang string) (oapi.JourneyList, error) {
	ctx = ensurePrincipal(ctx, p)
	prov := providersFrom(ctx)
	actor, err := prov.Personas.List(ctx, p.UserID)
	if err != nil {
		return oapi.JourneyList{}, fmt.Errorf("journeys: personas: %w", err)
	}
	if filter != "" && !holds(actor, filter) {
		return oapi.JourneyList{}, apierr.New(apierr.PermissionDenied, text(lang, "journey.persona_denied"))
	}
	var defs []definition
	err = rls.Tx(ctx, e.pool, p, func(tx pgx.Tx) error {
		var err error
		defs, err = listDefinitions(ctx, tx, actor, filter)
		return err
	})
	if err != nil {
		return oapi.JourneyList{}, localise(lang, err)
	}
	return groupDefinitions(defs, actor, filter, lang), nil
}

// Start persists a new instance at the definition's first step.
func (e *Engine) Start(ctx context.Context, p rls.Principal, slug, lang string) (oapi.JourneyInstanceCreated, error) {
	ctx = ensurePrincipal(ctx, p)
	prov := providersFrom(ctx)
	actor, err := prov.Personas.List(ctx, p.UserID)
	if err != nil {
		return oapi.JourneyInstanceCreated{}, fmt.Errorf("journeys: personas: %w", err)
	}
	var created oapi.JourneyInstanceCreated
	err = rls.Tx(ctx, e.pool, p, func(tx pgx.Tx) error {
		def, err := loadDefinition(ctx, tx, slug)
		if err != nil {
			return err
		}
		persona := ""
		for _, candidate := range def.Personas {
			if holds(actor, candidate) {
				persona = candidate
				break
			}
		}
		if persona == "" {
			return apierr.New(apierr.PermissionDenied, text(lang, "journey.persona_denied"))
		}
		inst := instance{
			ID:            uuid.New(),
			CompanyID:     p.CompanyID,
			Slug:          def.Slug,
			UserID:        p.UserID,
			Persona:       persona,
			Status:        string(oapi.JourneyInstanceStatusRunning),
			CurrentStepID: def.first().ID,
			ServerState:   map[string]any{},
			StateVersion:  1,
		}
		if err := insertInstance(ctx, tx, inst); err != nil {
			return err
		}
		created = oapi.JourneyInstanceCreated{
			InstanceId:   inst.ID,
			Step:         def.first().view(lang),
			StateVersion: int(inst.StateVersion),
		}
		return nil
	})
	if err != nil {
		return oapi.JourneyInstanceCreated{}, localise(lang, err)
	}
	return created, nil
}

// Get returns the persisted instance. It is the resume point.
func (e *Engine) Get(ctx context.Context, p rls.Principal, id uuid.UUID, lang string) (oapi.JourneyInstance, error) {
	ctx = ensurePrincipal(ctx, p)
	var view oapi.JourneyInstance
	err := rls.Tx(ctx, e.pool, p, func(tx pgx.Tx) error {
		inst, err := loadInstance(ctx, tx, id, false)
		if err != nil {
			return err
		}
		def, err := loadDefinition(ctx, tx, inst.Slug)
		if err != nil {
			return err
		}
		view, err = presentInstance(inst, def, lang)
		return err
	})
	if err != nil {
		return oapi.JourneyInstance{}, localise(lang, err)
	}
	return view, nil
}

// Submit applies one step. Business outcomes (pending, rejected, refused) are a
// step result with a nil error. Transport failures are errors.
func (e *Engine) Submit(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64, stepID string, input map[string]any, lang string) (oapi.JourneyStepResult, error) {
	ctx = ensurePrincipal(ctx, p)
	prov := providersFrom(ctx)
	input = stripInput(input)
	var result oapi.JourneyStepResult
	err := rls.Tx(ctx, e.pool, p, func(tx pgx.Tx) error {
		inst, err := loadInstance(ctx, tx, id, true)
		if err != nil {
			return err
		}
		def, err := loadDefinition(ctx, tx, inst.Slug)
		if err != nil {
			return err
		}
		if expected != inst.StateVersion {
			current, viewErr := presentInstance(inst, def, lang)
			if viewErr != nil {
				return viewErr
			}
			return ifmatch.Check(expected, inst.StateVersion, current)
		}
		if inst.Status == string(oapi.JourneyInstanceStatusRejected) {
			result = stopped(oapi.JourneyStepCodeREJECTED, "journey.stopped", lang, inst)
			return nil
		}
		if inst.Status == string(oapi.JourneyInstanceStatusCompleted) {
			result = stopped(oapi.JourneyStepCodeCONFLICT, "journey.already_finished", lang, inst)
			return nil
		}
		step, ok := def.step(inst.CurrentStepID)
		if !ok {
			return fmt.Errorf("journeys: current step %s missing", inst.CurrentStepID)
		}
		if step.ID != stepID {
			result = mismatch(lang, inst, step)
			return nil
		}
		before := snapshot(inst)
		nextResult, outcome, err := e.decide(ctx, prov, &inst, def, step, input, lang)
		if err != nil {
			return err
		}
		if outcome == "" {
			result = nextResult
			return nil
		}
		inst.StateVersion++
		nextResult = withVersion(nextResult, inst)
		if err := saveInstance(ctx, tx, inst); err != nil {
			return err
		}
		body, err := json.Marshal(nextResult)
		if err != nil {
			return fmt.Errorf("journeys: marshal result: %w", err)
		}
		if err := insertTransition(ctx, tx, inst, step.ID, outcome, input, body); err != nil {
			return err
		}
		auditType := "journey.step.refused"
		if outcome == "completed" || outcome == "rejected" {
			auditType = "journey.step.completed"
		}
		if _, err := audit.Emit(ctx, tx, audit.Event{
			Type:          auditType,
			ReferenceType: "journey_instance",
			ReferenceID:   inst.ID.String(),
			Before:        before,
			After:         snapshot(inst),
		}); err != nil {
			return err
		}
		if outcome == "completed" || outcome == "rejected" {
			if err := e.enqueue(ctx, tx, p, inst, step.ID, outcome); err != nil {
				return err
			}
		}
		result = nextResult
		return nil
	})
	if err != nil {
		return oapi.JourneyStepResult{}, localise(lang, err)
	}
	return result, nil
}

// decide mutates inst in memory and returns the step result before the version bump.
// outcome is empty when nothing should be persisted.
func (e *Engine) decide(ctx context.Context, prov Providers, inst *instance, def definition, step stepDef, input map[string]any, lang string) (oapi.JourneyStepResult, string, error) {
	switch step.Kind {
	case string(oapi.Form), string(oapi.WriteDraft), string(oapi.Route), string(oapi.Read):
		return e.collect(inst, def, step, input, lang)
	case string(oapi.Validate), string(oapi.Post):
		return e.guarded(ctx, prov, inst, def, step, lang)
	case string(oapi.Await):
		return e.await(ctx, prov, inst, def, step, lang)
	default:
		return oapi.JourneyStepResult{}, "", fmt.Errorf("journeys: unknown step kind %s", step.Kind)
	}
}

func (e *Engine) collect(inst *instance, def definition, step stepDef, input map[string]any, lang string) (oapi.JourneyStepResult, string, error) {
	problems := validateInput(step.InputSchema, input, lang)
	if len(problems) > 0 {
		return refused(oapi.JourneyStepCodeVALIDATIONERROR, "journey.validation", lang, inst, step, problems), "refused", nil
	}
	putCollected(inst.ServerState, step.ID, input)
	return advance(inst, def, lang), "completed", nil
}

func (e *Engine) guarded(ctx context.Context, prov Providers, inst *instance, def definition, step stepDef, lang string) (oapi.JourneyStepResult, string, error) {
	spec, err := parseGuard(step.Guard)
	if err != nil {
		return oapi.JourneyStepResult{}, "", err
	}
	fail, err := evalGuard(ctx, spec, prov, inst.UserID, inst.CompanyID.String(), approvalRefOf(inst), lang)
	if err != nil {
		return oapi.JourneyStepResult{}, "", err
	}
	if fail.Code != "" {
		return applyGuardFailure(inst, step, lang, fail)
	}
	if mentions(step.Guard, "opening_trial_balance_zero") {
		inst.ServerState["trial_balance_nets_to_zero"] = true
	}
	if step.Kind == string(oapi.Post) {
		inst.ServerState["unlocked"] = true
	}
	return advance(inst, def, lang), "completed", nil
}

func (e *Engine) await(ctx context.Context, prov Providers, inst *instance, def definition, step stepDef, lang string) (oapi.JourneyStepResult, string, error) {
	ref := ensureApprovalRef(inst, step.ID)
	decision, err := prov.Approvals.Status(ctx, inst.CompanyID.String(), ref)
	if err != nil {
		return oapi.JourneyStepResult{}, "", err
	}
	switch decision {
	case Approved:
		inst.ServerState["approval_outcome"] = string(Approved)
		if inst.Status == string(oapi.JourneyInstanceStatusAwaiting) {
			inst.Status = string(oapi.JourneyInstanceStatusRunning)
		}
		return advance(inst, def, lang), "completed", nil
	case Rejected:
		inst.ServerState["approval_outcome"] = string(Rejected)
		inst.Status = string(oapi.JourneyInstanceStatusRejected)
		res := refused(oapi.JourneyStepCodeREJECTED, "journey.rejected", lang, inst, step, nil)
		res.NextStep = nil
		return res, "rejected", nil
	default:
		inst.ServerState["approval_outcome"] = string(Pending)
		inst.Status = string(oapi.JourneyInstanceStatusAwaiting)
		res := refused(oapi.JourneyStepCodePENDING, "journey.pending", lang, inst, step, nil)
		return res, "pending", nil
	}
}

func applyGuardFailure(inst *instance, step stepDef, lang string, fail guardFailure) (oapi.JourneyStepResult, string, error) {
	if mentions(step.Guard, "opening_trial_balance_zero") && !fail.Stop && !fail.Pending {
		inst.ServerState["trial_balance_nets_to_zero"] = false
	}
	res := refused(fail.Code, fail.MessageKey, lang, inst, step, fail.Problems)
	if fail.Stop {
		inst.Status = string(oapi.JourneyInstanceStatusRejected)
		res.NextStep = nil
		data := versionMap(inst.StateVersion, inst.Status)
		res.Data = &data
		return res, "rejected", nil
	}
	if fail.Pending {
		inst.Status = string(oapi.JourneyInstanceStatusAwaiting)
		data := versionMap(inst.StateVersion, inst.Status)
		res.Data = &data
		return res, "pending", nil
	}
	return res, "refused", nil
}

func advance(inst *instance, def definition, lang string) oapi.JourneyStepResult {
	nxt, ok := def.next(inst.CurrentStepID)
	data := versionMap(inst.StateVersion, inst.Status)
	res := oapi.JourneyStepResult{
		Ok:       true,
		Message:  strPtr(text(lang, "journey.step_completed")),
		Problems: []oapi.JourneyProblem{},
		Data:     &data,
	}
	if !ok {
		inst.Status = string(oapi.JourneyInstanceStatusCompleted)
		data := versionMap(inst.StateVersion, inst.Status)
		res.Data = &data
		return res
	}
	inst.CurrentStepID = nxt.ID
	inst.Status = string(oapi.JourneyInstanceStatusRunning)
	data = versionMap(inst.StateVersion, inst.Status)
	res.Data = &data
	res.NextStep = stepPtr(nxt.view(lang))
	return res
}

func (e *Engine) enqueue(ctx context.Context, tx pgx.Tx, p rls.Principal, inst instance, stepID, outcome string) error {
	if e.river == nil {
		return fmt.Errorf("journeys: outbox client is not configured")
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return fmt.Errorf("journeys: clock: %w", err)
	}
	eventID, err := newULID(at.UTC())
	if err != nil {
		return err
	}
	name := p.UserID
	prov := providersFrom(ctx)
	if prov.ActorName != nil {
		if n, err := prov.ActorName(ctx, p.UserID); err == nil && n != "" {
			name = n
		}
	}
	severity := "LOW"
	if outcome == "rejected" {
		severity = "MEDIUM"
	}
	_, err = outbox.InsertTx(ctx, e.river, tx, StepCompletedArgs{
		EventID:    eventID,
		Type:       "journey.step.completed",
		Severity:   severity,
		CompanyID:  inst.CompanyID.String(),
		OccurredAt: at.UTC(),
		Actor:      eventActor{ID: p.UserID, Name: name},
		Subject: eventSubject{
			DocType:   "journey_instance",
			DocID:     inst.ID.String(),
			DocNumber: inst.Slug,
		},
		DeepLink:       "smarterp://journey/" + inst.ID.String(),
		AllowedActions: []string{"open"},
		StateVersion:   int(inst.StateVersion),
		Context: map[string]any{
			"slug":            inst.Slug,
			"step_id":         stepID,
			"outcome":         outcome,
			"instance_status": inst.Status,
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("journeys: outbox: %w", err)
	}
	return nil
}

func withVersion(res oapi.JourneyStepResult, inst instance) oapi.JourneyStepResult {
	data := versionMap(inst.StateVersion, inst.Status)
	res.Data = &data
	return res
}

func presentInstance(inst instance, def definition, lang string) (oapi.JourneyInstance, error) {
	step, ok := def.step(inst.CurrentStepID)
	if !ok {
		return oapi.JourneyInstance{}, fmt.Errorf("journeys: current step %s missing", inst.CurrentStepID)
	}
	state := inst.ServerState
	if state == nil {
		state = map[string]any{}
	}
	return oapi.JourneyInstance{
		InstanceId:   inst.ID,
		Slug:         inst.Slug,
		Persona:      inst.Persona,
		Status:       oapi.JourneyInstanceStatus(inst.Status),
		CurrentStep:  step.view(lang),
		StateVersion: int(inst.StateVersion),
		ServerState:  state,
		UpdatedAt:    inst.UpdatedAt.UTC(),
	}, nil
}

func groupDefinitions(defs []definition, actor []string, filter, lang string) oapi.JourneyList {
	by := map[string][]oapi.JourneyDefinition{}
	for _, d := range defs {
		view := oapi.JourneyDefinition{
			Slug:     d.Slug,
			Title:    text(lang, d.TitleKey),
			Group:    d.GroupKey,
			Personas: append([]string(nil), d.Personas...),
		}
		for _, s := range d.Steps {
			view.Steps = append(view.Steps, s.view(lang))
		}
		for _, persona := range d.Personas {
			if !holds(actor, persona) {
				continue
			}
			if filter != "" && persona != filter {
				continue
			}
			by[persona] = append(by[persona], view)
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	groups := make([]oapi.JourneyGroup, 0, len(keys))
	for _, k := range keys {
		groups = append(groups, oapi.JourneyGroup{Persona: k, Definitions: by[k]})
	}
	return oapi.JourneyList{Groups: groups}
}

func holds(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func putCollected(state map[string]any, stepID string, input map[string]any) {
	col, _ := state["collected"].(map[string]any)
	if col == nil {
		col = map[string]any{}
	}
	col[stepID] = input
	state["collected"] = col
}

func ensureApprovalRef(inst *instance, stepID string) string {
	if ref, ok := inst.ServerState["approval_ref"].(string); ok && ref != "" {
		return ref
	}
	ref := "journey:" + inst.ID.String() + ":" + stepID
	inst.ServerState["approval_ref"] = ref
	return ref
}

func approvalRefOf(inst *instance) string {
	ref, _ := inst.ServerState["approval_ref"].(string)
	return ref
}

func mentions(guard map[string]any, kind string) bool {
	b, err := json.Marshal(guard)
	if err != nil {
		return false
	}
	var spec guardSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return false
	}
	return spec.mentions(kind)
}

func (g guardSpec) mentions(kind string) bool {
	if g.Kind == kind {
		return true
	}
	for _, child := range g.All {
		if child.mentions(kind) {
			return true
		}
	}
	return false
}

func snapshot(inst instance) map[string]any {
	return map[string]any{
		"status":          inst.Status,
		"current_step_id": inst.CurrentStepID,
		"state_version":   inst.StateVersion,
		"server_state":    inst.ServerState,
	}
}

func refused(code oapi.JourneyStepCode, key, lang string, inst *instance, step stepDef, problems []oapi.JourneyProblem) oapi.JourneyStepResult {
	data := versionMap(inst.StateVersion, inst.Status)
	return oapi.JourneyStepResult{
		Ok:       false,
		Code:     codePtr(code),
		Message:  strPtr(text(lang, key)),
		Problems: problemsOrEmpty(problems),
		NextStep: stepPtr(step.view(lang)),
		Data:     &data,
	}
}

func mismatch(lang string, inst instance, step stepDef) oapi.JourneyStepResult {
	data := versionMap(inst.StateVersion, inst.Status)
	return oapi.JourneyStepResult{
		Ok:       false,
		Code:     codePtr(oapi.JourneyStepCodeVALIDATIONERROR),
		Message:  strPtr(text(lang, "journey.step_mismatch")),
		Problems: []oapi.JourneyProblem{},
		NextStep: stepPtr(step.view(lang)),
		Data:     &data,
	}
}

func stopped(code oapi.JourneyStepCode, key, lang string, inst instance) oapi.JourneyStepResult {
	data := versionMap(inst.StateVersion, inst.Status)
	return oapi.JourneyStepResult{
		Ok:       false,
		Code:     codePtr(code),
		Message:  strPtr(text(lang, key)),
		Problems: []oapi.JourneyProblem{},
		Data:     &data,
	}
}

func ensurePrincipal(ctx context.Context, p rls.Principal) context.Context {
	if _, err := rls.FromContext(ctx); err != nil {
		return rls.WithPrincipal(ctx, p)
	}
	return ctx
}

func localise(lang string, err error) error {
	var ae *apierr.Error
	if errors.As(err, &ae) && ae.Code == apierr.NotFound {
		return apierr.New(apierr.NotFound, text(lang, "journey.not_found"))
	}
	return err
}
