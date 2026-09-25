package identity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jaichahal/smart-erp/apps/api/internal/identity/zitadelproto"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

var ipN atomic.Int64

type clockBox struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockBox) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clockBox) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type memUser struct {
	id, password, totp string
}

type memSess struct {
	user           string
	password, totp bool
	web            bool
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
		return "", "", ErrUnknown
	}
	id := uuid.NewString()
	b.sess[id] = &memSess{user: login}
	return id, "token", nil
}

func (b *memBroker) Password(_ context.Context, sessionID, password string) (Factors, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, u, err := b.lookup(sessionID)
	if err != nil {
		return Factors{}, err
	}
	if u.password != password {
		return Factors{}, ErrFactor
	}
	s.password = true
	return b.factors(u, s), nil
}

func (b *memBroker) TOTP(_ context.Context, sessionID, code string) (Factors, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, u, err := b.lookup(sessionID)
	if err != nil {
		return Factors{}, err
	}
	if u.totp == "" || code != u.totp {
		return Factors{}, ErrFactor
	}
	s.totp = true
	return b.factors(u, s), nil
}

func (b *memBroker) WebAuthN(_ context.Context, sessionID string, assertion map[string]any) (Factors, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, u, err := b.lookup(sessionID)
	if err != nil {
		return Factors{}, err
	}
	if assertion["ok"] != true {
		return Factors{}, ErrFactor
	}
	s.web = true
	return b.factors(u, s), nil
}

func (b *memBroker) lookup(id string) (*memSess, memUser, error) {
	s := b.sess[id]
	if s == nil {
		return nil, memUser{}, ErrUnknown
	}
	return s, b.users[s.user], nil
}

func (b *memBroker) factors(u memUser, s *memSess) Factors {
	return Factors{UserID: u.id, LoginName: s.user, DisplayName: s.user, Password: s.password, TOTP: s.totp, WebAuthN: s.web}
}

type memDir struct {
	mu      sync.Mutex
	byLogin map[string]Account
	byID    map[string]Account
}

func (d *memDir) put(a Account) {
	d.mu.Lock()
	d.byLogin[a.LoginName] = a
	d.byID[a.ID] = a
	d.mu.Unlock()
}

func (d *memDir) ByLogin(_ context.Context, login string) (Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.byLogin[login]
	if !ok {
		return Account{}, ErrUnknown
	}
	return a, nil
}

func (d *memDir) ByID(_ context.Context, id string) (Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.byID[id]
	if !ok {
		return Account{}, ErrUnknown
	}
	return a, nil
}

type harness struct {
	db     *testdb.DB
	srv    *httptest.Server
	svc    *Service
	broker *memBroker
	dir    *memDir
	clock  *clockBox
	ip     string
}

func newHarness(t *testing.T, policy Policy) *harness {
	t.Helper()
	db := testdb.New(t)
	applyIdentity(t, db)
	n := ipN.Add(1)
	h := &harness{
		db: db, broker: newMemBroker(),
		dir:   &memDir{byLogin: map[string]Account{}, byID: map[string]Account{}},
		clock: &clockBox{t: time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)},
		ip:    fmt.Sprintf("10.%d.%d.%d", (n>>16)&255, (n>>8)&255, n&255),
	}
	riverClient, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = h.ip + ":40000"
			next.ServeHTTP(w, r)
		})
	}, apierr.RequestIDMiddleware)
	r.Route("/api/v1", func(api chi.Router) {
		h.svc = Mount(api, httpx.Deps{Pool: db.App, River: riverClient, Log: slog.New(slog.DiscardHandler)},
			WithBroker(h.broker), WithDirectory(h.dir), WithClock(h.clock.Now), WithPolicy(policy))
		api.With(h.svc.authenticate).Post("/actions/release", func(w http.ResponseWriter, r *http.Request) {
			if !h.svc.RequireStepUp(w, r) {
				return
			}
			httpx.JSON(w, r, http.StatusOK, map[string]bool{"released": true})
		})
	})
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

