package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/app"
	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/journeys"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func init() {
	dedicatedAcceptance["E13"] = proveE13
	dedicatedAcceptance["E14"] = proveE14
	dedicatedAcceptance["E15"] = proveE15
}

// E13: journey state from the client cannot change server-evaluated permissions
// or workflow state.
func proveE13(t *testing.T, s *stack) {
	t.Helper()
	insertJourneyDefinition(t, s, "permission-probe", "journey.probe.permission_title", "probe", []string{"accountant"}, []journeyStep{{
		id: "open_books", kind: "post", titleKey: "journey.probe.permission_step",
		schema: `{"type":"object"}`, guard: `{"kind":"permission","name":"company.unlock"}`,
	}})
	insertJourneyDefinition(t, s, "stakeholder-home", "journey.probe.stakeholder_title", "home", []string{"stakeholder"}, []journeyStep{{
		id: "brief", kind: "read", titleKey: "journey.probe.stakeholder_step",
		schema: `{"type":"object"}`, guard: `{}`,
	}})
	p := rls.Principal{UserID: "accountant-e13", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	srv := journeyServer(t, s, p, &scriptedDecisions{})

	status, raw := journeyCall(t, srv, http.MethodGet, "/api/v1/journeys?persona=stakeholder", nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("E13 persona query must be refused, got %d %s", status, raw)
	}
	var er struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &er); err != nil || er.Error.Code != string(apierr.PermissionDenied) {
		t.Fatalf("E13 expected PERMISSION_DENIED, got %s", raw)
	}

	status, raw = journeyCall(t, srv, http.MethodGet, "/api/v1/journeys?persona=accountant", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("E13 list: %d %s", status, raw)
	}
	list := journeyData[oapi.JourneyList](t, raw)
	slugs := map[string]bool{}
	for _, g := range list.Groups {
		if g.Persona != "accountant" {
			t.Fatalf("E13 unexpected group %s", g.Persona)
		}
		for _, d := range g.Definitions {
			slugs[d.Slug] = true
		}
	}
	if !slugs["go-live"] || !slugs["permission-probe"] || slugs["stakeholder-home"] {
		t.Fatalf("E13 persona filter leaked or hid definitions: %+v", slugs)
	}

	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/permission-probe/instances", nil, journeyIdem())
	if status != http.StatusCreated {
		t.Fatalf("E13 start: %d %s", status, raw)
	}
	created := journeyData[oapi.JourneyInstanceCreated](t, raw)
	body := map[string]any{
		"step_id": "open_books",
		"input": map[string]any{
			"permissions":                []any{"company.unlock"},
			"roles":                      []any{"stakeholder"},
			"personas":                   []any{"stakeholder"},
			"workflow_state":             "approved",
			"granted":                    true,
			"status":                     "completed",
			"trial_balance_nets_to_zero": true,
		},
	}
	stepHdr := journeyIdem()
	stepHdr["If-Match"] = "1"
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step", body, stepHdr)
	if status != http.StatusOK {
		t.Fatalf("E13 step: %d %s", status, raw)
	}
	result := journeyData[oapi.JourneyStepResult](t, raw)
	if result.Ok || result.Code == nil || *result.Code != oapi.JourneyStepCodePERMISSIONDENIED {
		t.Fatalf("E13 client permission claim must be refused: %+v", result)
	}

	status, raw = journeyCall(t, srv, http.MethodGet, "/api/v1/journeys/instances/"+created.InstanceId.String(), nil, nil)
	view := journeyData[oapi.JourneyInstance](t, raw)
	if status != http.StatusOK || view.Status != oapi.JourneyInstanceStatusRunning || view.CurrentStep.StepId != "open_books" {
		t.Fatalf("E13 workflow state changed: %d %+v", status, view)
	}
	blob := string(mustJourneyJSON(view.ServerState))
	for _, forbidden := range []string{"permissions", "workflow_state", "granted", "company.unlock", "trial_balance_nets_to_zero"} {
		if bytes.Contains([]byte(blob), []byte(forbidden)) {
			t.Fatalf("E13 server state stored client claim %q: %s", forbidden, blob)
		}
	}
	var stored string
	if err := s.db.Migrator.QueryRow(context.Background(), `SELECT server_state::text FROM erp.journey_instances WHERE id = $1`, created.InstanceId).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(stored), []byte("workflow_state")) || bytes.Contains([]byte(stored), []byte("company.unlock")) {
		t.Fatalf("E13 row stored client workflow: %s", stored)
	}

	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/go-live/instances", nil, journeyIdem())
	if status != http.StatusCreated {
		t.Fatalf("E13 go-live start: %d %s", status, raw)
	}
	goLive := journeyData[oapi.JourneyInstanceCreated](t, raw)
	skip := map[string]any{"step_id": "unlock_company", "input": map[string]any{"workflow_state": "approved", "unlocked": true}}
	skipHdr := journeyIdem()
	skipHdr["If-Match"] = "1"
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+goLive.InstanceId.String()+"/step", skip, skipHdr)
	if status != http.StatusOK {
		t.Fatalf("E13 skip: %d %s", status, raw)
	}
	skipped := journeyData[oapi.JourneyStepResult](t, raw)
	if skipped.Ok || skipped.NextStep == nil || skipped.NextStep.StepId != "chart_of_accounts" {
		t.Fatalf("E13 skip must stay on the first step: %+v", skipped)
	}
}

