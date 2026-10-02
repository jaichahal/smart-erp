package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/app"
	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// stack is the real API process in-process, on a database cloned from erp_template.
// Zitadel is reached through the identity Broker port (the same port the gRPC client implements).
type stack struct {
	db     *testdb.DB
	srv    *httptest.Server
	broker *memBroker
	dir    *memDir
}

func newStack(t *testing.T) *stack {
	t.Helper()
	db := testdb.New(t)
	broker := newMemBroker()
	dir := &memDir{byLogin: map[string]identity.Account{}, byID: map[string]identity.Account{}}
	river, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	policy := identity.DefaultPolicy()
	policy.RatePerLogin = 1000
	policy.RatePerIP = 1000
	handler, err := app.Handler(httpx.Deps{
		Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), StartedAt: time.Now(),
	}, app.WithIdentity(
		identity.WithBroker(broker),
		identity.WithDirectory(dir),
		identity.WithPolicy(policy),
		identity.WithClock(func() time.Time { return time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC) }),
	))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &stack{db: db, srv: srv, broker: broker, dir: dir}
}

func (s *stack) user(login, password string, roles []string, disabled bool) identity.Account {
	a := identity.Account{
		ID: uuid.NewString(), LoginName: login, Name: login, CompanyID: uuid.New(),
		Roles: roles, Personas: []string{"staff"}, Disabled: disabled,
		StepUpMethods: []string{"totp"},
	}
	s.dir.put(a)
	s.broker.add(login, a.ID, password, "654321")
	return a
}

func (s *stack) call(t *testing.T, method, path, body string, hdr map[string]string) (int, string, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, s.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && hdr["Idempotency-Key"] == "" {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, errorCode(raw), raw
}

func errorCode(raw []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.Error.Code
}

type memUser struct{ id, password, totp string }
type memSess struct {
	user           string
	password, totp bool
}

type memBroker struct {
	mu    sync.Mutex
	users map[string]memUser
	sess  map[string]*memSess
}

func newMemBroker() *memBroker {
	return &memBroker{users: map[string]memUser{}, sess: map[string]*memSess{}}
}

func (b *memBroker) add(login, id, password, totp string) {
	b.mu.Lock()
	b.users[login] = memUser{id: id, password: password, totp: totp}
	b.mu.Unlock()
}

func (b *memBroker) Create(_ context.Context, login string) (string, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.users[login]; !ok {
		return "", "", identity.ErrUnknown
	}
	id := uuid.NewString()
	b.sess[id] = &memSess{user: login}
	return id, "token", nil
}

func (b *memBroker) Password(_ context.Context, sessionID, password string) (identity.Factors, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, u, err := b.lookup(sessionID)
	if err != nil {
		return identity.Factors{}, err
	}
	if u.password != password {
		return identity.Factors{}, identity.ErrFactor
	}
	s.password = true
	return identity.Factors{UserID: u.id, LoginName: s.user, DisplayName: s.user, Password: true, TOTP: s.totp}, nil
}

func (b *memBroker) TOTP(_ context.Context, sessionID, code string) (identity.Factors, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, u, err := b.lookup(sessionID)
	if err != nil {
		return identity.Factors{}, err
	}
	if u.totp == "" || code != u.totp {
		return identity.Factors{}, identity.ErrFactor
	}
	s.totp = true
	return identity.Factors{UserID: u.id, LoginName: s.user, DisplayName: s.user, Password: s.password, TOTP: true}, nil
}

func (b *memBroker) WebAuthN(context.Context, string, map[string]any) (identity.Factors, error) {
	return identity.Factors{}, identity.ErrFactor
}

func (b *memBroker) lookup(id string) (*memSess, memUser, error) {
	s := b.sess[id]
	if s == nil {
		return nil, memUser{}, identity.ErrUnknown
	}
	return s, b.users[s.user], nil
}

type memDir struct {
	mu      sync.Mutex
	byLogin map[string]identity.Account
	byID    map[string]identity.Account
}

func (d *memDir) put(a identity.Account) {
	d.mu.Lock()
	d.byLogin[a.LoginName] = a
	d.byID[a.ID] = a
	d.mu.Unlock()
}

func (d *memDir) ByLogin(_ context.Context, login string) (identity.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.byLogin[login]
	if !ok {
		return identity.Account{}, identity.ErrUnknown
	}
	return a, nil
}

func (d *memDir) ByID(_ context.Context, id string) (identity.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.byID[id]
	if !ok {
		return identity.Account{}, identity.ErrUnknown
	}
	return a, nil
}