func defaultPolicy() Policy {
	p := DefaultPolicy()
	p.RatePerLogin = 1000
	p.RatePerIP = 1000
	return p
}

func applyIdentity(t *testing.T, db *testdb.DB) {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='erp' AND table_name='identity_users'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		raw, err := migrations.FS.ReadFile("00004_identity.sql")
		if err != nil {
			t.Fatal(err)
		}
		up := string(raw)
		if i := strings.Index(up, "-- +goose Down"); i >= 0 {
			up = up[:i]
		}
		for _, stmt := range strings.Split(up, ";") {
			var lines []string
			for _, line := range strings.Split(stmt, "\n") {
				trim := strings.TrimSpace(line)
				if trim == "" || strings.HasPrefix(trim, "--") {
					continue
				}
				lines = append(lines, line)
			}
			stmt = strings.TrimSpace(strings.Join(lines, "\n"))
			if stmt == "" {
				continue
			}
			if _, err := db.Migrator.Exec(ctx, stmt); err != nil {
				t.Fatalf("migration: %v\n%s", err, stmt)
			}
		}
	}
	if err := outbox.Migrate(ctx, db.Migrator); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) user(login, password string, roles []string, disabled bool) Account {
	a := Account{
		ID: uuid.NewString(), LoginName: login, Name: login, CompanyID: uuid.New(),
		Roles: roles, Personas: []string{"staff"}, Disabled: disabled,
		StepUpMethods: []string{"totp", "webauthn", "biometric"},
	}
	h.dir.put(a)
	h.broker.add(login, a.ID, password, "654321")
	return a
}

func deviceKey(t *testing.T) (jwk.Key, map[string]any) {
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
	return key, m
}

func (h *harness) do(t *testing.T, method, path string, body any, hdr map[string]string) *http.Response {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if body != nil || method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeData[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(readAll(t, resp), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func (h *harness) enroll(t *testing.T, pub map[string]any, platform string) string {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/api/v1/auth/device/enroll", map[string]any{
		"public_key": pub, "platform": platform, "app_version": "1.0.0", "device_name": "phone",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("enroll %d %s", resp.StatusCode, readAll(t, resp))
	}
	return decodeData[struct {
		DeviceID string `json:"device_id"`
	}](t, resp).DeviceID
}

func (h *harness) proof(t *testing.T, key jwk.Key, method, path, access string) string {
	t.Helper()
	raw, err := signDPoP(key, method, h.srv.URL+path, access, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *harness) start(t *testing.T, login string) string {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/api/v1/auth/session", map[string]string{"login_name": login}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session %d %s", resp.StatusCode, readAll(t, resp))
	}
	return decodeData[oapi.AuthSession](t, resp).SessionId
}

type issued struct {
	access, refresh, device string
	key                     jwk.Key
	user                    Account
}

func (h *harness) login(t *testing.T, a Account, password, totp string) issued {
	t.Helper()
	key, pub := deviceKey(t)
	dev := h.enroll(t, pub, string(oapi.Android))
	sid := h.start(t, a.LoginName)
	body := map[string]any{}
	if password != "" {
		body["password"] = password
	}
	if totp != "" {
		body["totp"] = totp
	}
	resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check %d %s", resp.StatusCode, readAll(t, resp))
	}
	path := "/api/v1/auth/token"
	resp = h.do(t, http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(t, key, http.MethodPost, path, ""),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token %d %s", resp.StatusCode, readAll(t, resp))
	}
	tok := decodeData[oapi.TokenResponse](t, resp)
	return issued{access: tok.AccessToken, refresh: tok.RefreshToken, device: dev, key: key, user: a}
}