// E14: an await step reports pending honestly and stops the run on rejection.
func proveE14(t *testing.T, s *stack) {
	t.Helper()
	insertJourneyDefinition(t, s, "await-probe", "journey.probe.await_title", "probe", []string{"accountant"}, []journeyStep{
		{id: "note", kind: "form", titleKey: "journey.probe.note", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
		{id: "wait", kind: "await", titleKey: "journey.probe.wait", schema: `{"type":"object"}`, guard: `{}`},
		{id: "finish", kind: "post", titleKey: "journey.probe.finish", schema: `{"type":"object"}`, guard: `{}`},
	})
	decisions := &scriptedDecisions{}
	p := rls.Principal{UserID: "accountant-e14", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	srv := journeyServer(t, s, p, decisions)

	status, raw := journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/await-probe/instances", nil, journeyIdem())
	if status != http.StatusCreated {
		t.Fatalf("E14 start: %d %s", status, raw)
	}
	created := journeyData[oapi.JourneyInstanceCreated](t, raw)
	noteHdr := journeyIdem()
	noteHdr["If-Match"] = "1"
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "note", "input": map[string]any{"note": "opening", "decision": "approved"}}, noteHdr)
	if status != http.StatusOK {
		t.Fatalf("E14 note: %d %s", status, raw)
	}
	noted := journeyData[oapi.JourneyStepResult](t, raw)
	if !noted.Ok || noted.NextStep == nil || noted.NextStep.StepId != "wait" {
		t.Fatalf("E14 note should advance to await: %+v", noted)
	}
	ver := journeyStateVersion(t, noted)

	waitHdr := journeyIdem()
	waitHdr["If-Match"] = strconv.Itoa(ver)
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "wait", "input": map[string]any{"approval_state": "approved", "decision": "approved"}}, waitHdr)
	if status != http.StatusOK {
		t.Fatalf("E14 pending: %d %s", status, raw)
	}
	pending := journeyData[oapi.JourneyStepResult](t, raw)
	if pending.Ok || pending.Code == nil || *pending.Code != oapi.JourneyStepCodePENDING || pending.NextStep == nil || pending.NextStep.StepId != "wait" {
		t.Fatalf("E14 pending must be honest: %+v", pending)
	}
	if (*pending.Data)["status"] != string(oapi.JourneyInstanceStatusAwaiting) {
		t.Fatalf("E14 status: %+v", pending.Data)
	}
	ver = journeyStateVersion(t, pending)

	status, raw = journeyCall(t, srv, http.MethodGet, "/api/v1/journeys/instances/"+created.InstanceId.String(), nil, nil)
	view := journeyData[oapi.JourneyInstance](t, raw)
	if status != http.StatusOK || view.Status != oapi.JourneyInstanceStatusAwaiting || view.CurrentStep.StepId != "wait" {
		t.Fatalf("E14 instance while pending: %d %+v", status, view)
	}

	decisions.set("journey:"+created.InstanceId.String()+":wait", journeys.Rejected)
	rejHdr := journeyIdem()
	rejHdr["If-Match"] = strconv.Itoa(ver)
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "wait", "input": map[string]any{"decision": "approved"}}, rejHdr)
	rejected := journeyData[oapi.JourneyStepResult](t, raw)
	if status != http.StatusOK || rejected.Ok || rejected.Code == nil || *rejected.Code != oapi.JourneyStepCodeREJECTED || rejected.NextStep != nil {
		t.Fatalf("E14 rejection: %d %+v %s", status, rejected, raw)
	}
	ver = journeyStateVersion(t, rejected)

	finHdr := journeyIdem()
	finHdr["If-Match"] = strconv.Itoa(ver)
	_, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "finish", "input": map[string]any{}}, finHdr)
	finished := journeyData[oapi.JourneyStepResult](t, raw)
	if finished.Ok || finished.Code == nil || *finished.Code != oapi.JourneyStepCodeREJECTED {
		t.Fatalf("E14 finish must not run after rejection: %+v", finished)
	}
	var finishEvents int
	if err := s.db.App.QueryRow(context.Background(), `SELECT count(*) FROM river_job
		WHERE kind = 'journey.step.completed' AND args->'subject'->>'doc_id' = $1 AND args->'context'->>'step_id' = 'finish'`,
		created.InstanceId.String()).Scan(&finishEvents); err != nil {
		t.Fatal(err)
	}
	if finishEvents != 0 {
		t.Fatalf("E14 finish emitted %d events", finishEvents)
	}
	var statusNow string
	if err := s.db.Migrator.QueryRow(context.Background(), `SELECT status FROM erp.journey_instances WHERE id = $1`, created.InstanceId).Scan(&statusNow); err != nil {
		t.Fatal(err)
	}
	if statusNow != "rejected" {
		t.Fatalf("E14 status %s", statusNow)
	}
}

