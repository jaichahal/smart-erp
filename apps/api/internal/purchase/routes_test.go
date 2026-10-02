package purchase_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/purchase"
)

func purchaseRouter(w *world) http.Handler {
	r := chi.NewRouter()
	r.Use(apierr.RequestIDMiddleware)
	purchase.Handler{Svc: w.svc}.Routes(r)
	return r
}

func getAuthed(h http.Handler, path string, p *rls.Principal) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if p != nil {
		req = req.WithContext(rls.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGETLposListsCompanyLPOs(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Listed", raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "2", "5")})
	w.must(err)
	if _, err := w.svc.CreateRequisition(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "1")}); err != nil {
		t.Fatal(err)
	}
	h := purchaseRouter(w)
	anon := getAuthed(h, "/lpos", nil)
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /lpos: %d %s", anon.Code, anon.Body.String())
	}
	caller := w.as("clerk")
	rec := getAuthed(h, "/lpos", &caller)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /lpos: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []struct {
			ID      uuid.UUID `json:"id"`
			Number  string    `json:"number"`
			Status  string    `json:"status"`
			DocType string    `json:"doc_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 || env.Data[0].ID != lpo.ID || env.Data[0].Number != lpo.Number || env.Data[0].DocType != "lpo" {
		t.Fatalf("lpo list %+v", env.Data)
	}
	other := rls.Principal{UserID: "stranger", CompanyID: uuid.New(), Roles: []string{"clerk"}}
	hidden := getAuthed(h, "/lpos", &other)
	if hidden.Code != http.StatusOK {
		t.Fatalf("other company GET /lpos: %d %s", hidden.Code, hidden.Body.String())
	}
	var empty struct {
		Data []struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(hidden.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Data) != 0 {
		t.Fatalf("other company saw %+v", empty.Data)
	}
}

func TestGETVendorsDashboardFiltersSKUAndBestPrice(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	good := w.onboard("Ranked", raw)
	bad := w.onboard("Blocked", raw)
	general := w.onboard("General")
	window := purchase.DocInput{EffectiveFrom: "2020-01-01", EffectiveTo: "2099-01-01"}
	usd := window
	usd.VendorID = good.ID
	usd.Currency = "USD"
	usd.FXRate = "3.67"
	usd.Lines = w.lines(raw, "1", "10")
	best, err := w.svc.CreateQuote(w.ctx, w.as("accountant"), usd)
	w.must(err)
	best, err = w.svc.ApproveCommercial(w.ctx, w.as("accountant"), best.ID)
	w.must(err)
	higher := window
	higher.VendorID = good.ID
	higher.Lines = w.lines(raw, "1", "40")
	q, err := w.svc.CreateQuote(w.ctx, w.as("accountant"), higher)
	w.must(err)
	if _, err := w.svc.ApproveCommercial(w.ctx, w.as("accountant"), q.ID); err != nil {
		t.Fatal(err)
	}
	cheap := window
	cheap.VendorID = bad.ID
	cheap.Lines = w.lines(raw, "1", "1")
	bq, err := w.svc.CreateQuote(w.ctx, w.as("accountant"), cheap)
	w.must(err)
	if _, err := w.svc.ApproveCommercial(w.ctx, w.as("accountant"), bq.ID); err != nil {
		t.Fatal(err)
	}
	w.blacklist(bad.ID)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: good.ID, Lines: w.lines(raw, "1", "10")})
	w.must(err)
	if _, err := w.svc.ReceiveGoods(w.ctx, w.as("clerk"), lpo.ID, w.lines(raw, "1", "10")); err != nil {
		t.Fatal(err)
	}
	openInv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: good.ID, SupplierNumber: "OPEN-D", Lines: w.lines(raw, "1", "1"),
	})
	w.must(err)
	posted, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: good.ID, SourceID: &lpo.ID, SupplierNumber: "PAST-D", Lines: w.lines(raw, "1", "10"),
	})
	w.must(err)
	if _, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), posted.ID, false); err != nil {
		t.Fatal(err)
	}
	_ = openInv
	h := purchaseRouter(w)
	path := "/vendors/dashboard?sku=" + url.QueryEscape(raw.String())
	anon := getAuthed(h, path, nil)
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard: %d %s", anon.Code, anon.Body.String())
	}
	clerk := w.as("clerk")
	rec := getAuthed(h, path, &clerk)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vendors/dashboard: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data purchase.Dashboard `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]purchase.VendorRow{}
	for _, row := range env.Data.Vendors {
		seen[row.VendorID] = row
	}
	if _, ok := seen[good.ID]; !ok {
		t.Fatalf("approved vendor missing: %+v", env.Data.Vendors)
	}
	if _, ok := seen[general.ID]; ok {
		t.Fatal("generally approved vendor is on the SKU list")
	}
	if seen[good.ID].ActiveInvoices != 1 || seen[good.ID].PastInvoices != 1 {
		t.Fatalf("counts %+v", seen[good.ID])
	}
	if env.Data.Best == nil || env.Data.Best.DocumentID != best.ID || env.Data.Best.Currency != "USD" || env.Data.Best.AED != "36.7000" || env.Data.Best.Number == "" {
		t.Fatalf("best %+v", env.Data.Best)
	}
	if env.Data.Best.VendorID == bad.ID || env.Data.Best.VendorID == general.ID {
		t.Fatal("unapproved or blacklisted vendor ranked as best")
	}
	reasons := map[string]bool{}
	for _, row := range env.Data.Blocked {
		reasons[row.Reason] = true
		if row.VendorID == bad.ID && row.Reason == "blacklisted" && row.Source != nil && row.Source.DocumentID == best.ID {
			t.Fatal("best source listed as the blacklisted quote")
		}
	}
	if !reasons["blacklisted"] || !reasons["not_approved_for_sku"] {
		t.Fatalf("blocked %+v", env.Data.Blocked)
	}
	if len(env.Data.Actions) != 0 {
		t.Fatalf("clerk actions %v", env.Data.Actions)
	}
	cfo := w.as("cfo")
	gated := getAuthed(h, path, &cfo)
	if gated.Code != http.StatusOK {
		t.Fatalf("cfo dashboard: %d %s", gated.Code, gated.Body.String())
	}
	var cfoEnv struct {
		Data purchase.Dashboard `json:"data"`
	}
	if err := json.Unmarshal(gated.Body.Bytes(), &cfoEnv); err != nil {
		t.Fatal(err)
	}
	if len(cfoEnv.Data.Actions) != 2 || cfoEnv.Data.Actions[0] != "approve" || cfoEnv.Data.Actions[1] != "blacklist" {
		t.Fatalf("cfo actions %v", cfoEnv.Data.Actions)
	}
}

func TestGETVendorsDashboardNotCapturedAsVendorID(t *testing.T) {
	w := newWorld(t)
	r := chi.NewRouter()
	r.Get("/vendors/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(chi.URLParam(r, "id")))
	})
	purchase.Handler{Svc: w.svc}.Routes(r)
	clerk := w.as("clerk")
	rec := getAuthed(r, "/vendors/dashboard", &clerk)
	if rec.Code == http.StatusTeapot || rec.Body.String() == "dashboard" {
		t.Fatalf("GET /vendors/dashboard fell through to /vendors/{id}: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vendors/dashboard: %d %s", rec.Code, rec.Body.String())
	}
}
