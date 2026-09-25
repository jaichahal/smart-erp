package idempotency_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func server(t *testing.T, db *testdb.DB, calls *int32) http.Handler {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		httpx.JSON(w, r, 201, map[string]any{"n": atomic.LoadInt32(calls)})
	})
	withPrincipal := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := rls.WithPrincipal(r.Context(), rls.Principal{UserID: "u1", CompanyID: uuid.New()})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	return apierr.RequestIDMiddleware(withPrincipal(idempotency.Middleware(db.App)(inner)))
}

// A18: same key and body replays; different body with same key is 409.
func TestA18_ReplayAndConflict(t *testing.T) {
	db := testdb.New(t)
	var calls int32
	h := server(t, db, &calls)
	key := uuid.NewString()
	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/things", bytes.NewBufferString(body))
		req.Header.Set(idempotency.Header, key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := do(`{"a":1}`)
	second := do(`{"a":1}`)
	if first.Code != 201 || second.Code != 201 {
		t.Fatalf("codes %d %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay must return the stored body:\n%s\n%s", first.Body, second.Body)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("handler must run once; calls=%d", calls)
	}
	third := do(`{"a":2}`)
	if third.Code != 409 {
		t.Fatalf("different body same key must be 409, got %d %s", third.Code, third.Body)
	}
}

func TestMissingKeyIsValidationError(t *testing.T) {
	db := testdb.New(t)
	var calls int32
	h := server(t, db, &calls)
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte(`"VALIDATION_ERROR"`)) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestGetIsNotGated(t *testing.T) {
	db := testdb.New(t)
	var calls int32
	h := server(t, db, &calls)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", http.NoBody))
	if rec.Code != 201 {
		t.Fatalf("GET must pass through, got %d", rec.Code)
	}
}