// E15: a journey instance resumes after the API process is discarded and days later.
func proveE15(t *testing.T, s *stack) {
	t.Helper()
	insertJourneyDefinition(t, s, "resume-probe", "journey.probe.resume_title", "probe", []string{"accountant"}, []journeyStep{
		{id: "one", kind: "form", titleKey: "journey.probe.one", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
		{id: "two", kind: "form", titleKey: "journey.probe.two", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
	})
	p := rls.Principal{UserID: "accountant-e15", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	decisions := &scriptedDecisions{}
	srv := journeyServer(t, s, p, decisions)

	status, raw := journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/resume-probe/instances", nil, journeyIdem())
	if status != http.StatusCreated {
		t.Fatalf("E15 start: %d %s", status, raw)
	}
	created := journeyData[oapi.JourneyInstanceCreated](t, raw)
	firstHdr := journeyIdem()
	firstHdr["If-Match"] = strconv.Itoa(created.StateVersion)
	status, raw = journeyCall(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "one", "input": map[string]any{"note": "kept"}}, firstHdr)
	if status != http.StatusOK {
		t.Fatalf("E15 first step: %d %s", status, raw)
	}
	first := journeyData[oapi.JourneyStepResult](t, raw)
	if !first.Ok || first.NextStep == nil || first.NextStep.StepId != "two" {
		t.Fatalf("E15 first step did not advance: %+v", first)
	}
	srv.Close()
	runtime.GC()
	if _, err := s.db.Migrator.Exec(context.Background(), `UPDATE erp.journey_instances SET updated_at = clock_timestamp() - interval '3 days' WHERE id = $1`, created.InstanceId); err != nil {
		t.Fatal(err)
	}

	srv2 := journeyServer(t, s, p, decisions)
	status, raw = journeyCall(t, srv2, http.MethodGet, "/api/v1/journeys/instances/"+created.InstanceId.String(), nil, nil)
	if status != http.StatusOK {
		t.Fatalf("E15 resume get: %d %s", status, raw)
	}
	view := journeyData[oapi.JourneyInstance](t, raw)
	if view.CurrentStep.StepId != "two" || view.Status != oapi.JourneyInstanceStatusRunning {
		t.Fatalf("E15 did not resume: %+v", view)
	}
	if time.Since(view.UpdatedAt) < 48*time.Hour {
		t.Fatalf("E15 instance should still be resumable days later, updated_at %s", view.UpdatedAt)
	}
	secondHdr := journeyIdem()
	secondHdr["If-Match"] = strconv.Itoa(view.StateVersion)
	status, raw = journeyCall(t, srv2, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "two", "input": map[string]any{"note": "resumed"}}, secondHdr)
	if status != http.StatusOK {
		t.Fatalf("E15 resume step: %d %s", status, raw)
	}
	second := journeyData[oapi.JourneyStepResult](t, raw)
	if !second.Ok {
		t.Fatalf("E15 resume step: %+v", second)
	}
	got, _ := (*second.Data)["status"].(string)
	if got != string(oapi.JourneyInstanceStatusCompleted) {
		t.Fatalf("E15 status after resume: %+v", second.Data)
	}
	var n int
	if err := s.db.Migrator.QueryRow(context.Background(), `SELECT count(*) FROM erp.journey_transitions WHERE instance_id = $1`, created.InstanceId).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("E15 expected 2 persisted transitions, got %d", n)
	}
	var eventID string
	if err := s.db.App.QueryRow(context.Background(), `SELECT args->>'event_id' FROM river_job
		WHERE kind = 'journey.step.completed' AND args->'subject'->>'doc_id' = $1 AND args->'context'->>'step_id' = 'two'`,
		created.InstanceId.String()).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if !journeyULID.MatchString(eventID) {
		t.Fatalf("E15 event id %q is not a ULID", eventID)
	}
}

