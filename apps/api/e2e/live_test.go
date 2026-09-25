package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestP0_DesignTokens(t *testing.T) {
	s := newStack(t)
	status, _, raw := s.call(t, http.MethodGet, "/api/v1/design/tokens", "", nil)
	if status != http.StatusOK {
		t.Fatalf("P0.1 tokens: status %d %s", status, raw)
	}
	var env struct {
		Data struct {
			Colour struct {
				Semantic map[string]map[string]string `json:"semantic"`
				Data     []string                    `json:"data"`
			} `json:"colour"`
			TypeScale map[string][]string `json:"type_scale"`
			Direction map[string]struct {
				Components []string `json:"components"`
			} `json:"direction"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"light", "dark"} {
		sem := env.Data.Colour.Semantic[theme]
		for _, role := range []string{"success", "warning", "critical", "info", "neutral"} {
			if sem[role] == "" {
				t.Fatalf("P0.1 missing %s %s", theme, role)
			}
		}
	}
	if len(env.Data.Colour.Data) != 8 {
		t.Fatalf("P0.1 data palette len %d", len(env.Data.Colour.Data))
	}
	if len(env.Data.TypeScale["latin"]) == 0 || len(env.Data.TypeScale["arabic"]) == 0 {
		t.Fatal("P0.1 type scale must cover Arabic and Latin")
	}
	ltr := env.Data.Direction["ltr"].Components
	rtl := env.Data.Direction["rtl"].Components
	if len(ltr) == 0 || !bytes.Equal([]byte(join(ltr)), []byte(join(rtl))) {
		t.Fatalf("P0.1 RTL and LTR component sets differ: %v vs %v", ltr, rtl)
	}
}

func join(in []string) string {
	out := ""
	for _, s := range in {
		out += s + "\n"
	}
	return out
}

func TestA1_FailedLoginsAreByteIdentical(t *testing.T) {
	s := newStack(t)
	wrong := s.user("a1-wrong", "secret", []string{"Sales Agent"}, false)
	disabled := s.user("a1-disabled", "secret", []string{"Sales Agent"}, true)
	mfa := s.user("a1-mfa", "secret", []string{"Accountant"}, false)
	fail := func(login, password string) (int, []byte) {
		t.Helper()
		_, _, created := s.call(t, http.MethodPost, "/api/v1/auth/session", `{"login_name":"`+login+`"}`, map[string]string{"X-Request-ID": "req-a1"})
		var env struct {
			Data struct {
				SessionID string `json:"session_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(created, &env); err != nil || env.Data.SessionID == "" {
			t.Fatalf("session create: %s", created)
		}
		status, _, body := s.call(t, http.MethodPost, "/api/v1/auth/session/"+env.Data.SessionID+"/check", `{"password":"`+password+`"}`, map[string]string{"X-Request-ID": "req-a1"})
		return status, body
	}
	s1, b1 := fail(wrong.LoginName, "nope")
	s2, b2 := fail(disabled.LoginName, "secret")
	s3, b3 := fail("a1-nobody", "secret")
	// MFA required: password is correct but a second factor is still required, so token issuance fails the same way.
	_, _, created := s.call(t, http.MethodPost, "/api/v1/auth/session", `{"login_name":"`+mfa.LoginName+`"}`, map[string]string{"X-Request-ID": "req-a1"})
	var env struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created, &env); err != nil || env.Data.SessionID == "" {
		t.Fatalf("mfa session: %s", created)
	}
	okStatus, _, okBody := s.call(t, http.MethodPost, "/api/v1/auth/session/"+env.Data.SessionID+"/check", `{"password":"secret"}`, map[string]string{"X-Request-ID": "req-a1"})
	if okStatus != http.StatusOK {
		t.Fatalf("mfa password step %d %s", okStatus, okBody)
	}
	status4, _, body4 := s.call(t, http.MethodPost, "/api/v1/auth/token", `{"session_id":"`+env.Data.SessionID+`","device_id":"missing"}`, map[string]string{"X-Request-ID": "req-a1"})
	for i, got := range []struct {
		status int
		body   []byte
	}{{s2, b2}, {s3, b3}, {status4, body4}} {
		if got.status != s1 || !bytes.Equal(got.body, b1) {
			t.Fatalf("A1 cause %d status %d/%d\n%s\n%s", i+2, s1, got.status, b1, got.body)
		}
	}
}

func TestA17_ErrorEnvelope(t *testing.T) {
	s := newStack(t)
	for _, path := range []string{"/api/v1/does-not-exist", "/nope"} {
		status, code, raw := s.call(t, http.MethodGet, path, "", nil)
		if status < 400 {
			t.Fatalf("A17 %s status %d", path, status)
		}
		assertEnvelope(t, code, raw)
	}
	status, code, raw := s.call(t, http.MethodPost, "/health", `{}`, nil)
	if status < 400 {
		t.Fatalf("A17 method status %d", status)
	}
	assertEnvelope(t, code, raw)
}

func assertEnvelope(t *testing.T, code string, raw []byte) {
	t.Helper()
	var env struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Details   map[string]any `json:"details"`
			RequestID string         `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("A17 not json: %s", raw)
	}
	if env.Error.Code == "" || env.Error.Message == "" || env.Error.Details == nil || env.Error.RequestID == "" || env.Error.Code != code {
		t.Fatalf("A17 envelope incomplete: %s", raw)
	}
}

func TestB1_AppRoleCannotMutateImmutable(t *testing.T) {
	s := newStack(t)
	ctx := t.Context()
	if _, err := s.db.App.Exec(ctx, `UPDATE erp.audit_events SET hash = hash`); err == nil {
		t.Fatal("B1 application role updated erp.audit_events")
	}
	if _, err := s.db.App.Exec(ctx, `DELETE FROM erp.audit_events`); err == nil {
		t.Fatal("B1 application role deleted from erp.audit_events")
	}
}
