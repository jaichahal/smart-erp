package sales

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func TestHTTPCreditHoldDoesNotPost(t *testing.T) {
	w := newWorld(t)
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "10.00", 30, 0, "0", "cust-v1", true, false)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(rw, req.WithContext(rls.WithPrincipal(req.Context(), w.p)))
		})
	})
	Mount(r, httpx.Deps{Pool: w.pool})
	body := `{"customer_id":"cust-1","warehouse_id":"WH","currency":"AED","as_of":"2026-09-15T09:00:00Z","lines":[{"sku":"SKU1","qty":"2","unit_price":"20.00"}]}`
	req := httptest.NewRequest(http.MethodPost, "/sales-orders", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", uuid.NewString())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CREDIT_HOLD") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_postings WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("held order posted to the ledger")
	}
}
