package e2e

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/notifications"
)

func init() {
	registerAcceptance("E1", acceptE1)
	registerAcceptance("E2", acceptE2)
	registerAcceptance("E3", acceptE3)
	registerAcceptance("E4", acceptE4)
	registerAcceptance("E5", acceptE5)
	registerAcceptance("E6", acceptE6)
	registerAcceptance("E7", acceptE7)
	registerAcceptance("E8", acceptE8)
	registerAcceptance("E9", acceptE9)
	registerAcceptance("E10", acceptE10)
	registerAcceptance("E11", acceptE11)
	registerAcceptance("E12", acceptE12)
}

type loggedIn struct {
	account identity.Account
	access  string
	device  string
}

func (s *stack) member(login, password string, company uuid.UUID, roles []string, disabled bool) identity.Account {
	a := identity.Account{
		ID: uuid.NewString(), LoginName: login, Name: login, CompanyID: company,
		Roles: roles, Personas: []string{"staff"}, Disabled: disabled,
		StepUpMethods: []string{"totp"},
	}
	s.dir.put(a)
	s.broker.add(login, a.ID, password, "654321")
	return a
}

func (s *stack) login(t *testing.T, a identity.Account, password string) loggedIn {
	t.Helper()
	pub := consolePublicKey(t)
	body, err := json.Marshal(map[string]any{
		"public_key": pub, "platform": "console", "app_version": "1.0.0", "device_name": "console",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := s.ok(t, http.MethodPost, "/api/v1/auth/device/enroll", string(body), nil, http.StatusCreated)
	device, _ := dataField(t, raw)["device_id"].(string)
	if device == "" {
		t.Fatalf("enrol: %s", raw)
	}
	raw = s.ok(t, http.MethodPost, "/api/v1/auth/session", `{"login_name":"`+a.LoginName+`"}`, nil, http.StatusOK)
	sid, _ := dataField(t, raw)["session_id"].(string)
	if sid == "" {
		t.Fatalf("session: %s", raw)
	}
	s.ok(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", `{"password":"`+password+`"}`, nil, http.StatusOK)
	raw = s.ok(t, http.MethodPost, "/api/v1/auth/token", `{"session_id":"`+sid+`","device_id":"`+device+`"}`, nil, http.StatusOK)
	access, _ := dataField(t, raw)["access_token"].(string)
	if access == "" {
		t.Fatalf("token: %s", raw)
	}
	return loggedIn{account: a, access: access, device: device}
}

func (s *stack) authz(access string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + access}
}

func (s *stack) ok(t *testing.T, method, path, body string, hdr map[string]string, want int) []byte {
	t.Helper()
	status, code, raw := s.call(t, method, path, body, hdr)
	if status != want {
		t.Fatalf("%s %s -> %d code %q (want %d) %s", method, path, status, code, want, trim(raw))
	}
	return raw
}

func dataField(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("data: %v %s", err, raw)
	}
	if env.Data == nil {
		t.Fatalf("data envelope: %s", raw)
	}
	return env.Data
}

func consolePublicKey(t *testing.T) map[string]any {
	t.Helper()
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (s *stack) notify(t *testing.T, opts ...notifications.Option) *notifications.Service {
	t.Helper()
	client, err := outbox.NewClient(s.db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Env: "dev", PushMode: "sink", PushSinkURL: "http://127.0.0.1:9"}
	svc, err := notifications.New(httpx.Deps{
		Pool: s.db.App, River: client, Config: cfg, Log: slog.New(slog.DiscardHandler),
	}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (s *stack) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.Migrator.QueryRow(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sampleEvent(t *testing.T, company uuid.UUID, actor, severity string, actions []string) notifications.Event {
	t.Helper()
	id, err := notifications.NewEventID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if severity == "" {
		severity = "HIGH"
	}
	if actions == nil {
		actions = []string{"approve", "reject", "open"}
	}
	num := "INV-1"
	party := "Al Noor Trading LLC"
	return notifications.Event{
		EventID: id, Type: "approval.requested", Severity: severity, CompanyID: company.String(),
		OccurredAt: time.Now().UTC().Truncate(time.Second),
		Actor:      notifications.Actor{ID: actor, Name: "Actor"},
		Subject:    notifications.Subject{DocType: "sales_invoice", DocID: "doc-" + id, DocNumber: &num, Party: &party},
		Amount:     &notifications.Money{Amount: "14350.00", Currency: "AED"},
		DeepLink:   "smarterp://approval/" + id, AllowedActions: actions, StateVersion: 3,
		Context: map[string]any{"request_id": id},
	}
}

func principalOf(a identity.Account, roles ...string) rls.Principal {
	if len(roles) == 0 {
		roles = a.Roles
	}
	return rls.Principal{UserID: a.ID, CompanyID: a.CompanyID, Roles: roles}
}

type recordingPush struct {
	mu    sync.Mutex
	err   error
	unreg bool
	calls []string
}

func (f *recordingPush) Send(_ context.Context, _, token string, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := data["notification"]; ok {
		return notifications.ErrNotificationBlock
	}
	f.calls = append(f.calls, token)
	if f.unreg {
		return &notifications.ErrTokenUnregistered{Reason: "UNREGISTERED"}
	}
	return f.err
}

func (f *recordingPush) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type recordingMail struct {
	mu     sync.Mutex
	bodies []string
}

func (m *recordingMail) Send(_ context.Context, _, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	return nil
}

type userDir struct{ users []notifications.User }

func (d userDir) Users(context.Context, string) ([]notifications.User, error) { return d.users, nil }

// E1: a business write and its outbox row commit atomically; a crash between them leaves neither.
func acceptE1(t *testing.T, s *stack) {
	svc := s.notify(t)
	company := uuid.New()
	actor := s.member("e1-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	p := principalOf(actor)
	ev := sampleEvent(t, company, actor.ID, "HIGH", nil)
	ctx := t.Context()
	tx, err := rls.Begin(ctx, s.db.App, p)
	if err != nil {
		t.Fatal(err)
	}
	rolled := "rolled-" + uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO erp.notification_submissions (company_id, document_type, document_id, state) VALUES ($1,'sales_invoice',$2,'submitted')`, company.String(), rolled); err != nil {
		t.Fatal(err)
	}
	if err := notifications.Enqueue(ctx, svc.River, tx, notifications.FanoutArgs{Event: ev}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, rolled); n != 0 {
		t.Fatalf("rolled-back submission still present: %d", n)
	}
	if n := s.count(t, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev.EventID); n != 0 {
		t.Fatalf("rolled-back job still present: %d", n)
	}
	ev2 := sampleEvent(t, company, actor.ID, "HIGH", nil)
	kept := "kept-" + uuid.NewString()
	if err := svc.CommitSubmission(ctx, p, "sales_invoice", kept, ev2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, kept); n != 1 {
		t.Fatalf("committed submissions: %d", n)
	}
	if n := s.count(t, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev2.EventID); n != 1 {
		t.Fatalf("committed jobs: %d", n)
	}
}

// E2: delivery is at least once and a second consume of the same event_id is a no-op.
func acceptE2(t *testing.T, s *stack) {
	company := uuid.New()
	recv := s.member("e2-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	in := s.login(t, recv, "secret")
	token := "tok-e2-" + uuid.NewString()
	s.registerToken(t, in, token)
	push := &recordingPush{}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithDirectory(userDir{users: []notifications.User{{ID: recv.ID, Email: "r@example.com"}}}))
	ev := sampleEvent(t, company, "someone-else", "HIGH", nil)
	args := notifications.FanoutArgs{Event: ev, Explicit: []string{recv.ID}}
	if err := svc.Deliver(t.Context(), args, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), args, 1); err != nil {
		t.Fatal(err)
	}
	if got := push.tokens(); len(got) != 1 || got[0] != token {
		t.Fatalf("push calls: %#v", got)
	}
	raw := s.ok(t, http.MethodGet, "/api/v1/notifications", "", s.authz(in.access), http.StatusOK)
	if !strings.Contains(string(raw), ev.EventID) {
		t.Fatalf("inbox missing event: %s", raw)
	}
}

func (s *stack) registerToken(t *testing.T, in loggedIn, token string) {
	t.Helper()
	s.registerTokenOn(t, in, in.device, token)
}

func (s *stack) registerTokenOn(t *testing.T, in loggedIn, device, token string) {
	t.Helper()
	body := `{"token":"` + token + `","platform":"android","app_version":"1.2.3"}`
	hdr := s.authz(in.access)
	hdr[notifications.DeviceHeader] = device
	s.ok(t, http.MethodPost, "/api/v1/devices/push-token", body, hdr, http.StatusOK)
}

// E3: push payloads are data-only; a notification block fails schema validation.
func acceptE3(t *testing.T, s *stack) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(), "contracts", "events", "event.schema.json"))
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
	if err := notifications.RejectNotificationBlock([]byte(`{"notification":{"title":"x"},"event_id":"y"}`)); !errors.Is(err, notifications.ErrNotificationBlock) {
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
	if err := notifications.RequirePushCredentials(cfg); err == nil {
		t.Fatal("live push must fail fast when FCM and APNs credentials are missing")
	}
	_ = s
}

// E4: an amount without a currency is refused before enqueue.
func acceptE4(t *testing.T, s *stack) {
	svc := s.notify(t)
	company := uuid.New()
	actor := s.member("e4-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	raw := []byte(`{"event_id":"01J8Q5X3K2M4N6P8R0S2T4V6W8","type":"approval.requested","severity":"HIGH","company_id":"` + company.String() + `","occurred_at":"2026-09-25T10:00:00Z","actor":{"id":"a","name":"A"},"subject":{"doc_type":"sales_invoice","doc_id":"d","doc_number":null,"party":null},"amount":{"amount":"10.00"},"deep_link":"smarterp://approval/d","allowed_actions":["open"],"state_version":1,"context":{}}`)
	if _, err := notifications.ParseEvent(raw); !errors.Is(err, notifications.ErrAmountCurrency) {
		t.Fatalf("parse: %v", err)
	}
	ev := sampleEvent(t, company, actor.ID, "HIGH", nil)
	ev.Amount = &notifications.Money{Amount: "10.00", Currency: ""}
	before := s.count(t, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,company_id}' = $1`, company.String())
	err := rls.Tx(t.Context(), s.db.App, principalOf(actor), func(tx pgx.Tx) error {
		return notifications.Enqueue(t.Context(), svc.River, tx, notifications.FanoutArgs{Event: ev})
	})
	if !errors.Is(err, notifications.ErrAmountCurrency) {
		t.Fatalf("enqueue: %v", err)
	}
	after := s.count(t, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,company_id}' = $1`, company.String())
	if after != before {
		t.Fatalf("job was enqueued: before %d after %d", before, after)
	}
}

// E5: the actor and disabled users are never recipients; role and explicit lists merge.
func acceptE5(t *testing.T, s *stack) {
	company := uuid.New()
	actor := s.member("e5-actor-"+uuid.NewString(), "secret", company, []string{"approver"}, false)
	disabled := s.member("e5-off-"+uuid.NewString(), "secret", company, []string{"approver"}, true)
	byRole := s.member("e5-role-"+uuid.NewString(), "secret", company, []string{"approver"}, false)
	explicit := s.member("e5-exp-"+uuid.NewString(), "secret", company, []string{"clerk"}, false)
	other := s.member("e5-other-"+uuid.NewString(), "secret", company, []string{"clerk"}, false)
	users := []notifications.User{
		{ID: actor.ID, Roles: []string{"approver"}},
		{ID: disabled.ID, Roles: []string{"approver"}, Disabled: true},
		{ID: byRole.ID, Roles: []string{"approver"}},
		{ID: explicit.ID, Roles: []string{"clerk"}},
		{ID: other.ID, Roles: []string{"clerk"}},
	}
	got := notifications.DeriveRecipients(actor.ID, []string{"approver"}, []string{explicit.ID, actor.ID, disabled.ID}, users)
	ids := map[string]struct{}{}
	for _, u := range got {
		ids[u.ID] = struct{}{}
	}
	if _, ok := ids[byRole.ID]; !ok {
		t.Fatal("role recipient missing")
	}
	if _, ok := ids[explicit.ID]; !ok {
		t.Fatal("explicit recipient missing")
	}
	if _, ok := ids[actor.ID]; ok {
		t.Fatal("actor was a recipient")
	}
	if _, ok := ids[disabled.ID]; ok {
		t.Fatal("disabled user was a recipient")
	}
	if _, ok := ids[other.ID]; ok {
		t.Fatal("unrelated user was a recipient")
	}
	in := s.login(t, byRole, "secret")
	token := "tok-e5-" + uuid.NewString()
	s.registerToken(t, in, token)
	push := &recordingPush{}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithDirectory(userDir{users: users}))
	ev := sampleEvent(t, company, actor.ID, "HIGH", nil)
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: ev, Roles: []string{"approver"}, Explicit: []string{explicit.ID, actor.ID, disabled.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Migrator.Query(t.Context(), `SELECT user_id FROM erp.notifications WHERE event_id = $1`, ev.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		seen[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !seen[byRole.ID] || !seen[explicit.ID] || seen[actor.ID] || seen[disabled.ID] || seen[other.ID] {
		t.Fatalf("inbox recipients: %#v", seen)
	}
	raw := s.ok(t, http.MethodGet, "/api/v1/notifications", "", s.authz(in.access), http.StatusOK)
	if !strings.Contains(string(raw), ev.EventID) {
		t.Fatalf("role recipient list: %s", raw)
	}
	actorIn := s.login(t, actor, "secret")
	raw = s.ok(t, http.MethodGet, "/api/v1/notifications", "", s.authz(actorIn.access), http.StatusOK)
	if strings.Contains(string(raw), ev.EventID) {
		t.Fatalf("actor saw the event: %s", raw)
	}
}

// E6: no preference row means opted in; quiet hours suppress all but Critical; the weekly digest batches FYI.
func acceptE6(t *testing.T, s *stack) {
	company := uuid.New()
	opted := s.member("e6-opt-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	quiet := s.member("e6-quiet-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	critical := s.member("e6-crit-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	fyi := s.member("e6-fyi-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	off := s.member("e6-off-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	optIn := s.login(t, opted, "secret")
	quietIn := s.login(t, quiet, "secret")
	critIn := s.login(t, critical, "secret")
	offIn := s.login(t, off, "secret")
	putPref := func(in loggedIn, body string) {
		t.Helper()
		s.ok(t, http.MethodPut, "/api/v1/me/notification-preferences", body, s.authz(in.access), http.StatusOK)
	}
	allDay := `{"quiet_hours":{"start":"00:00","end":"24:00","zone":"UTC"}}`
	putPref(quietIn, allDay)
	putPref(critIn, allDay)
	putPref(offIn, `{"channels":[{"event_type":"*","channel":"push","device_id":"*","enabled":false}]}`)
	tok := map[string]string{}
	for _, in := range []loggedIn{optIn, quietIn, critIn, offIn} {
		token := "tok-" + in.account.ID
		s.registerToken(t, in, token)
		tok[in.account.ID] = token
	}
	users := []notifications.User{
		{ID: opted.ID, Email: "o@example.com"},
		{ID: quiet.ID, Email: "q@example.com"},
		{ID: critical.ID, Email: "c@example.com"},
		{ID: fyi.ID, Email: "f@example.com"},
		{ID: off.ID, Email: "off@example.com"},
	}
	push := &recordingPush{}
	mail := &recordingMail{}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	svc := s.notify(t,
		notifications.WithPush(push),
		notifications.WithMailer(mail),
		notifications.WithDirectory(userDir{users: users}),
		notifications.WithClock(func() time.Time { return now }),
	)
	high := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: high, Explicit: []string{opted.ID, quiet.ID, off.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, token := range push.tokens() {
		got[token]++
	}
	if got[tok[opted.ID]] != 1 {
		t.Fatalf("opted-in user (no preference row) pushes: %d", got[tok[opted.ID]])
	}
	if got[tok[quiet.ID]] != 0 || got[tok[off.ID]] != 0 {
		t.Fatalf("suppressed pushes: %#v", got)
	}
	crit := sampleEvent(t, company, "actor", "CRITICAL", []string{"approve", "open"})
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: crit, Explicit: []string{critical.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	if !containsToken(push, tok[critical.ID]) {
		t.Fatal("critical was suppressed during quiet hours")
	}
	beforeFYI := len(push.tokens())
	var fyiIDs []string
	for i := 0; i < 2; i++ {
		ev := sampleEvent(t, company, "actor", "LOW", []string{"acknowledge"})
		ev.Type = "document.registered"
		fyiIDs = append(fyiIDs, ev.EventID)
		if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: ev, Explicit: []string{fyi.ID}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(push.tokens()) != beforeFYI {
		t.Fatalf("FYI was pushed immediately")
	}
	if err := svc.FlushDigest(t.Context(), company.String()); err != nil {
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
		t.Fatalf("digest batch: %#v want %v", batched, fyiIDs)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.delivery_log WHERE company_id = $1 AND channel = 'digest' AND recipient_user_id = $2`, company.String(), fyi.ID); n != 1 {
		t.Fatalf("digest delivery rows: %d", n)
	}
	auditUser := s.member("e6-audit-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	auditP := principalOf(auditUser)
	auditCtx := rls.WithPrincipal(t.Context(), auditP)
	if err := rls.Tx(auditCtx, s.db.App, auditP, func(tx pgx.Tx) error {
		_, err := audit.Emit(auditCtx, tx, audit.Event{
			Type: "config.changed", ReferenceType: "notification_preferences", ReferenceID: auditUser.ID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := s.count(t, `SELECT count(*) FROM erp.audit_events WHERE actor_id = $1`, auditUser.ID)
	offEv := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: offEv, Explicit: []string{off.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	after := s.count(t, `SELECT count(*) FROM erp.audit_events WHERE actor_id = $1`, auditUser.ID)
	if after != before || before == 0 {
		t.Fatalf("preference suppressed or skipped the audit row: before %d after %d", before, after)
	}
}

func containsToken(f *recordingPush, token string) bool {
	for _, c := range f.tokens() {
		if c == token {
			return true
		}
	}
	return false
}

// E7: an unregistered provider response deletes the token; tokens unseen for 60 days are pruned.
func acceptE7(t *testing.T, s *stack) {
	company := uuid.New()
	owner := s.member("e7-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	other := s.member("e7b-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	in := s.login(t, owner, "secret")
	otherIn := s.login(t, other, "secret")
	dead := "tok-dead-" + uuid.NewString()
	s.registerToken(t, in, dead)
	push := &recordingPush{unreg: true}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithDirectory(userDir{users: []notifications.User{{ID: owner.ID}}}))
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: ev, Explicit: []string{owner.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE token = $1`, dead); n != 0 {
		t.Fatalf("unregistered token still stored: %d", n)
	}
	stale := "tok-stale-" + uuid.NewString()
	fresh := "tok-fresh-" + uuid.NewString()
	s.registerTokenOn(t, in, "stale-dev-"+owner.ID, stale)
	s.registerTokenOn(t, in, "fresh-dev-"+owner.ID, fresh)
	otherTok := "tok-other-" + uuid.NewString()
	s.registerToken(t, otherIn, otherTok)
	if _, err := s.db.Migrator.Exec(t.Context(), `UPDATE erp.device_tokens SET last_seen_at = $2 WHERE token = $1`, stale, time.Now().Add(-61*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	err := rls.Tx(t.Context(), s.db.App, rls.Principal{UserID: "system", CompanyID: company, Roles: []string{"system"}}, func(tx pgx.Tx) error {
		_, err := notifications.PruneTokens(t.Context(), tx, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE token = $1`, stale); n != 0 {
		t.Fatal("stale token was not pruned")
	}
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE token = $1`, fresh); n != 1 {
		t.Fatalf("fresh token count %d", n)
	}
	keyBody := `{"token":"` + otherTok + `","platform":"android","app_version":"1.2.3"}`
	hdr := s.authz(otherIn.access)
	hdr[notifications.DeviceHeader] = otherIn.device
	key := uuid.NewString()
	hdr["Idempotency-Key"] = key
	s.ok(t, http.MethodPost, "/api/v1/devices/push-token", keyBody, hdr, http.StatusOK)
	s.ok(t, http.MethodPost, "/api/v1/devices/push-token", keyBody, hdr, http.StatusOK)
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE user_id = $1 AND device_id = $2`, other.ID, otherIn.device); n != 1 {
		t.Fatalf("idempotent register rows: %d", n)
	}
	del := s.authz(in.access)
	del[notifications.DeviceHeader] = in.device
	s.ok(t, http.MethodDelete, "/api/v1/devices/push-token", "", del, http.StatusOK)
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE user_id = $1 AND device_id = $2`, owner.ID, in.device); n != 0 {
		t.Fatal("caller token was not deleted")
	}
	if n := s.count(t, `SELECT count(*) FROM erp.device_tokens WHERE token = $1`, otherTok); n != 1 {
		t.Fatal("another user's token was deleted")
	}
}

// E8: a delivery failure records a Failed row, retries with backoff, and email is its own row.
func acceptE8(t *testing.T, s *stack) {
	company := uuid.New()
	recv := s.member("e8-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	in := s.login(t, recv, "secret")
	s.registerToken(t, in, "tok-e8-"+uuid.NewString())
	push := &recordingPush{err: errors.New("temporary")}
	mail := &recordingMail{}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithMailer(mail), notifications.WithDirectory(userDir{users: []notifications.User{{ID: recv.ID, Email: "r@example.com"}}}))
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	args := notifications.FanoutArgs{Event: ev, Explicit: []string{recv.ID}}
	if err := svc.Deliver(t.Context(), args, 1); err == nil {
		t.Fatal("expected delivery error")
	}
	if err := svc.Deliver(t.Context(), args, 2); err == nil {
		t.Fatal("expected second delivery error")
	}
	failed := s.count(t, `SELECT count(*) FROM erp.delivery_log WHERE event_id = $1 AND channel = 'push' AND status = 'failed'`, ev.EventID)
	if failed != 2 {
		t.Fatalf("failed push rows: %d", failed)
	}
	email := s.count(t, `SELECT count(*) FROM erp.delivery_log WHERE event_id = $1 AND channel = 'email'`, ev.EventID)
	if email < 1 {
		t.Fatalf("email fallback rows: %d", email)
	}
	w := &notifications.FanoutWorker{Svc: svc}
	t1 := w.NextRetry(&river.Job[notifications.FanoutArgs]{JobRow: &rivertype.JobRow{Attempt: 1}})
	t2 := w.NextRetry(&river.Job[notifications.FanoutArgs]{JobRow: &rivertype.JobRow{Attempt: 2}})
	if !t2.After(t1) {
		t.Fatalf("backoff did not grow: attempt1 %s attempt2 %s", t1, t2)
	}
}

// E9: an action on one surface clears the item on a second connected surface within one second.
func acceptE9(t *testing.T, s *stack) {
	company := uuid.New()
	user := s.member("e9-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	in := s.login(t, user, "secret")
	s.registerToken(t, in, "tok-e9-"+uuid.NewString())
	push := &recordingPush{}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithDirectory(userDir{users: []notifications.User{{ID: user.ID}}}))
	ev := sampleEvent(t, company, "actor", "HIGH", nil)
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: ev, Explicit: []string{user.ID}}, 1); err != nil {
		t.Fatal(err)
	}
	c1, err := dialSurface(s.srv.URL, "/api/v1/ws", in.access)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c1.Close() })
	c2, err := dialSurface(s.srv.URL, "/api/v1/ws", in.access)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c2.Close() })
	_ = c1.SetDeadline(time.Now().Add(3 * time.Second))
	_ = c2.SetDeadline(time.Now().Add(3 * time.Second))
	got := make(chan []byte, 1)
	go func() {
		msg, err := c2.readText()
		if err != nil {
			return
		}
		got <- msg
	}()
	time.Sleep(50 * time.Millisecond)
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
	if n := s.count(t, `SELECT count(*) FROM erp.notifications WHERE event_id = $1 AND cleared_at IS NOT NULL`, ev.EventID); n != 1 {
		t.Fatalf("cleared rows: %d", n)
	}
}

type shadeApprovals struct {
	version int64
	decided bool
	fetched bool
}

func (f *shadeApprovals) Fetch(context.Context, string) (notifications.LiveDocument, error) {
	f.fetched = true
	return notifications.LiveDocument{ID: "req", State: "pending", StateVersion: f.version, FraudHints: []string{"new payee"}}, nil
}

func (f *shadeApprovals) Decide(context.Context, string, int64, string, string) error {
	f.decided = true
	return nil
}

// E10: approve from the shade fetches the live document and refuses when state_version changed.
func acceptE10(t *testing.T, s *stack) {
	appr := &shadeApprovals{version: 4}
	svc := s.notify(t, notifications.WithApprovals(appr))
	req, err := http.NewRequest(http.MethodPost, "/notifications/req/approve", strings.NewReader(`{"comment":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
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
	req2, err := http.NewRequest(http.MethodPost, "/notifications/req/approve", strings.NewReader(`{"comment":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("If-Match", "3")
	if _, err := svc.ApproveFromShade(req2.Context(), req2, "req", "approve", ""); err != nil {
		t.Fatal(err)
	}
	if !appr.decided {
		t.Fatal("matching version did not commit")
	}
}

// E11: alert dispatch failure never rolls back the business write.
func acceptE11(t *testing.T, s *stack) {
	company := uuid.New()
	actor := s.member("e11-act-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	recv := s.member("e11-rcv-"+uuid.NewString(), "secret", company, []string{"user"}, false)
	in := s.login(t, recv, "secret")
	s.registerToken(t, in, "tok-e11-"+uuid.NewString())
	push := &recordingPush{err: errors.New("provider down")}
	svc := s.notify(t, notifications.WithPush(push), notifications.WithMailer(&recordingMail{}), notifications.WithDirectory(userDir{users: []notifications.User{{ID: recv.ID, Email: "r@example.com"}}}))
	ev := sampleEvent(t, company, actor.ID, "HIGH", nil)
	doc := "biz-e11-" + uuid.NewString()
	if err := svc.CommitSubmission(t.Context(), principalOf(actor), "sales_invoice", doc, ev, nil, []string{recv.ID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), notifications.FanoutArgs{Event: ev, Explicit: []string{recv.ID}}, 1); err == nil {
		t.Fatal("expected dispatch failure")
	}
	if n := s.count(t, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, doc); n != 1 {
		t.Fatalf("business write survived: %d", n)
	}
}

// E12: blocking rules prevent submission; advisory rules notify only.
func acceptE12(t *testing.T, s *stack) {
	company := uuid.New()
	admin := s.member("e12-"+uuid.NewString(), "secret", company, []string{"admin"}, false)
	in := s.login(t, admin, "secret")
	hdr := s.authz(in.access)
	s.ok(t, http.MethodPost, "/api/v1/alert-rules", `{"document_type":"sales_invoice","condition":{"op":"amount_gt","value":"100.00"},"recipient_roles":["approver"],"channel":"push","severity":"HIGH","mode":"blocking","builtin":true}`, hdr, http.StatusOK)
	s.ok(t, http.MethodPost, "/api/v1/alert-rules", `{"document_type":"purchase_invoice","condition":{"op":"amount_gt","value":"1.00"},"recipient_roles":["approver"],"channel":"push","severity":"MEDIUM","mode":"advisory"}`, hdr, http.StatusOK)
	listed := s.ok(t, http.MethodGet, "/api/v1/alert-rules?document_type=sales_invoice", "", hdr, http.StatusOK)
	if !strings.Contains(string(listed), "blocking") {
		t.Fatalf("rules: %s", listed)
	}
	svc := s.notify(t)
	p := principalOf(admin, "admin")
	blocked := "blocked-" + uuid.NewString()
	if err := svc.Submit(t.Context(), p, "sales_invoice", blocked, "200.00", notifications.Event{}, nil, nil); !errors.Is(err, notifications.ErrBlockingAlert) {
		t.Fatalf("blocking: %v", err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, blocked); n != 0 {
		t.Fatalf("blocking submitted: %d", n)
	}
	advised := "advised-" + uuid.NewString()
	ev := sampleEvent(t, company, admin.ID, "LOW", []string{"open"})
	ev.Subject.DocType = "purchase_invoice"
	if err := svc.Submit(t.Context(), p, "purchase_invoice", advised, "50.00", ev, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, `SELECT count(*) FROM erp.notification_submissions WHERE document_id = $1`, advised); n != 1 {
		t.Fatalf("advisory submissions: %d", n)
	}
	if n := s.count(t, `SELECT count(*) FROM river_job WHERE kind = 'notifications.fanout' AND args #>> '{event,event_id}' = $1`, ev.EventID); n != 1 {
		t.Fatalf("advisory jobs: %d", n)
	}
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
	var logs strings.Builder
	cmd := exec.Command("go", "run", "./cmd/pushsink")
	cmd.Dir = filepath.Join(repoRoot(), "apps", "api")
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

type surface struct {
	c net.Conn
	r *bufio.Reader
}

func dialSurface(base, path, access string) (*surface, error) {
	u := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		return nil, err
	}
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nAuthorization: Bearer %s\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u, access, key)
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		rest, _ := io.ReadAll(io.LimitReader(br, 400))
		_ = conn.Close()
		return nil, fmt.Errorf("websocket status %s %s", strings.TrimSpace(status), rest)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &surface{c: conn, r: br}, nil
}

func (c *surface) Close() error { return c.c.Close() }

func (c *surface) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

func (c *surface) writeText(payload []byte) error {
	hdr := []byte{0x81, 0}
	n := len(payload)
	hdr[1] = byte(n) | 0x80
	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	if _, err := c.c.Write(hdr); err != nil {
		return err
	}
	if _, err := c.c.Write(maskKey[:]); err != nil {
		return err
	}
	buf := make([]byte, n)
	for i := range payload {
		buf[i] = payload[i] ^ maskKey[i%4]
	}
	_, err := c.c.Write(buf)
	return err
}

func (c *surface) readText() ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return nil, err
	}
	ln := int(h[1] & 0x7f)
	switch ln {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return nil, err
		}
		ln = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		return nil, errors.New("websocket frame too large")
	}
	buf := make([]byte, ln)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
