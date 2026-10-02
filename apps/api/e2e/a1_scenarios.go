package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

func init() {
	acceptanceScenarios["A2"] = scenarioA2
	acceptanceScenarios["A3"] = scenarioA3
	acceptanceScenarios["A4"] = scenarioA4
	acceptanceScenarios["A5"] = scenarioA5
	acceptanceScenarios["A6"] = scenarioA6
	acceptanceScenarios["A7"] = scenarioA7
	acceptanceScenarios["A8"] = scenarioA8
	acceptanceScenarios["A9"] = scenarioA9
	acceptanceScenarios["A10"] = scenarioA10
}

// The catalog probe posts one empty body to /auth/session for every A-id, so it
// cannot tell DEVICE_MISMATCH from TOKEN_EXPIRED. These flows hit the composed
// API and assert the code named in docs/spec/08-acceptance-tests.md.

func scenarioA2(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), false)
	user := h.user("a2-user", "secret", []string{"Sales Agent"}, false)
	sid := h.start(user.LoginName)
	status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "nope"}, nil)
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.AuthRequired))
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

func scenarioA3(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), false)
	user := h.user("a3-user", "secret", []string{"Sales Agent"}, false)
	got := h.login(user, "secret", "")
	other, _ := a1DeviceKey(t)
	path := "/api/v1/auth/refresh"
	status, raw, _ := h.do(http.MethodPost, path, map[string]string{"refresh_token": got.refresh}, map[string]string{
		"DPoP": h.proof(other, http.MethodPost, path, ""),
	})
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.DeviceMismatch))
	status, raw, _ = h.do(http.MethodPost, path, map[string]string{"refresh_token": got.refresh}, map[string]string{
		"DPoP": h.proof(got.key, http.MethodPost, path, ""),
	})
	if status != http.StatusOK {
		t.Fatalf("original device should still refresh, %d %s", status, raw)
	}
}

func scenarioA4(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), false)
	user := h.user("a4-user", "secret", []string{"Sales Agent"}, false)
	first := h.login(user, "secret", "")
	path := "/api/v1/auth/refresh"
	status, raw, _ := h.do(http.MethodPost, path, map[string]string{"refresh_token": first.refresh}, map[string]string{
		"DPoP": h.proof(first.key, http.MethodPost, path, ""),
	})
	if status != http.StatusOK {
		t.Fatalf("rotate %d %s", status, raw)
	}
	rotated := a1Data[oapi.TokenResponse](t, raw)
	status, raw, _ = h.do(http.MethodPost, path, map[string]string{"refresh_token": first.refresh}, map[string]string{
		"DPoP": h.proof(first.key, http.MethodPost, path, ""),
	})
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenRevoked))
	me := "/api/v1/me"
	status, raw, _ = h.do(http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + rotated.AccessToken,
		"DPoP":          h.proof(first.key, http.MethodGet, me, rotated.AccessToken),
	})
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenRevoked))
}

func scenarioA5(t *testing.T, s *stack) {
	p := a1Policy()
	p.AccessTTL = time.Minute
	h := openA1(t, s.db, p, false)
	expiredUser := h.user("a5-exp", "secret", []string{"Sales Agent"}, false)
	revokedUser := h.user("a5-rev", "secret", []string{"Sales Agent"}, false)
	bothUser := h.user("a5-both", "secret", []string{"Sales Agent"}, false)

	exp := h.login(expiredUser, "secret", "")
	h.clock.Advance(2 * time.Minute)
	me := "/api/v1/me"
	status, raw, _ := h.authed(http.MethodGet, me, exp.access, exp.key, nil)
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenExpired))

	rev := h.login(revokedUser, "secret", "")
	status, raw, _ = h.authed(http.MethodPost, "/api/v1/auth/logout", rev.access, rev.key, map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("logout %d %s", status, raw)
	}
	status, raw, _ = h.authed(http.MethodGet, me, rev.access, rev.key, nil)
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenRevoked))

	both := h.login(bothUser, "secret", "")
	status, raw, _ = h.authed(http.MethodPost, "/api/v1/auth/logout", both.access, both.key, map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("logout both %d %s", status, raw)
	}
	h.clock.Advance(2 * time.Minute)
	status, raw, _ = h.authed(http.MethodGet, me, both.access, both.key, nil)
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenRevoked))
}

func scenarioA6(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), true)
	user := h.user("a6-user", "secret", []string{"Accountant"}, false)
	got := h.login(user, "secret", "654321")
	path := "/api/v1/actions/release"
	status, raw, _ := h.authed(http.MethodPost, path, got.access, got.key, map[string]any{})
	a1Want(t, status, raw, http.StatusForbidden, string(apierr.StepUpRequired))
	status, raw, _ = h.authed(http.MethodPost, "/api/v1/auth/step-up", got.access, got.key, map[string]string{"method": "totp", "code": "654321"})
	if status != http.StatusOK {
		t.Fatalf("step-up %d %s", status, raw)
	}
	token := a1Data[struct {
		StepUpToken string `json:"step_up_token"`
		ExpiresIn   int    `json:"expires_in"`
	}](t, raw)
	if token.ExpiresIn != 120 {
		t.Fatalf("expires_in %d", token.ExpiresIn)
	}
	call := func(rawToken string) (int, []byte) {
		t.Helper()
		status, body, _ := h.do(http.MethodPost, path, map[string]any{}, map[string]string{
			"Authorization": "Bearer " + got.access,
			"DPoP":          h.proof(got.key, http.MethodPost, path, got.access),
			"Step-Up-Token": rawToken,
		})
		return status, body
	}
	if status, body := call(token.StepUpToken); status != http.StatusOK {
		t.Fatalf("first use %d %s", status, body)
	}
	if status, body := call(token.StepUpToken); status != http.StatusForbidden || errorCode(body) != string(apierr.StepUpRequired) {
		t.Fatalf("second use %d %s", status, body)
	}
	status, raw, _ = h.authed(http.MethodPost, "/api/v1/auth/step-up", got.access, got.key, map[string]string{"method": "totp", "code": "654321"})
	if status != http.StatusOK {
		t.Fatalf("second step-up %d %s", status, raw)
	}
	token = a1Data[struct {
		StepUpToken string `json:"step_up_token"`
		ExpiresIn   int    `json:"expires_in"`
	}](t, raw)
	h.clock.Advance(2*time.Minute + time.Second)
	if status, body := call(token.StepUpToken); status != http.StatusForbidden || errorCode(body) != string(apierr.StepUpRequired) {
		t.Fatalf("expired step-up %d %s", status, body)
	}
}

