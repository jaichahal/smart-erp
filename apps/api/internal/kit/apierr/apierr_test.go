package apierr_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// A17: every error response matches the envelope schema and carries the request id.
func TestA17_EnvelopeShape(t *testing.T) {
	h := apierr.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, r, apierr.New(apierr.PermissionDenied, "nope").WithDetails(map[string]any{"role": "agent"}))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", http.NoBody)
	req.Header.Set("X-Request-ID", "req-123")
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Details   map[string]any `json:"details"`
			RequestID string         `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "PERMISSION_DENIED" || body.Error.RequestID != "req-123" || body.Error.Details["role"] != "agent" {
		t.Fatalf("bad envelope: %s", rec.Body)
	}
}

func TestUnknownErrorsBecomeInternalWithoutLeaking(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", http.NoBody)
	apierr.Write(rec, req, errors.New("pq: password authentication failed for user erp_app"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "password") || !strings.Contains(rec.Body.String(), `"INTERNAL_ERROR"`) {
		t.Fatalf("internal cause leaked or wrong code: %d %s", rec.Code, rec.Body)
	}
}

func TestEveryCodeHasAStatus(t *testing.T) {
	for _, c := range apierr.All {
		if s := apierr.New(c, "x").Status(); s < 400 || s > 599 {
			t.Fatalf("code %s has status %d", c, s)
		}
	}
}

// The code list in code and the OpenAPI enum must be identical (04 "Codes").
func TestCodesMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../../../contracts/openapi/openapi.yaml")
	if err != nil {
		t.Skipf("openapi not present yet: %v", err)
	}
	spec := string(raw)
	for _, c := range apierr.All {
		if !regexp.MustCompile(`(?m)^\s*-\s*` + string(c) + `\s*$`).MatchString(spec) {
			t.Errorf("code %s missing from OpenAPI Error.code enum", c)
		}
	}
}
