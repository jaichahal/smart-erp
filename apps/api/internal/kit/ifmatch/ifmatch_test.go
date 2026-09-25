package ifmatch_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
)

// A19: a stale If-Match returns 409 with the current state.
func TestA19_StaleVersionIsConflictWithCurrentState(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", http.NoBody)
	req.Header.Set(ifmatch.Header, `"3"`)
	v, present, err := ifmatch.Parse(req, true)
	if err != nil || !present || v != 3 {
		t.Fatalf("parse: %v %v %d", err, present, v)
	}
	err = ifmatch.Check(v, 5, map[string]any{"state": "approved"})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.Conflict || ae.Status() != 409 || ae.Details["current_state_version"] != int64(5) {
		t.Fatalf("expected 409 CONFLICT with current state, got %v", err)
	}
	if ifmatch.Check(5, 5, http.NoBody) != nil {
		t.Fatal("equal versions must pass")
	}
}

func TestRequiredHeaderMissingIsValidationError(t *testing.T) {
	_, _, err := ifmatch.Parse(httptest.NewRequest("POST", "/x", http.NoBody), true)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.ValidationError {
		t.Fatalf("got %v", err)
	}
	if _, present, err := ifmatch.Parse(httptest.NewRequest("POST", "/x", http.NoBody), false); err != nil || present {
		t.Fatal("optional absent header must be a no-op")
	}
}