func scenarioA7(t *testing.T, s *stack) {
	p := a1Policy()
	p.RatePerLogin = 2
	h := openA1(t, s.db, p, false)
	var status int
	var raw []byte
	var hdr http.Header
	for i := 0; i < 3; i++ {
		status, raw, hdr = h.do(http.MethodPost, "/api/v1/auth/session", map[string]string{"login_name": "a7-user"}, nil)
	}
	a1Want(t, status, raw, http.StatusTooManyRequests, string(apierr.RateLimited))
	if hdr.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func scenarioA8(t *testing.T, s *stack) {
	p := a1Policy()
	p.FailuresBeforeLock = 2
	h := openA1(t, s.db, p, false)
	user := h.user("a8-user", "secret", []string{"Sales Agent"}, false)
	sid := h.start(user.LoginName)
	for range 2 {
		status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "nope"}, nil)
		a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.AuthRequired))
	}
	status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.AuthRequired))
	if _, err := h.db.App.Exec(context.Background(), `UPDATE erp.identity_lockouts SET failures = 0, locked_until = NULL WHERE login_name = $1`, user.LoginName); err != nil {
		t.Fatal(err)
	}
	sid = h.start(user.LoginName)
	status, raw, _ = h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	if status != http.StatusOK {
		t.Fatalf("after unlock %d %s", status, raw)
	}
}

func scenarioA9(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), false)
	user := h.user("a9-user", "secret", []string{"Sales Agent"}, false)
	a := h.login(user, "secret", "")
	b := h.login(user, "secret", "")
	status, raw, _ := h.authed(http.MethodGet, "/api/v1/me/sessions", b.access, b.key, nil)
	if status != http.StatusOK {
		t.Fatalf("list %d %s", status, raw)
	}
	sessions := a1Data[[]oapi.UserSession](t, raw)
	var target string
	for _, sess := range sessions {
		if sess.DeviceId == a.device {
			target = sess.Id
		}
	}
	if target == "" {
		t.Fatalf("session for device A missing: %+v", sessions)
	}
	status, raw, _ = h.authed(http.MethodDelete, "/api/v1/me/sessions/"+target, b.access, b.key, nil)
	if status != http.StatusOK {
		t.Fatalf("delete %d %s", status, raw)
	}
	me := "/api/v1/me"
	status, raw, _ = h.do(http.MethodGet, me, nil, map[string]string{
		"Authorization": "Bearer " + a.access,
		"DPoP":          h.proof(a.key, http.MethodGet, me, a.access),
	})
	a1Want(t, status, raw, http.StatusUnauthorized, string(apierr.TokenRevoked))
	status, raw, _ = h.authed(http.MethodGet, me, b.access, b.key, nil)
	if status != http.StatusOK {
		t.Fatalf("device B should remain %d %s", status, raw)
	}
}

func scenarioA10(t *testing.T, s *stack) {
	h := openA1(t, s.db, a1Policy(), false)
	acct := h.user("a10-acct", "secret", []string{"Accountant"}, false)
	key, pub := a1DeviceKey(t)
	status, raw, _ := h.do(http.MethodPost, "/api/v1/auth/device/enroll", map[string]any{
		"public_key": pub, "platform": string(oapi.Android), "app_version": "1.0.0", "device_name": "phone",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("enroll %d %s", status, raw)
	}
	dev := a1Data[struct {
		DeviceID string `json:"device_id"`
	}](t, raw).DeviceID
	sid := h.start(acct.LoginName)
	status, raw, _ = h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"password": "secret"}, nil)
	if status != http.StatusOK {
		t.Fatalf("password %d %s", status, raw)
	}
	path := "/api/v1/auth/token"
	status, raw, _ = h.do(http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(key, http.MethodPost, path, ""),
	})
	if status == http.StatusOK {
		t.Fatal("accountant completed login without a second factor")
	}
	status, raw, _ = h.do(http.MethodPost, "/api/v1/auth/session/"+sid+"/check", map[string]string{"totp": "654321"}, nil)
	if status != http.StatusOK {
		t.Fatalf("totp %d %s", status, raw)
	}
	status, raw, _ = h.do(http.MethodPost, path, map[string]string{"session_id": sid, "device_id": dev}, map[string]string{
		"DPoP": h.proof(key, http.MethodPost, path, ""),
	})
	if status != http.StatusOK {
		t.Fatalf("with totp %d %s", status, raw)
	}
	agent := h.user("a10-agent", "secret", []string{"Sales Agent"}, false)
	got := h.login(agent, "secret", "")
	if got.access == "" {
		t.Fatal("sales agent should complete login with a password")
	}
}
