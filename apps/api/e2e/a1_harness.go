package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"

	"github.com/jaichahal/smart-erp/apps/api/internal/app"
	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// a1IP keeps each harness off the shared rate-limit key of the catalog stack.
var a1IP atomic.Int64

type a1Clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *a1Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *a1Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// a1API is the composed API on the catalog database, with a controllable
// clock and policy so A5, A6, A7, and A8 can be asserted for real.
type a1API struct {
	t      *testing.T
	db     *testdb.DB
	srv    *httptest.Server
	broker *memBroker
	dir    *memDir
	clock  *a1Clock
}

func openA1(t *testing.T, db *testdb.DB, policy identity.Policy, gate bool) *a1API {
	t.Helper()
	n := a1IP.Add(1)
	ip := "10.8." + formatInt64((n>>8)&255) + "." + formatInt64(n&255)
	clock := &a1Clock{t: time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)}
	broker := newMemBroker()
	dir := &memDir{byLogin: map[string]identity.Account{}, byID: map[string]identity.Account{}}
	river, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	opts := []identity.Option{
		identity.WithBroker(broker),
		identity.WithDirectory(dir),
		identity.WithPolicy(policy),
		identity.WithClock(clock.Now),
	}
	if gate {
		opts = append(opts, identity.WithStepUpAction())
	}
	handler, err := app.Handler(httpx.Deps{
		Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), StartedAt: clock.Now(),
	}, app.WithIdentity(opts...))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = ip + ":40000"
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &a1API{t: t, db: db, srv: srv, broker: broker, dir: dir, clock: clock}
}

func a1Policy() identity.Policy {
	p := identity.DefaultPolicy()
	p.RatePerLogin = 1000
	p.RatePerIP = 1000
	return p
}

func (h *a1API) user(login, password string, roles []string, disabled bool) identity.Account {
	a := identity.Account{
		ID: uuid.NewString(), LoginName: login, Name: login, CompanyID: uuid.New(),
		Roles: roles, Personas: []string{"staff"}, Disabled: disabled,
		StepUpMethods: []string{"totp"},
	}
	h.dir.put(a)
	h.broker.add(login, a.ID, password, "654321")
	return a
}

func (h *a1API) do(method, path string, body any, hdr map[string]string) (int, []byte, http.Header) {
	h.t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, buf)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
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
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, raw, resp.Header
}

func (h *a1API) proof(key jwk.Key, method, path, access string) string {
	h.t.Helper()
	raw, err := a1SignDPoP(key, method, h.srv.URL+path, access, h.clock.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func (h *a1API) start(login string) string {
	h.t.Helper()
	status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/session", map[string]string{"login_name": login}, nil)
	if status != http.StatusOK {
		h.t.Fatalf("session %d %s", status, raw)
	}
	return a1Data[oapi.AuthSession](h.t, raw).SessionId
}

type a1Issued struct {
	access, refresh, device string
	key                     jwk.Key
}

func (h *a1API) login(a identity.Account, password, totp string) a1Issued {
	h.t.Helper()
	key, pub := a1DeviceKey(h.t)
	status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/device/enroll", map[string]any{
		"public_key": pub, "platform": string(oapi.Android), "app_version": "1.0.0", "device_name": "phone",
	}, nil)
	if status != http.StatusCreated {
		h.t.Fatalf("enroll %d %s", status, raw)
	}
	dev := a1Data[struct {
		DeviceID string `json:"device_id"`
	}](h.t, raw).DeviceID
	sid := h.start(a.LoginName)
	body := map[string]any{}
	if password != "" {
		body["password"] = password
	}
	if totp != "" {
		body["totp"] = totp
	}
	status, raw, _ = h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", body, nil)
	if status != http.StatusOK {
		h.t.Fatalf("check %d %s", status, raw)
	}
	path := "/api/v1/auth/token"
	status, raw, _ = h.do(http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(key, http.MethodPost, path, ""),
	})
	if status != http.StatusOK {
		h.t.Fatalf("token %d %s", status, raw)
	}
	tok := a1Data[oapi.TokenResponse](h.t, raw)
	return a1Issued{access: tok.AccessToken, refresh: tok.RefreshToken, device: dev, key: key}
}

func (h *a1API) authed(method, path, access string, key jwk.Key, body any) (int, []byte, http.Header) {
	h.t.Helper()
	return h.do(method, path, body, map[string]string{
		"Authorization": "Bearer " + access,
		"DPoP":          h.proof(key, method, path, access),
	})
}

func a1Data[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return env.Data
}

func a1Want(t *testing.T, status int, raw []byte, want int, code string) {
	t.Helper()
	got := errorCode(raw)
	if status != want || got != code {
		t.Fatalf("status %d code %q, want %d %s body %s", status, got, want, code, trim(raw))
	}
}

func a1DeviceKey(t *testing.T) (jwk.Key, map[string]any) {
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

func a1SignDPoP(key jwk.Key, method, htu, access string, now time.Time) (string, error) {
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		pub = key
	}
	claims := map[string]any{"htm": method, "htu": htu, "iat": now.Unix(), "jti": uuid.NewString()}
	if access != "" {
		sum := sha256.Sum256([]byte(access))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.AlgorithmKey, jwa.ES256); err != nil {
		return "", err
	}
	if err := hdr.Set(jws.TypeKey, "dpop+jwt"); err != nil {
		return "", err
	}
	if err := hdr.Set(jws.JWKKey, pub); err != nil {
		return "", err
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.ES256, key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
