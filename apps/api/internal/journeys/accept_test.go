package journeys_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/journeys"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

var (
	migrateOnce sync.Once
	migrateErr  error
	ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

func ensureSchema(t *testing.T, db *testdb.DB) {
	t.Helper()
	migrateOnce.Do(func() {
		migrateErr = applyMigrations(db.Name)
	})
	if migrateErr != nil {
		t.Fatal(migrateErr)
	}
}

func applyMigrations(name string) error {
	sqlDB, err := sql.Open("pgx", withDB(os.Getenv("ERP_MIGRATOR_DATABASE_URL"), name))
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	goose.SetTableName("goose_db_version")
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(sqlDB, ".")
}

func withDB(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

type staticPersonas []string

func (s staticPersonas) List(context.Context, string) ([]string, error) { return []string(s), nil }

type staticPerms map[string]bool

func (m staticPerms) Allowed(_ context.Context, _, _, permission string) (bool, error) {
	return m[permission], nil
}

type scriptedApprovals struct {
	mu sync.Mutex
	by map[string]journeys.Decision
}

func (s *scriptedApprovals) set(ref string, d journeys.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]journeys.Decision{}
	}
	s.by[ref] = d
}

func (s *scriptedApprovals) Status(_ context.Context, _, ref string) (journeys.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.by[ref]; ok {
		return d, nil
	}
	return journeys.Pending, nil
}

type staticBooks bool

func (b staticBooks) TrialBalanceNetsToZero(context.Context, string) (bool, error) {
	return bool(b), nil
}

func newDeps(t *testing.T, db *testdb.DB) httpx.Deps {
	t.Helper()
	client, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return httpx.Deps{Pool: db.App, River: client, Log: slog.New(slog.DiscardHandler)}
}