var journeyULID = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

type journeyStep struct {
	id, kind, titleKey, schema, guard string
}

func insertJourneyDefinition(t *testing.T, s *stack, slug, titleKey, group string, personas []string, steps []journeyStep) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM erp.journey_transitions WHERE instance_id IN (SELECT id FROM erp.journey_instances WHERE slug = $1)`,
		`DELETE FROM erp.journey_instances WHERE slug = $1`,
		`DELETE FROM erp.journey_definition_steps WHERE slug = $1`,
		`DELETE FROM erp.journey_definitions WHERE slug = $1`,
	} {
		if _, err := s.db.Migrator.Exec(ctx, stmt, slug); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Migrator.Exec(ctx, `INSERT INTO erp.journey_definitions (slug, title_key, group_key, personas) VALUES ($1,$2,$3,$4)`,
		slug, titleKey, group, personas); err != nil {
		t.Fatal(err)
	}
	for i, step := range steps {
		if _, err := s.db.Migrator.Exec(ctx, `INSERT INTO erp.journey_definition_steps
			(slug, step_id, position, kind, title_key, input_schema, guard) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb)`,
			slug, step.id, i+1, step.kind, step.titleKey, step.schema, step.guard); err != nil {
			t.Fatal(err)
		}
	}
}

// journeyServer is the composed API. The principal is what identity would put
// on the context; /journeys does not read personas or permissions from the body.
// Approvals are the engine port: a decision the client sends is ignored, and
// the test supplies the neighbouring module's answer the same way production
// will once that module writes erp.approval_requests.
func journeyServer(t *testing.T, s *stack, p rls.Principal, approvals journeys.Approvals) *httptest.Server {
	t.Helper()
	riverClient, err := outbox.NewClient(s.db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	policy := identity.DefaultPolicy()
	policy.RatePerLogin = 1000
	policy.RatePerIP = 1000
	handler, err := app.Handler(httpx.Deps{
		Pool: s.db.App, River: riverClient, Log: slog.New(slog.DiscardHandler), StartedAt: time.Now(),
	}, app.WithIdentity(
		identity.WithBroker(s.broker),
		identity.WithDirectory(s.dir),
		identity.WithPolicy(policy),
		identity.WithClock(func() time.Time { return time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC) }),
	))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	prov := journeys.PostgresProviders(s.db.App)
	prov.Approvals = approvals
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := rls.WithPrincipal(r.Context(), p)
		ctx = journeys.WithProviders(ctx, prov)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return srv
}

func journeyCall(t *testing.T, srv *httptest.Server, method, path string, body any, hdr map[string]string) (int, []byte) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

func journeyData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	return env.Data
}

func journeyIdem() map[string]string {
	return map[string]string{"Idempotency-Key": uuid.NewString()}
}

func journeyStateVersion(t *testing.T, result oapi.JourneyStepResult) int {
	t.Helper()
	if result.Data == nil {
		t.Fatal("missing step data")
	}
	v, ok := (*result.Data)["state_version"].(float64)
	if !ok {
		t.Fatalf("state_version: %#v", *result.Data)
	}
	return int(v)
}

func mustJourneyJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type scriptedDecisions struct {
	mu sync.Mutex
	by map[string]journeys.Decision
}

func (s *scriptedDecisions) set(ref string, d journeys.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]journeys.Decision{}
	}
	s.by[ref] = d
}

func (s *scriptedDecisions) Status(_ context.Context, _, ref string) (journeys.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.by[ref]; ok {
		return d, nil
	}
	return journeys.Pending, nil
}