func (h *harness) authed(t *testing.T, method, path, access string, key jwk.Key, body any) *http.Response {
	t.Helper()
	return h.do(t, method, path, body, map[string]string{
		"Authorization": "Bearer " + access,
		"DPoP":          h.proof(t, key, method, path, access),
	})
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Details   map[string]any `json:"details"`
			RequestID string         `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code == "" || env.Error.Message == "" || env.Error.Details == nil || env.Error.RequestID == "" {
		t.Fatalf("envelope incomplete: %s", body)
	}
	return env.Error.Code
}

// A1: two failed logins with different causes return byte-identical bodies and status.
func TestA1_FailedLoginsAreByteIdentical(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	rid := "req-a1-identical"
	wrong := h.user("a1-wrong", "secret", []string{"Sales Agent"}, false)
	disabled := h.user("a1-disabled", "secret", []string{"Sales Agent"}, true)
	mfa := h.user("a1-mfa", "secret", []string{"Accountant"}, false)

	failCheck := func(login, password string) (int, []byte) {
		t.Helper()
		sid := h.start(t, login)
		resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": password}, map[string]string{"X-Request-ID": rid})
		return resp.StatusCode, readAll(t, resp)
	}
	s1, b1 := failCheck(wrong.LoginName, "nope")
	s2, b2 := failCheck(disabled.LoginName, "secret")
	s3, b3 := failCheck("a1-nobody", "secret")

	sid := h.start(t, mfa.LoginName)
	ok := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("mfa password step %d %s", ok.StatusCode, readAll(t, ok))
	}
	key, pub := deviceKey(t)
	dev := h.enroll(t, pub, string(oapi.Android))
	path := "/api/v1/auth/token"
	resp := h.do(t, http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(t, key, http.MethodPost, path, ""), "X-Request-ID": rid,
	})
	s4, b4 := resp.StatusCode, readAll(t, resp)

	for i, got := range []struct {
		status int
		body   []byte
	}{{s2, b2}, {s3, b3}, {s4, b4}} {
		if got.status != s1 || !bytes.Equal(got.body, b1) {
			t.Fatalf("cause %d status %d/%d body\n%s\n%s", i+2, s1, got.status, b1, got.body)
		}
	}
	if errorCode(t, b1) != string(apierr.AuthRequired) {
		t.Fatalf("code %s", b1)
	}
}

// A2: a failed login's audit row survives the rolled-back request transaction.
func TestA2_FailedLoginAuditSurvivesRollback(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("a2-user", "secret", []string{"Sales Agent"}, false)
	sid := h.start(t, user.LoginName)
	resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "nope"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d %s", resp.StatusCode, readAll(t, resp))
	}
	var attempts int
	if err := h.db.App.QueryRow(context.Background(), `SELECT count(*) FROM erp.identity_auth_attempts WHERE login_name=$1`, user.LoginName).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("rolled-back attempt rows still present: %d", attempts)
	}
	var reason string
	if err := h.db.App.QueryRow(context.Background(), `SELECT reason FROM erp.audit_events WHERE event_type='auth.login_failed' AND reference_id=$1`, user.LoginName).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "wrong_password" {
		t.Fatalf("audit reason %q", reason)
	}
}

// A3: a proof from a key other than the one enrolled for the refresh token is DEVICE_MISMATCH.
func TestA3_WrongDeviceKeyIsMismatch(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("a3-user", "secret", []string{"Sales Agent"}, false)
	got := h.login(t, user, "secret", "")
	other, _ := deviceKey(t)
	path := "/api/v1/auth/refresh"
	resp := h.do(t, http.MethodPost, path, map[string]string{"refresh_token": got.refresh}, map[string]string{
		"DPoP": h.proof(t, other, http.MethodPost, path, ""),
	})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || errorCode(t, body) != string(apierr.DeviceMismatch) {
		t.Fatalf("mismatch %d %s", resp.StatusCode, body)
	}
	resp = h.do(t, http.MethodPost, path, map[string]string{"refresh_token": got.refresh}, map[string]string{
		"DPoP": h.proof(t, got.key, http.MethodPost, path, ""),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("original device should still refresh, %d %s", resp.StatusCode, readAll(t, resp))
	}
}

// A4: reusing a refresh token revokes the device and every token issued to it.
func TestA4_RefreshReuseRevokesDevice(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("a4-user", "secret", []string{"Sales Agent"}, false)
	first := h.login(t, user, "secret", "")
	path := "/api/v1/auth/refresh"
	resp := h.do(t, http.MethodPost, path, map[string]string{"refresh_token": first.refresh}, map[string]string{
		"DPoP": h.proof(t, first.key, http.MethodPost, path, ""),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate %d %s", resp.StatusCode, readAll(t, resp))
	}
	rotated := decodeData[oapi.TokenResponse](t, resp)
	resp = h.do(t, http.MethodPost, path, map[string]string{"refresh_token": first.refresh}, map[string]string{
		"DPoP": h.proof(t, first.key, http.MethodPost, path, ""),
	})
	body := readAll(t, resp)
	if errorCode(t, body) != string(apierr.TokenRevoked) {
		t.Fatalf("reuse %d %s", resp.StatusCode, body)
	}
	me := "/api/v1/me"
	resp = h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + rotated.AccessToken,
		"DPoP":          h.proof(t, first.key, http.MethodGet, me, rotated.AccessToken),
	})
	body = readAll(t, resp)
	if errorCode(t, body) != string(apierr.TokenRevoked) {
		t.Fatalf("access after reuse %d %s", resp.StatusCode, body)
	}
}

// A5: expired is TOKEN_EXPIRED, revoked is TOKEN_REVOKED, and revocation wins when both apply.
func TestA5_ExpiryAndRevocation(t *testing.T) {
	p := defaultPolicy()
	p.AccessTTL = time.Minute
	h := newHarness(t, p)
	expiredUser := h.user("a5-exp", "secret", []string{"Sales Agent"}, false)
	revokedUser := h.user("a5-rev", "secret", []string{"Sales Agent"}, false)
	bothUser := h.user("a5-both", "secret", []string{"Sales Agent"}, false)

	exp := h.login(t, expiredUser, "secret", "")
	h.clock.Advance(2 * time.Minute)
	me := "/api/v1/me"
	resp := h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + exp.access,
		"DPoP":          h.proof(t, exp.key, http.MethodGet, me, exp.access),
	})
	if errorCode(t, readAll(t, resp)) != string(apierr.TokenExpired) {
		t.Fatalf("expired: %d", resp.StatusCode)
	}

	rev := h.login(t, revokedUser, "secret", "")
	out := h.authed(t, http.MethodPost, "/api/v1/auth/logout", rev.access, rev.key, map[string]any{})
	if out.StatusCode != http.StatusOK {
		t.Fatalf("logout %d %s", out.StatusCode, readAll(t, out))
	}
	resp = h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + rev.access,
		"DPoP":          h.proof(t, rev.key, http.MethodGet, me, rev.access),
	})
	if errorCode(t, readAll(t, resp)) != string(apierr.TokenRevoked) {
		t.Fatalf("revoked")
	}

	both := h.login(t, bothUser, "secret", "")
	out = h.authed(t, http.MethodPost, "/api/v1/auth/logout", both.access, both.key, map[string]any{})
	if out.StatusCode != http.StatusOK {
		t.Fatalf("logout both %d %s", out.StatusCode, readAll(t, out))
	}
	h.clock.Advance(2 * time.Minute)
	resp = h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + both.access,
		"DPoP":          h.proof(t, both.key, http.MethodGet, me, both.access),
	})
	body := readAll(t, resp)
	if errorCode(t, body) != string(apierr.TokenRevoked) {
		t.Fatalf("revocation must win: %s", body)
	}
}

// A6: a gated action without a valid step-up token returns STEP_UP_REQUIRED; the token is single-use and lasts two minutes.
func TestA6_StepUpSingleUseTwoMinutes(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("a6-user", "secret", []string{"Accountant"}, false)
	got := h.login(t, user, "secret", "654321")
	path := "/api/v1/actions/release"
	resp := h.authed(t, http.MethodPost, path, got.access, got.key, map[string]any{})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusForbidden || errorCode(t, body) != string(apierr.StepUpRequired) {
		t.Fatalf("missing step-up %d %s", resp.StatusCode, body)
	}
	up := h.authed(t, http.MethodPost, "/api/v1/auth/step-up", got.access, got.key, map[string]string{"method": "totp", "code": "654321"})
	if up.StatusCode != http.StatusOK {
		t.Fatalf("step-up %d %s", up.StatusCode, readAll(t, up))
	}
	token := decodeData[struct {
		StepUpToken string `json:"step_up_token"`
		ExpiresIn   int    `json:"expires_in"`
	}](t, up)
	if token.ExpiresIn != 120 {
		t.Fatalf("expires_in %d", token.ExpiresIn)
	}
	call := func(raw string) (int, []byte) {
		t.Helper()
		resp := h.do(t, http.MethodPost, path, map[string]any{}, map[string]string{
			"Authorization": "Bearer " + got.access,
			"DPoP":          h.proof(t, got.key, http.MethodPost, path, got.access),
			"Step-Up-Token": raw,
		})
		return resp.StatusCode, readAll(t, resp)
	}
	if status, body := call(token.StepUpToken); status != http.StatusOK {
		t.Fatalf("first use %d %s", status, body)
	}
	if status, body := call(token.StepUpToken); status != http.StatusForbidden || errorCode(t, body) != string(apierr.StepUpRequired) {
		t.Fatalf("second use %d %s", status, body)
	}
	up = h.authed(t, http.MethodPost, "/api/v1/auth/step-up", got.access, got.key, map[string]string{"method": "totp", "code": "654321"})
	token = decodeData[struct {
		StepUpToken string `json:"step_up_token"`
		ExpiresIn   int    `json:"expires_in"`
	}](t, up)
	h.clock.Advance(2*time.Minute + time.Second)
	if status, body := call(token.StepUpToken); status != http.StatusForbidden || errorCode(t, body) != string(apierr.StepUpRequired) {
		t.Fatalf("expired step-up %d %s", status, body)
	}
}

// A7: rate limiting returns the standard envelope with RATE_LIMITED.
func TestA7_RateLimitEnvelope(t *testing.T) {
	p := defaultPolicy()
	p.RatePerLogin = 2
	h := newHarness(t, p)
	var last *http.Response
	for i := 0; i < 3; i++ {
		last = h.do(t, http.MethodPost, "/api/v1/auth/session", map[string]string{"login_name": "a7-user"}, nil)
	}
	body := readAll(t, last)
	if last.StatusCode != http.StatusTooManyRequests || errorCode(t, body) != string(apierr.RateLimited) {
		t.Fatalf("rate %d %s", last.StatusCode, body)
	}
	if last.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

// A8: lockout blocks the correct password until unlock.
func TestA8_LockoutUntilUnlock(t *testing.T) {
	p := defaultPolicy()
	p.FailuresBeforeLock = 2
	h := newHarness(t, p)
	user := h.user("a8-user", "secret", []string{"Sales Agent"}, false)
	sid := h.start(t, user.LoginName)
	for range 2 {
		resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "nope"}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d %s", resp.StatusCode, readAll(t, resp))
		}
	}
	resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || errorCode(t, body) != string(apierr.AuthRequired) {
		t.Fatalf("locked correct password %d %s", resp.StatusCode, body)
	}
	if err := h.svc.Unlock(context.Background(), user.LoginName); err != nil {
		t.Fatal(err)
	}
	sid = h.start(t, user.LoginName)
	resp = h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after unlock %d %s", resp.StatusCode, readAll(t, resp))
	}
}

// A9: ending a session from another device stops the first device's next request.
func TestA9_EndSessionFromOtherDevice(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("a9-user", "secret", []string{"Sales Agent"}, false)
	a := h.login(t, user, "secret", "")
	b := h.login(t, user, "secret", "")
	list := h.authed(t, http.MethodGet, "/api/v1/me/sessions", b.access, b.key, nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list %d %s", list.StatusCode, readAll(t, list))
	}
	sessions := decodeData[[]oapi.UserSession](t, list)
	var target string
	for _, s := range sessions {
		if s.DeviceId == a.device {
			target = s.Id
		}
	}
	if target == "" {
		t.Fatalf("session for device A missing: %+v", sessions)
	}
	del := h.authed(t, http.MethodDelete, "/api/v1/me/sessions/"+target, b.access, b.key, nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete %d %s", del.StatusCode, readAll(t, del))
	}
	me := "/api/v1/me"
	resp := h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + a.access,
		"DPoP":          h.proof(t, a.key, http.MethodGet, me, a.access),
	})
	body := readAll(t, resp)
	if errorCode(t, body) != string(apierr.TokenRevoked) {
		t.Fatalf("device A after end %d %s", resp.StatusCode, body)
	}
	resp = h.authed(t, http.MethodGet, me, b.access, b.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device B should remain %d %s", resp.StatusCode, readAll(t, resp))
	}
}

// A10: MFA-mandatory roles cannot finish login without a second factor.
func TestA10_MFAMandatoryRoles(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	acct := h.user("a10-acct", "secret", []string{"Accountant"}, false)
	key, pub := deviceKey(t)
	dev := h.enroll(t, pub, string(oapi.Android))
	sid := h.start(t, acct.LoginName)
	resp := h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password %d %s", resp.StatusCode, readAll(t, resp))
	}
	path := "/api/v1/auth/token"
	resp = h.do(t, http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(t, key, http.MethodPost, path, ""),
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatal("accountant completed login without a second factor")
	}
	resp = h.do(t, http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"totp": "654321"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp %d %s", resp.StatusCode, readAll(t, resp))
	}
	resp = h.do(t, http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(t, key, http.MethodPost, path, ""),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with totp %d %s", resp.StatusCode, readAll(t, resp))
	}
	agent := h.user("a10-agent", "secret", []string{"Sales Agent"}, false)
	got := h.login(t, agent, "secret", "")
	if got.access == "" {
		t.Fatal("sales agent should complete login with a password")
	}
}

// A22: the session cap ends the oldest session when a new one would exceed it.
func TestA22_SessionCapEndsOldest(t *testing.T) {
	p := defaultPolicy()
	p.MaxSessions = 2
	h := newHarness(t, p)
	user := h.user("a22-user", "secret", []string{"Sales Agent"}, false)
	first := h.login(t, user, "secret", "")
	second := h.login(t, user, "secret", "")
	third := h.login(t, user, "secret", "")
	me := "/api/v1/me"
	resp := h.do(t, http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + first.access,
		"DPoP":          h.proof(t, first.key, http.MethodGet, me, first.access),
	})
	if errorCode(t, readAll(t, resp)) != string(apierr.TokenRevoked) {
		t.Fatal("oldest session should have been ended")
	}
	list := h.authed(t, http.MethodGet, "/api/v1/me/sessions", third.access, third.key, nil)
	sessions := decodeData[[]oapi.UserSession](t, list)
	if len(sessions) != 2 {
		t.Fatalf("active sessions %d", len(sessions))
	}
	if second.access == "" {
		t.Fatal("second login missing")
	}
}

func TestJWKSVerifiesAccessToken(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	user := h.user("jwks-user", "secret", []string{"Sales Agent"}, false)
	got := h.login(t, user, "secret", "")
	resp := h.do(t, http.MethodGet, "/api/v1/auth/jwks", nil, nil)
	raw := readAll(t, resp)
	set, err := jwk.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Parse([]byte(got.access), jwt.WithKeySet(set), jwt.WithValidate(true), jwt.WithIssuer("smart-erp"), jwt.WithAudience("erp"), jwt.WithClock(jwt.ClockFunc(h.clock.Now))); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestDeviceRegisteredOutbox(t *testing.T) {
	h := newHarness(t, defaultPolicy())
	_, pub := deviceKey(t)
	h.enroll(t, pub, string(oapi.Ios))
	var n int
	if err := h.db.App.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind='device.registered'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("jobs %d", n)
	}
}

func TestZitadelSessionBroker(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	zitadelproto.RegisterSessionServiceServer(gs, &fakeSessions{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	broker := &ZitadelBroker{client: zitadelproto.NewSessionServiceClient(conn)}
	id, _, err := broker.Create(context.Background(), "ada")
	if err != nil || id == "" {
		t.Fatal(err)
	}
	factors, err := broker.Password(context.Background(), id, "secret")
	if err != nil || !factors.Password || factors.UserID != "user-ada" {
		t.Fatalf("%+v %v", factors, err)
	}
	factors, err = broker.TOTP(context.Background(), id, "654321")
	if err != nil || !factors.TOTP {
		t.Fatalf("totp %+v %v", factors, err)
	}
	_, err = broker.Password(context.Background(), id, "nope")
	if !errors.Is(err, ErrFactor) {
		t.Fatalf("bad password: %v", err)
	}
}

type fakeSessions struct {
	zitadelproto.UnimplementedSessionServiceServer
	mu   sync.Mutex
	sess map[string]*fakeSess
}

type fakeSess struct {
	login, id string
	password  bool
	totp      bool
}

func (f *fakeSessions) CreateSession(_ context.Context, req *zitadelproto.CreateSessionRequest) (*zitadelproto.CreateSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sess == nil {
		f.sess = map[string]*fakeSess{}
	}
	id := uuid.NewString()
	login := req.GetChecks().GetUser().GetLoginName()
	f.sess[id] = &fakeSess{login: login, id: "user-" + login}
	return &zitadelproto.CreateSessionResponse{SessionId: id, SessionToken: "tok"}, nil
}

func (f *fakeSessions) SetSession(_ context.Context, req *zitadelproto.SetSessionRequest) (*zitadelproto.SetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sess[req.GetSessionId()]
	if s == nil {
		return nil, ErrUnknown
	}
	if pw := req.GetChecks().GetPassword().GetPassword(); pw != "" {
		if pw != "secret" {
			return nil, status.Error(codes.InvalidArgument, "password invalid")
		}
		s.password = true
	}
	if code := req.GetChecks().GetTotp().GetCode(); code != "" {
		if code != "654321" {
			return nil, ErrFactor
		}
		s.totp = true
	}
	return &zitadelproto.SetSessionResponse{}, nil
}

func (f *fakeSessions) GetSession(_ context.Context, req *zitadelproto.GetSessionRequest) (*zitadelproto.GetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sess[req.GetSessionId()]
	if s == nil {
		return nil, ErrUnknown
	}
	factors := &zitadelproto.Factors{User: &zitadelproto.UserFactor{Id: s.id, LoginName: s.login, DisplayName: s.login, VerifiedAt: timestamppb.Now()}}
	if s.password {
		factors.Password = &zitadelproto.PasswordFactor{VerifiedAt: timestamppb.Now()}
	}
	if s.totp {
		factors.Totp = &zitadelproto.TOTPFactor{VerifiedAt: timestamppb.Now()}
	}
	return &zitadelproto.GetSessionResponse{Session: &zitadelproto.Session{Id: req.GetSessionId(), Factors: factors}}, nil
}