func newServer(t *testing.T, deps httpx.Deps, p rls.Principal, prov journeys.Providers) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Use(apierr.RequestIDMiddleware)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := rls.WithPrincipal(r.Context(), p)
			ctx = journeys.WithProviders(ctx, prov)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Route("/api/v1", func(r chi.Router) {
		journeys.Mount(r, deps)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any, hdr map[string]string) (int, []byte) {
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

func decodeData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	return env.Data
}

func idem() map[string]string {
	return map[string]string{"Idempotency-Key": uuid.NewString()}
}

func resetSlug(t *testing.T, db *testdb.DB, slug string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM erp.journey_transitions WHERE instance_id IN (SELECT id FROM erp.journey_instances WHERE slug = $1)`,
		`DELETE FROM erp.journey_instances WHERE slug = $1`,
		`DELETE FROM erp.journey_definition_steps WHERE slug = $1`,
		`DELETE FROM erp.journey_definitions WHERE slug = $1`,
	} {
		if _, err := db.Migrator.Exec(ctx, stmt, slug); err != nil {
			t.Fatal(err)
		}
	}
}

type stepRow struct {
	id, kind, titleKey, schema, guard string
}

func insertDefinition(t *testing.T, db *testdb.DB, slug, titleKey, group string, personas []string, steps []stepRow) {
	t.Helper()
	resetSlug(t, db, slug)
	ctx := context.Background()
	if _, err := db.Migrator.Exec(ctx, `INSERT INTO erp.journey_definitions (slug, title_key, group_key, personas) VALUES ($1,$2,$3,$4)`,
		slug, titleKey, group, personas); err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		if _, err := db.Migrator.Exec(ctx, `INSERT INTO erp.journey_definition_steps
			(slug, step_id, position, kind, title_key, input_schema, guard) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb)`,
			slug, s.id, i+1, s.kind, s.titleKey, s.schema, s.guard); err != nil {
			t.Fatal(err)
		}
	}
}

// E13: client-supplied permissions and workflow state cannot change what the server evaluates.
func TestE13_ClientStateCannotChangePermissionsOrWorkflow(t *testing.T) {
	db := testdb.New(t)
	ensureSchema(t, db)
	insertDefinition(t, db, "permission-probe", "journey.probe.permission_title", "probe", []string{"accountant"}, []stepRow{{
		id: "open_books", kind: "post", titleKey: "journey.probe.permission_step",
		schema: `{"type":"object"}`, guard: `{"kind":"permission","name":"company.unlock"}`,
	}})
	insertDefinition(t, db, "stakeholder-home", "journey.probe.stakeholder_title", "home", []string{"stakeholder"}, []stepRow{{
		id: "brief", kind: "read", titleKey: "journey.probe.stakeholder_step",
		schema: `{"type":"object"}`, guard: `{}`,
	}})

	p := rls.Principal{UserID: "accountant-e13", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	prov := journeys.Providers{
		Personas:    staticPersonas{"accountant"},
		Permissions: staticPerms{},
		Approvals:   &scriptedApprovals{},
		Books:       staticBooks(false),
	}
	srv := newServer(t, newDeps(t, db), p, prov)

	status, raw := call(t, srv, http.MethodGet, "/api/v1/journeys?persona=stakeholder", nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("persona query must be refused, got %d %s", status, raw)
	}
	var er struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &er); err != nil || er.Error.Code != string(apierr.PermissionDenied) {
		t.Fatalf("expected PERMISSION_DENIED, got %s", raw)
	}

	status, raw = call(t, srv, http.MethodGet, "/api/v1/journeys?persona=accountant", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	list := decodeData[oapi.JourneyList](t, raw)
	slugs := map[string]bool{}
	for _, g := range list.Groups {
		if g.Persona != "accountant" {
			t.Fatalf("unexpected group %s", g.Persona)
		}
		for _, d := range g.Definitions {
			slugs[d.Slug] = true
		}
	}
	if !slugs["go-live"] || !slugs["permission-probe"] || slugs["stakeholder-home"] {
		t.Fatalf("persona filter leaked or hid definitions: %+v", slugs)
	}

	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/permission-probe/instances", nil, idem())
	if status != http.StatusCreated {
		t.Fatalf("start: %d %s", status, raw)
	}
	created := decodeData[oapi.JourneyInstanceCreated](t, raw)
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
	stepHdr := idem()
	stepHdr["If-Match"] = "1"
	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step", body, stepHdr)
	if status != http.StatusOK {
		t.Fatalf("step: %d %s", status, raw)
	}
	result := decodeData[oapi.JourneyStepResult](t, raw)
	if result.Ok || result.Code == nil || *result.Code != oapi.JourneyStepCodePERMISSIONDENIED {
		t.Fatalf("client permission claim must be refused: %+v", result)
	}

	status, raw = call(t, srv, http.MethodGet, "/api/v1/journeys/instances/"+created.InstanceId.String(), nil, nil)
	view := decodeData[oapi.JourneyInstance](t, raw)
	if status != http.StatusOK || view.Status != oapi.JourneyInstanceStatusRunning || view.CurrentStep.StepId != "open_books" {
		t.Fatalf("workflow state changed: %d %+v", status, view)
	}
	blob := string(mustJSON(view.ServerState))
	for _, forbidden := range []string{"permissions", "workflow_state", "granted", "company.unlock", "trial_balance_nets_to_zero"} {
		if bytes.Contains([]byte(blob), []byte(forbidden)) {
			t.Fatalf("server state stored client claim %q: %s", forbidden, blob)
		}
	}

	var stored string
	if err := db.Migrator.QueryRow(context.Background(), `SELECT server_state::text FROM erp.journey_instances WHERE id = $1`, created.InstanceId).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(stored), []byte("workflow_state")) || bytes.Contains([]byte(stored), []byte("company.unlock")) {
		t.Fatalf("row stored client workflow: %s", stored)
	}

	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/go-live/instances", nil, idem())
	if status != http.StatusCreated {
		t.Fatalf("go-live start: %d %s", status, raw)
	}
	goLive := decodeData[oapi.JourneyInstanceCreated](t, raw)
	skip := map[string]any{"step_id": "unlock_company", "input": map[string]any{"workflow_state": "approved", "unlocked": true}}
	skipHdr := idem()
	skipHdr["If-Match"] = "1"
	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+goLive.InstanceId.String()+"/step", skip, skipHdr)
	if status != http.StatusOK {
		t.Fatalf("skip: %d %s", status, raw)
	}
	skipped := decodeData[oapi.JourneyStepResult](t, raw)
	if skipped.Ok || skipped.NextStep == nil || skipped.NextStep.StepId != "chart_of_accounts" {
		t.Fatalf("skip must stay on the first step: %+v", skipped)
	}
}

// E14: an await step reports pending and stops the run when the decision is a rejection.
func TestE14_AwaitReportsPendingAndStopsOnRejection(t *testing.T) {
	db := testdb.New(t)
	ensureSchema(t, db)
	insertDefinition(t, db, "await-probe", "journey.probe.await_title", "probe", []string{"accountant"}, []stepRow{
		{id: "note", kind: "form", titleKey: "journey.probe.note", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
		{id: "wait", kind: "await", titleKey: "journey.probe.wait", schema: `{"type":"object"}`, guard: `{}`},
		{id: "finish", kind: "post", titleKey: "journey.probe.finish", schema: `{"type":"object"}`, guard: `{}`},
	})
	approvals := &scriptedApprovals{}
	p := rls.Principal{UserID: "accountant-e14", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	prov := journeys.Providers{Personas: staticPersonas{"accountant"}, Permissions: staticPerms{}, Approvals: approvals, Books: staticBooks(false)}
	srv := newServer(t, newDeps(t, db), p, prov)

	status, raw := call(t, srv, http.MethodPost, "/api/v1/journeys/await-probe/instances", nil, idem())
	if status != http.StatusCreated {
		t.Fatalf("start: %d %s", status, raw)
	}
	created := decodeData[oapi.JourneyInstanceCreated](t, raw)
	noteHdr := idem()
	noteHdr["If-Match"] = "1"
	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "note", "input": map[string]any{"note": "opening", "decision": "approved"}}, noteHdr)
	if status != http.StatusOK {
		t.Fatalf("note: %d %s", status, raw)
	}
	noted := decodeData[oapi.JourneyStepResult](t, raw)
	if !noted.Ok || noted.NextStep == nil || noted.NextStep.StepId != "wait" {
		t.Fatalf("note should advance to await: %+v", noted)
	}
	ver := stateVersion(t, noted)

	waitHdr := idem()
	waitHdr["If-Match"] = strconv.Itoa(ver)
	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "wait", "input": map[string]any{"approval_state": "approved", "decision": "approved"}}, waitHdr)
	if status != http.StatusOK {
		t.Fatalf("pending: %d %s", status, raw)
	}
	pending := decodeData[oapi.JourneyStepResult](t, raw)
	if pending.Ok || pending.Code == nil || *pending.Code != oapi.JourneyStepCodePENDING || pending.NextStep == nil || pending.NextStep.StepId != "wait" {
		t.Fatalf("pending must be honest: %+v", pending)
	}
	if (*pending.Data)["status"] != string(oapi.JourneyInstanceStatusAwaiting) {
		t.Fatalf("status: %+v", pending.Data)
	}
	ver = stateVersion(t, pending)

	status, raw = call(t, srv, http.MethodGet, "/api/v1/journeys/instances/"+created.InstanceId.String(), nil, nil)
	view := decodeData[oapi.JourneyInstance](t, raw)
	if status != http.StatusOK || view.Status != oapi.JourneyInstanceStatusAwaiting || view.CurrentStep.StepId != "wait" {
		t.Fatalf("instance while pending: %d %+v", status, view)
	}

	approvals.set("journey:"+created.InstanceId.String()+":wait", journeys.Rejected)
	rejHdr := idem()
	rejHdr["If-Match"] = strconv.Itoa(ver)
	status, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "wait", "input": map[string]any{"decision": "approved"}}, rejHdr)
	rejected := decodeData[oapi.JourneyStepResult](t, raw)
	if status != http.StatusOK || rejected.Ok || rejected.Code == nil || *rejected.Code != oapi.JourneyStepCodeREJECTED || rejected.NextStep != nil {
		t.Fatalf("rejection: %d %+v %s", status, rejected, raw)
	}
	ver = stateVersion(t, rejected)

	finHdr := idem()
	finHdr["If-Match"] = strconv.Itoa(ver)
	_, raw = call(t, srv, http.MethodPost, "/api/v1/journeys/instances/"+created.InstanceId.String()+"/step",
		map[string]any{"step_id": "finish", "input": map[string]any{}}, finHdr)
	finished := decodeData[oapi.JourneyStepResult](t, raw)
	if finished.Ok || finished.Code == nil || *finished.Code != oapi.JourneyStepCodeREJECTED {
		t.Fatalf("finish must not run after rejection: %+v", finished)
	}
	var finishEvents int
	if err := db.App.QueryRow(context.Background(), `SELECT count(*) FROM river_job
		WHERE kind = 'journey.step.completed' AND args->'subject'->>'doc_id' = $1 AND args->'context'->>'step_id' = 'finish'`,
		created.InstanceId.String()).Scan(&finishEvents); err != nil {
		t.Fatal(err)
	}
	if finishEvents != 0 {
		t.Fatalf("finish emitted %d events", finishEvents)
	}
	var statusNow string
	if err := db.Migrator.QueryRow(context.Background(), `SELECT status FROM erp.journey_instances WHERE id = $1`, created.InstanceId).Scan(&statusNow); err != nil {
		t.Fatal(err)
	}
	if statusNow != "rejected" {
		t.Fatalf("status %s", statusNow)
	}
}

// E15: a new engine resumes an instance from the database after the first engine is discarded and days have passed.
func TestE15_InstanceResumesAfterRestartAndDays(t *testing.T) {
	db := testdb.New(t)
	ensureSchema(t, db)
	insertDefinition(t, db, "resume-probe", "journey.probe.resume_title", "probe", []string{"accountant"}, []stepRow{
		{id: "one", kind: "form", titleKey: "journey.probe.one", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
		{id: "two", kind: "form", titleKey: "journey.probe.two", schema: `{"type":"object","required":["note"]}`, guard: `{}`},
	})
	p := rls.Principal{UserID: "accountant-e15", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	prov := journeys.Providers{Personas: staticPersonas{"accountant"}, Permissions: staticPerms{}, Approvals: &scriptedApprovals{}, Books: staticBooks(false)}
	ctx := journeys.WithProviders(rls.WithPrincipal(context.Background(), p), prov)
	deps := newDeps(t, db)

	eng1 := journeys.NewEngine(deps.Pool, deps.River)
	created, err := eng1.Start(ctx, p, "resume-probe", "en")
	if err != nil {
		t.Fatal(err)
	}
	first, err := eng1.Submit(ctx, p, created.InstanceId, int64(created.StateVersion), "one", map[string]any{"note": "kept"}, "en")
	if err != nil || !first.Ok || first.NextStep == nil || first.NextStep.StepId != "two" {
		t.Fatalf("first step: %+v %v", first, err)
	}
	eng1 = nil
	runtime.GC()
	if _, err := db.Migrator.Exec(context.Background(), `UPDATE erp.journey_instances SET updated_at = clock_timestamp() - interval '3 days' WHERE id = $1`, created.InstanceId); err != nil {
		t.Fatal(err)
	}

	eng2 := journeys.NewEngine(deps.Pool, deps.River)
	view, err := eng2.Get(ctx, p, created.InstanceId, "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.CurrentStep.StepId != "two" || view.Status != oapi.JourneyInstanceStatusRunning {
		t.Fatalf("did not resume: %+v", view)
	}
	if time.Since(view.UpdatedAt) < 48*time.Hour {
		t.Fatalf("instance should still be resumable days later, updated_at %s", view.UpdatedAt)
	}
	second, err := eng2.Submit(ctx, p, created.InstanceId, int64(view.StateVersion), "two", map[string]any{"note": "resumed"}, "en")
	if err != nil || !second.Ok {
		t.Fatalf("resume step: %+v %v", second, err)
	}
	got, _ := (*second.Data)["status"].(string)
	if got != string(oapi.JourneyInstanceStatusCompleted) {
		t.Fatalf("status after resume: %+v", second.Data)
	}
	var n int
	if err := db.Migrator.QueryRow(context.Background(), `SELECT count(*) FROM erp.journey_transitions WHERE instance_id = $1`, created.InstanceId).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 persisted transitions, got %d", n)
	}
	var eventID string
	if err := db.App.QueryRow(context.Background(), `SELECT args->>'event_id' FROM river_job
		WHERE kind = 'journey.step.completed' AND args->'subject'->>'doc_id' = $1 AND args->'context'->>'step_id' = 'two'`,
		created.InstanceId.String()).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if !ulidPattern.MatchString(eventID) {
		t.Fatalf("event id %q is not a ULID", eventID)
	}
}

func TestGoLiveFixtureMatchesR18(t *testing.T) {
	db := testdb.New(t)
	ensureSchema(t, db)
	p := rls.Principal{UserID: "accountant-golive", CompanyID: uuid.New(), Roles: []string{"accountant"}}
	prov := journeys.Providers{Personas: staticPersonas{"accountant"}, Permissions: staticPerms{}, Approvals: &scriptedApprovals{}, Books: staticBooks(false)}
	srv := newServer(t, newDeps(t, db), p, prov)
	status, raw := call(t, srv, http.MethodGet, "/api/v1/journeys?persona=accountant", nil, map[string]string{"Accept-Language": "ar"})
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	list := decodeData[oapi.JourneyList](t, raw)
	var def *oapi.JourneyDefinition
	for _, g := range list.Groups {
		for i := range g.Definitions {
			if g.Definitions[i].Slug == "go-live" {
				def = &g.Definitions[i]
			}
		}
	}
	if def == nil {
		t.Fatal("go-live definition missing")
	}
	if def.Title != "بدء التشغيل" {
		t.Fatalf("arabic title %q", def.Title)
	}
	want := []struct{ id, kind string }{
		{"chart_of_accounts", "form"},
		{"open_customer_invoices", "form"},
		{"open_supplier_invoices", "form"},
		{"opening_stock", "form"},
		{"opening_assets", "form"},
		{"bank_balances", "form"},
		{"trial_balance", "validate"},
		{"stakeholder_approval", "await"},
		{"unlock_company", "post"},
	}
	if len(def.Steps) != len(want) {
		t.Fatalf("steps: %+v", def.Steps)
	}
	for i, w := range want {
		if def.Steps[i].StepId != w.id || string(def.Steps[i].Kind) != w.kind {
			t.Fatalf("step %d: %+v", i, def.Steps[i])
		}
	}
	guard := mustJSON(def.Steps[len(def.Steps)-1].Guard)
	if !bytes.Contains(guard, []byte("opening_trial_balance_zero")) || !bytes.Contains(guard, []byte("approval_approved")) {
		t.Fatalf("unlock guard: %s", guard)
	}
}

func stateVersion(t *testing.T, result oapi.JourneyStepResult) int {
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

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
