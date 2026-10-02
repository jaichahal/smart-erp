package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

var (
	schemaOnce sync.Once
	schemaErr  error
)

func TestMain(m *testing.M) { os.Exit(m.Run()) }

func newDB(t *testing.T) *testdb.DB {
	t.Helper()
	db := testdb.New(t)
	schemaOnce.Do(func() { schemaErr = applySchema(db.Name) })
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	return db
}

func applySchema(name string) error {
	dsn := os.Getenv("ERP_MIGRATOR_DATABASE_URL")
	if dsn == "" {
		return errors.New("ERP_MIGRATOR_DATABASE_URL is required")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	u.Path = "/" + name
	sqlDB, err := sql.Open("pgx", u.String())
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

func newSvc(t *testing.T, db *testdb.DB, opts ...Option) *Service {
	t.Helper()
	client, err := outbox.NewClient(db.App, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Env: "dev", PushMode: "sink", PushSinkURL: "http://127.0.0.1:9"}
	svc, err := New(httpx.Deps{Pool: db.App, River: client, Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func principal(user string, company uuid.UUID, roles ...string) rls.Principal {
	if len(roles) == 0 {
		roles = []string{"user"}
	}
	return rls.Principal{UserID: user, CompanyID: company, Roles: roles}
}

func sampleEvent(t *testing.T, company uuid.UUID, actor, severity string, actions []string) Event {
	t.Helper()
	id, err := NewEventID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	num := "INV-1"
	party := "Al Noor Trading LLC"
	if severity == "" {
		severity = "HIGH"
	}
	if actions == nil {
		actions = []string{"approve", "reject", "open"}
	}
	return Event{
		EventID: id, Type: "approval.requested", Severity: severity, CompanyID: company.String(),
		OccurredAt: time.Now().UTC().Truncate(time.Second),
		Actor:      Actor{ID: actor, Name: "Actor"},
		Subject:    Subject{DocType: "sales_invoice", DocID: "doc-1", DocNumber: &num, Party: &party},
		Amount:     &Money{Amount: "14350.00", Currency: "AED"},
		DeepLink:   "smarterp://approval/doc-1", AllowedActions: actions, StateVersion: 3,
		Context: map[string]any{"request_id": "doc-1"},
	}
}

type fakePush struct {
	mu    sync.Mutex
	err   error
	unreg bool
	calls []string
}

func (f *fakePush) Send(_ context.Context, _, token string, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := data["notification"]; ok {
		return ErrNotificationBlock
	}
	f.calls = append(f.calls, token)
	if f.unreg {
		return &ErrTokenUnregistered{Reason: "UNREGISTERED"}
	}
	return f.err
}

func (f *fakePush) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeMail struct {
	mu     sync.Mutex
	bodies []string
}

func (m *fakeMail) Send(_ context.Context, _, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	return nil
}

type memDir struct{ users []User }

func (d memDir) Users(context.Context, string) ([]User, error) { return d.users, nil }

func insertTok(t *testing.T, db *testdb.DB, company uuid.UUID, user, device, token string, seen time.Time) {
	t.Helper()
	err := rls.Tx(context.Background(), db.App, rls.Principal{UserID: "system", CompanyID: company, Roles: []string{"system"}}, func(tx pgx.Tx) error {
		return upsertToken(context.Background(), tx, company.String(), user, device, token, "android", "1", seen)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func countQuery(t *testing.T, db *testdb.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Migrator.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// E1: a business write and its outbox row commit atomically; a rollback leaves neither.
func TestE1_AtomicOutbox(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	svc := newSvc(t, db)
	company := uuid.New()
	p := principal("actor", company)
	ev := sampleEvent(t, company, "actor", "HIGH", nil)

	tx, err := rls.Begin(ctx, db.App, p)
	if err != nil {
		t.Fatal(err)
	}
	rolled := "rolled-" + uuid.NewString()
	if _, err := insertSubmission(ctx, tx, company.String(), "sales_invoice", rolled, "submitted"); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, svc.River, tx, FanoutArgs{Event: ev}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, rolled); n != 0 {
		t.Fatalf("rolled-back submission still present: %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev.EventID); n != 0 {
		t.Fatalf("rolled-back job still present: %d", n)
	}

	ev2 := sampleEvent(t, company, "actor", "HIGH", nil)
	kept := "kept-" + uuid.NewString()
	if err := svc.CommitSubmission(ctx, p, "sales_invoice", kept, ev2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, kept); n != 1 {
		t.Fatalf("committed submissions: %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev2.EventID); n != 1 {
		t.Fatalf("committed jobs: %d", n)
	}
}

// E2: delivery is at least once and a second consume of the same event_id is a no-op.
func TestE2_DedupeByEventID(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	push := &fakePush{}
	svc := newSvc(t, db, WithPush(push), WithDirectory(memDir{users: []User{{ID: "recv", Email: "r@example.com"}}}))
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	insertTok(t, db, company, "recv", "d1", "tok-e2", time.Now())
	args := FanoutArgs{Event: ev, Explicit: []string{"recv"}}
	if err := svc.Deliver(ctx, args, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(ctx, args, 1); err != nil {
		t.Fatal(err)
	}
	if push.n() != 1 {
		t.Fatalf("push calls: %d", push.n())
	}
}

// E3: push payloads are data-only; a notification block is rejected, including by the dev push-sink.
func TestE3_DataOnlyPush(t *testing.T) {
	raw, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("event schema must refuse unknown keys, including a notification block")
	}
	if err := RejectNotificationBlock([]byte(`{"notification":{"title":"x"},"event_id":"y"}`)); !errors.Is(err, ErrNotificationBlock) {
		t.Fatalf("block: %v", err)
	}
	ev := sampleEvent(t, uuid.New(), "actor", "HIGH", nil)
	data, err := ev.PushData()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data["notification"]; ok {
		t.Fatal("flattened payload contains a notification key")
	}
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	sink := pushSinkURL(t)
	okResp, err := http.Post(sink, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer okResp.Body.Close()
	if okResp.StatusCode != http.StatusAccepted {
		t.Fatalf("data-only status %d", okResp.StatusCode)
	}
	bad, err := http.Post(sink, "application/json", strings.NewReader(`{"notification":{"title":"no"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("notification block status %d", bad.StatusCode)
	}

	cfg := &config.Config{PushMode: "live", Env: "dev"}
	t.Setenv("ERP_FCM_PROJECT_ID", "")
	t.Setenv("ERP_FCM_ACCESS_TOKEN", "")
	t.Setenv("ERP_APNS_TEAM_ID", "")
	t.Setenv("ERP_APNS_KEY_ID", "")
	t.Setenv("ERP_APNS_BUNDLE_ID", "")
	t.Setenv("ERP_APNS_AUTH_TOKEN", "")
	if err := RequirePushCredentials(cfg); err == nil {
		t.Fatal("live push must fail fast when FCM and APNs credentials are missing")
	}
}

// E4: an amount without a currency is refused before enqueue.
func TestE4_AmountRequiresCurrency(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	svc := newSvc(t, db)
	company := uuid.New()
	raw := []byte(`{"event_id":"01J8Q5X3K2M4N6P8R0S2T4V6W8","type":"approval.requested","severity":"HIGH","company_id":"` + company.String() + `","occurred_at":"2026-09-25T10:00:00Z","actor":{"id":"a","name":"A"},"subject":{"doc_type":"sales_invoice","doc_id":"d","doc_number":null,"party":null},"amount":{"amount":"10.00"},"deep_link":"smarterp://approval/d","allowed_actions":["open"],"state_version":1,"context":{}}`)
	if _, err := ParseEvent(raw); !errors.Is(err, ErrAmountCurrency) {
		t.Fatalf("parse: %v", err)
	}
	ev := sampleEvent(t, company, "a", "HIGH", nil)
	ev.Amount = &Money{Amount: "10.00", Currency: ""}
	before := countQuery(t, db, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,company_id}' = $1`, company.String())
	err := rls.Tx(ctx, db.App, principal("a", company), func(tx pgx.Tx) error {
		return Enqueue(ctx, svc.River, tx, FanoutArgs{Event: ev})
	})
	if !errors.Is(err, ErrAmountCurrency) {
		t.Fatalf("enqueue: %v", err)
	}
	after := countQuery(t, db, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,company_id}' = $1`, company.String())
	if after != before {
		t.Fatalf("job was enqueued: before %d after %d", before, after)
	}
}

// E5: the actor and disabled users are never recipients; role and explicit lists merge.
func TestE5_RecipientsExcludeActorAndDisabled(t *testing.T) {
	users := []User{
		{ID: "actor", Roles: []string{"approver"}},
		{ID: "disabled", Roles: []string{"approver"}, Disabled: true},
		{ID: "by-role", Roles: []string{"approver"}},
		{ID: "explicit", Roles: []string{"clerk"}},
		{ID: "other", Roles: []string{"clerk"}},
	}
	got := DeriveRecipients("actor", []string{"approver"}, []string{"explicit", "actor", "disabled"}, users)
	ids := map[string]struct{}{}
	for _, u := range got {
		ids[u.ID] = struct{}{}
	}
	if _, ok := ids["by-role"]; !ok {
		t.Fatal("role recipient missing")
	}
	if _, ok := ids["explicit"]; !ok {
		t.Fatal("explicit recipient missing")
	}
	if _, ok := ids["actor"]; ok {
		t.Fatal("actor was a recipient")
	}
	if _, ok := ids["disabled"]; ok {
		t.Fatal("disabled user was a recipient")
	}
	if _, ok := ids["other"]; ok {
		t.Fatal("unrelated user was a recipient")
	}
}

// E6: no preference row means opted in; quiet hours suppress all but Critical; the weekly digest batches FYI.
func TestE6_PreferencesQuietHoursDigest(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	push := &fakePush{}
	mail := &fakeMail{}
	dir := memDir{users: []User{
		{ID: "opted", Email: "o@example.com"},
		{ID: "quiet", Email: "q@example.com"},
		{ID: "critical", Email: "c@example.com"},
		{ID: "fyi", Email: "f@example.com"},
		{ID: "off", Email: "off@example.com"},
	}}
	svc := newSvc(t, db, WithPush(push), WithMailer(mail), WithDirectory(dir))
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	pQuiet := rls.Principal{UserID: "system", CompanyID: company, Roles: []string{"system"}}
	err := rls.Tx(ctx, db.App, pQuiet, func(tx pgx.Tx) error {
		if err := saveQuiet(ctx, tx, company.String(), "quiet", quietWindow{StartMin: 0, EndMin: 1440, Zone: "UTC"}); err != nil {
			return err
		}
		if err := saveQuiet(ctx, tx, company.String(), "critical", quietWindow{StartMin: 0, EndMin: 1440, Zone: "UTC"}); err != nil {
			return err
		}
		return savePreference(ctx, tx, company.String(), "off", "*", "push", "*", false)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"opted", "quiet", "critical", "off"} {
		insertTok(t, db, company, id, "d-"+id, "tok-"+id, now)
	}
	high := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(ctx, FanoutArgs{Event: high, Explicit: []string{"opted", "quiet", "off"}}, 1); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	push.mu.Lock()
	for _, tok := range push.calls {
		got[tok]++
	}
	push.mu.Unlock()
	if got["tok-opted"] != 1 {
		t.Fatalf("opted-in user (no preference row) pushes: %d", got["tok-opted"])
	}
	if got["tok-quiet"] != 0 || got["tok-off"] != 0 {
		t.Fatalf("suppressed pushes: %#v", got)
	}
	crit := sampleEvent(t, company, "actor", "CRITICAL", []string{"approve", "open"})
	if err := svc.Deliver(ctx, FanoutArgs{Event: crit, Explicit: []string{"critical"}}, 1); err != nil {
		t.Fatal(err)
	}
	if !calledToken(push, "tok-critical") {
		t.Fatal("critical was suppressed during quiet hours")
	}
	push.mu.Lock()
	beforeFYI := len(push.calls)
	push.mu.Unlock()
	for i := 0; i < 2; i++ {
		ev := sampleEvent(t, company, "actor", "LOW", []string{"acknowledge"})
		ev.Type = "document.registered"
		if err := svc.Deliver(ctx, FanoutArgs{Event: ev, Explicit: []string{"fyi"}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	push.mu.Lock()
	if len(push.calls) != beforeFYI {
		t.Fatalf("FYI was pushed immediately: %d extra", len(push.calls)-beforeFYI)
	}
	push.mu.Unlock()
	if err := svc.FlushDigest(ctx, company.String()); err != nil {
		t.Fatal(err)
	}
	mail.mu.Lock()
	bodies := append([]string(nil), mail.bodies...)
	mail.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("digest emails: %d", len(bodies))
	}
	var batched []string
	if err := json.Unmarshal([]byte(bodies[0]), &batched); err != nil {
		t.Fatal(err)
	}
	if len(batched) != 2 {
		t.Fatalf("digest batch: %#v", batched)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.delivery_log WHERE company_id = $1 AND channel = 'digest' AND recipient_user_id = 'fyi'`, company.String()); n != 1 {
		t.Fatalf("digest delivery rows: %d", n)
	}

	actor := principal("audit-user", company)
	auditCtx := rls.WithPrincipal(ctx, actor)
	if err := rls.Tx(auditCtx, db.App, actor, func(tx pgx.Tx) error {
		_, err := audit.Emit(auditCtx, tx, audit.Event{
			Type: "config.changed", ReferenceType: "notification_preferences", ReferenceID: actor.UserID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := countQuery(t, db, `SELECT count(*) FROM erp.audit_events WHERE actor_id = $1`, actor.UserID)
	offEv := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(ctx, FanoutArgs{Event: offEv, Explicit: []string{"off"}}, 1); err != nil {
		t.Fatal(err)
	}
	after := countQuery(t, db, `SELECT count(*) FROM erp.audit_events WHERE actor_id = $1`, actor.UserID)
	if after != before || before == 0 {
		t.Fatalf("preference suppressed or skipped the audit row: before %d after %d", before, after)
	}
}

func calledToken(f *fakePush, token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == token {
			return true
		}
	}
	return false
}

// E7: an unregistered provider response deletes the token; tokens unseen for 60 days are pruned.
// Register is idempotent and unregister touches only the caller's device.
func TestE7_TokenLifecycle(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	push := &fakePush{unreg: true}
	svc := newSvc(t, db, WithPush(push), WithDirectory(memDir{users: []User{{ID: "owner"}}}))
	insertTok(t, db, company, "owner", "phone", "tok-dead", time.Now())
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(ctx, FanoutArgs{Event: ev, Explicit: []string{"owner"}}, 1); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE token = 'tok-dead'`); n != 0 {
		t.Fatalf("unregistered token still stored: %d", n)
	}

	old := time.Now().Add(-61 * 24 * time.Hour)
	insertTok(t, db, company, "owner", "stale", "tok-stale", old)
	insertTok(t, db, company, "owner", "fresh", "tok-fresh", time.Now())
	err := rls.Tx(ctx, db.App, rls.Principal{UserID: "system", CompanyID: company, Roles: []string{"system"}}, func(tx pgx.Tx) error {
		_, err := PruneTokens(ctx, tx, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE token = 'tok-stale'`); n != 0 {
		t.Fatal("stale token was not pruned")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE token = 'tok-fresh'`); n != 1 {
		t.Fatalf("fresh token count %d", n)
	}

	router := chi.NewRouter()
	userA := principal("user-a", company)
	userB := principal("user-b", company)
	current := userA
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), current)))
		})
	})
	router.Use(apierr.RequestIDMiddleware)
	deps := httpx.Deps{Pool: db.App, Config: &config.Config{Env: "dev", PushMode: "sink"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := Mount(router, deps); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	postTok := func(user rls.Principal, token, device, key string) {
		t.Helper()
		current = user
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/devices/push-token", strings.NewReader(`{"token":"`+token+`","platform":"android","app_version":"1.2.3"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(DeviceHeader, device)
		req.Header.Set(idempotency.Header, key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("register %s: %d %s", token, resp.StatusCode, b)
		}
	}
	key := uuid.NewString()
	postTok(userA, "tok-a", "dev-a", key)
	postTok(userA, "tok-a", "dev-a", key)
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE user_id = 'user-a' AND device_id = 'dev-a'`); n != 1 {
		t.Fatalf("idempotent register rows: %d", n)
	}
	postTok(userB, "tok-b", "dev-b", uuid.NewString())
	current = userA
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/devices/push-token", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(DeviceHeader, "dev-a")
	req.Header.Set(idempotency.Header, uuid.NewString())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE token = 'tok-a'`); n != 0 {
		t.Fatal("caller token was not deleted")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.device_tokens WHERE token = 'tok-b'`); n != 1 {
		t.Fatal("another user's token was deleted")
	}
}

// E8: a delivery failure records a Failed row, retries with backoff, and email is its own row.
func TestE8_FailureRetryAndEmail(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	push := &fakePush{err: errors.New("temporary")}
	mail := &fakeMail{}
	svc := newSvc(t, db, WithPush(push), WithMailer(mail), WithDirectory(memDir{users: []User{{ID: "recv", Email: "r@example.com"}}}))
	insertTok(t, db, company, "recv", "d", "tok-e8", time.Now())
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	args := FanoutArgs{Event: ev, Explicit: []string{"recv"}}
	if err := svc.Deliver(ctx, args, 1); err == nil {
		t.Fatal("expected delivery error")
	}
	if err := svc.Deliver(ctx, args, 2); err == nil {
		t.Fatal("expected second delivery error")
	}
	failed := countQuery(t, db, `SELECT count(*) FROM erp.delivery_log WHERE event_id = $1 AND channel = 'push' AND status = 'failed'`, ev.EventID)
	if failed != 2 {
		t.Fatalf("failed push rows: %d", failed)
	}
	email := countQuery(t, db, `SELECT count(*) FROM erp.delivery_log WHERE event_id = $1 AND channel = 'email'`, ev.EventID)
	if email < 1 {
		t.Fatalf("email fallback rows: %d", email)
	}
	w := &FanoutWorker{Svc: svc}
	t1 := w.NextRetry(&river.Job[FanoutArgs]{JobRow: &rivertype.JobRow{Attempt: 1}})
	t2 := w.NextRetry(&river.Job[FanoutArgs]{JobRow: &rivertype.JobRow{Attempt: 2}})
	if !t2.After(t1) {
		t.Fatalf("backoff did not grow: attempt1 %s attempt2 %s", t1, t2)
	}
}

// E9: an action on one surface clears the item on a second connected surface within one second.
func TestE9_ClearSecondSurfaceWithinOneSecond(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	user := principal("shade-user", company)
	svc := newSvc(t, db)
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	err := rls.Tx(ctx, db.App, user, func(tx pgx.Tx) error {
		return insertInbox(ctx, tx, company.String(), user.UserID, ev, "needs_me")
	})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), user)))
		})
	})
	router.Get("/ws", svc.serveWS)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c1, err := dialWS(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c1.close() })
	c2, err := dialWS(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c2.close() })
	_ = c1.setDeadline(time.Now().Add(3 * time.Second))
	_ = c2.setDeadline(time.Now().Add(3 * time.Second))
	deadline := time.Now().Add(time.Second)
	for svc.Hub.ConnectionCount(user.UserID) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.Hub.ConnectionCount(user.UserID) < 2 {
		t.Fatalf("connections: %d", svc.Hub.ConnectionCount(user.UserID))
	}
	got := make(chan []byte, 1)
	go func() {
		msg, err := c2.readText()
		if err != nil {
			return
		}
		got <- msg
	}()
	start := time.Now()
	if err := c1.writeText([]byte(`{"ack":"` + ev.EventID + `"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if time.Since(start) > time.Second {
			t.Fatalf("second surface cleared after %s", time.Since(start))
		}
		var body map[string]any
		if err := json.Unmarshal(msg, &body); err != nil {
			t.Fatal(err)
		}
		if body["type"] != "notification.cleared" || body["event_id"] != ev.EventID {
			t.Fatalf("clear message: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("second surface did not clear within one second")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notifications WHERE event_id = $1 AND cleared_at IS NOT NULL`, ev.EventID); n != 1 {
		t.Fatalf("cleared rows: %d", n)
	}
}

type fakeApprovals struct {
	version int64
	decided bool
	fetched bool
}

func (f *fakeApprovals) Fetch(context.Context, string) (LiveDocument, error) {
	f.fetched = true
	return LiveDocument{ID: "req", State: "pending", StateVersion: f.version, FraudHints: []string{"new payee"}}, nil
}

func (f *fakeApprovals) Decide(context.Context, string, int64, string, string) error {
	f.decided = true
	return nil
}

// E10: approve from the shade fetches the live document and refuses when state_version changed.
func TestE10_ApproveRefusesStaleStateVersion(t *testing.T) {
	db := newDB(t)
	appr := &fakeApprovals{version: 4}
	svc := newSvc(t, db, WithApprovals(appr))
	req := httptest.NewRequest(http.MethodPost, "/notifications/req/approve", strings.NewReader(`{"comment":"ok"}`))
	req.Header.Set("If-Match", "3")
	if _, err := svc.ApproveFromShade(req.Context(), req, "req", "approve", ""); err == nil {
		t.Fatal("expected state_version conflict")
	}
	if !appr.fetched {
		t.Fatal("live document was not fetched")
	}
	if appr.decided {
		t.Fatal("stale approve committed")
	}
	appr.version = 3
	req2 := httptest.NewRequest(http.MethodPost, "/notifications/req/approve", strings.NewReader(`{"comment":"ok"}`))
	req2.Header.Set("If-Match", "3")
	if _, err := svc.ApproveFromShade(req2.Context(), req2, "req", "approve", ""); err != nil {
		t.Fatal(err)
	}
	if !appr.decided {
		t.Fatal("matching version did not commit")
	}
}

// E11: alert dispatch failure never rolls back the business write.
func TestE11_DispatchFailureDoesNotRollback(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	push := &fakePush{err: errors.New("provider down")}
	svc := newSvc(t, db, WithPush(push), WithMailer(&fakeMail{}), WithDirectory(memDir{users: []User{{ID: "recv", Email: "r@example.com"}}}))
	p := principal("actor", company)
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	doc := "biz-e11-" + uuid.NewString()
	if err := svc.CommitSubmission(ctx, p, "sales_invoice", doc, ev, nil, []string{"recv"}); err != nil {
		t.Fatal(err)
	}
	insertTok(t, db, company, "recv", "d", "tok-e11-"+doc, time.Now())
	if err := svc.Deliver(ctx, FanoutArgs{Event: ev, Explicit: []string{"recv"}}, 1); err == nil {
		t.Fatal("expected dispatch failure")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, doc); n != 1 {
		t.Fatalf("business write survived: %d", n)
	}
}

// E12: blocking rules prevent submission; advisory rules notify only.
func TestE12_BlockingAndAdvisoryRules(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	company := uuid.New()
	svc := newSvc(t, db)
	p := principal("admin", company, "admin")
	err := rls.Tx(ctx, db.App, p, func(tx pgx.Tx) error {
		_, err := insertRule(ctx, tx, AlertRule{
			CompanyID: company.String(), DocumentType: "sales_invoice",
			Condition:      map[string]any{"op": "amount_gt", "value": "100.00"},
			RecipientRoles: []string{"approver"}, Channel: "push", Severity: "HIGH",
			Mode: "blocking", Enabled: true, Builtin: true,
		})
		if err != nil {
			return err
		}
		_, err = insertRule(ctx, tx, AlertRule{
			CompanyID: company.String(), DocumentType: "purchase_invoice",
			Condition:      map[string]any{"op": "amount_gt", "value": "1.00"},
			RecipientRoles: []string{"approver"}, Channel: "push", Severity: "MEDIUM",
			Mode: "advisory", Enabled: true,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := "blocked-" + uuid.NewString()
	advised := "advised-" + uuid.NewString()
	if err := svc.Submit(ctx, p, "sales_invoice", blocked, "200.00", Event{}, nil, nil); !errors.Is(err, ErrBlockingAlert) {
		t.Fatalf("blocking: %v", err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, blocked); n != 0 {
		t.Fatalf("blocking submitted: %d", n)
	}
	ev := sampleEvent(t, company, "admin", "LOW", []string{"open"})
	ev.Subject.DocType = "purchase_invoice"
	if err := svc.Submit(ctx, p, "purchase_invoice", advised, "50.00", ev, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, advised); n != 1 {
		t.Fatalf("advisory submissions: %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev.EventID); n != 1 {
		t.Fatalf("advisory jobs: %d", n)
	}
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "contracts", "events", "event.schema.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func pushSinkURL(t *testing.T) string {
	t.Helper()
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:8090", 200*time.Millisecond); err == nil {
		_ = conn.Close()
		resp, err := http.Get("http://127.0.0.1:8090/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return "http://127.0.0.1:8090/"
			}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	root := apiRoot(t)
	var logs strings.Builder
	cmd := exec.Command("go", "run", "./cmd/pushsink")
	cmd.Dir = root
	cmd.Env = replaceEnv(os.Environ(), "ERP_PUSH_SINK_ADDR", addr)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	deadline := time.Now().Add(90 * time.Second)
	url := "http://" + addr + "/"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("dev push-sink did not start: %s", logs.String())
	return ""
}

func replaceEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+val)
}

func apiRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("go.mod not found")
		}
		wd = parent
	}
}
