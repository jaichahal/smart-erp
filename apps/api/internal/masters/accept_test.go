package masters

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

type world struct {
	t   *testing.T
	db  *testdb.DB
	ctx context.Context
	co  uuid.UUID
	who map[string]rls.Principal
}

type body struct {
	Data map[string]any `json:"data"`
	Err  struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db := testdb.New(t)
	migrateMasters(t, db)
	co := uuid.New()
	ctx := context.Background()
	if _, err := db.Migrator.Exec(ctx, `INSERT INTO erp.companies (id, legal_name) VALUES ($1, 'Masters Co')`, co); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, db: db, ctx: ctx, co: co, who: map[string]rls.Principal{}}
	w.person("acct", "Accountant", "accounts", "accountant")
	w.person("cfo", "CFO", "management", "stakeholder")
	w.person("partner", "Partner", "management", "stakeholder")
	w.person("finance", "Finance Manager", "management", "stakeholder")
	w.person("boss", "Approver", "sales", "approver")
	w.person("enter", "Enterer", "sales", "approver")
	w.person("other", "Other Approver", "sales", "approver")
	w.person("credit", "Credit Controller", "accounts", "credit_controller")
	w.person("mgr", "System Manager", "it", "system_manager")
	return w
}

func (w *world) person(id, name, dept string, roles ...string) {
	w.t.Helper()
	uid := id + "-" + w.co.String()
	w.who[id] = rls.Principal{UserID: uid, CompanyID: w.co, Roles: roles}
	if _, err := w.db.Migrator.Exec(w.ctx, `INSERT INTO erp.users (id, company_id, name) VALUES ($1, $2, $3)`, uid, w.co, name); err != nil {
		w.t.Fatal(err)
	}
	for _, role := range roles {
		if _, err := w.db.Migrator.Exec(w.ctx, `INSERT INTO erp.user_roles (user_id, role_name) VALUES ($1, $2)`, uid, role); err != nil {
			w.t.Fatal(err)
		}
	}
	if _, err := w.db.Migrator.Exec(w.ctx, `
		INSERT INTO erp.approval_actors (company_id, user_id, name, department, roles)
		VALUES ($1, $2, $3, $4, $5)`, w.co, uid, name, dept, roles); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) uid(name string) string { return w.who[name].UserID }

func (w *world) router() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			ctx := rls.WithPrincipal(req.Context(), w.who[req.Header.Get("X-User")])
			next.ServeHTTP(rw, req.WithContext(ctx))
		})
	})
	Mount(r, httpx.Deps{Pool: w.db.App})
	return r
}

func (w *world) call(method, user, path, raw string, match int64) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	req.Header.Set("If-Match", fmt.Sprintf("%d", match))
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	w.router().ServeHTTP(rec, req)
	return rec
}

func (w *world) post(user, path, raw string, match int64) body {
	w.t.Helper()
	return decode(w.t, w.call(http.MethodPost, user, path, raw, match))
}

func (w *world) get(user, path string) body {
	w.t.Helper()
	return decode(w.t, w.call(http.MethodGet, user, path, "", 0))
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) body {
	t.Helper()
	var b body
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("status %d body %s: %v", rec.Code, rec.Body.String(), err)
	}
	b.Data = dataOrEmpty(b.Data)
	if id, ok := b.Data["id"]; ok {
		b.Data["id"] = fmt.Sprint(id)
	}
	return b
}

func dataOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (b body) str(key string) string {
	if b.Data == nil {
		return ""
	}
	v, ok := b.Data[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (b body) ver() int64 {
	s := b.str("state_version")
	var n int64
	fmt.Sscan(s, &n)
	return n
}

func (w *world) approve(requestID, user, step string, match int64) body {
	w.t.Helper()
	raw := fmt.Sprintf(`{"decision":"approve","reason":"ok","step_up_token":%q}`, step)
	return w.post(user, "/master-approvals/"+requestID, raw, match)
}

func (w *world) mustCode(b body, status int, code string, rec *httptest.ResponseRecorder) {
	w.t.Helper()
	if rec != nil && rec.Code != status {
		w.t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if code != "" && b.Err.Code != code {
		w.t.Fatalf("code %s data %v", b.Err.Code, b.Data)
	}
}

func (w *world) openCustomer(credit string) body {
	w.t.Helper()
	created := w.post("acct", "/customers", fmt.Sprintf(`{"name":"Ada","credit_limit":%q,"change_reason":"open"}`, credit), 0)
	if created.Err.Code != "" || created.str("status") != "pending_approval" || created.str("approval_request_id") == "" {
		w.t.Fatalf("customer create %+v", created)
	}
	decided := w.approve(created.str("approval_request_id"), "boss", "", created.ver())
	if decided.Err.Code != "" || decided.str("status") != "approved" {
		w.t.Fatalf("customer approve %+v created=%+v", decided, created)
	}
	return decided
}

func (w *world) openVendor(bank bool) body {
	w.t.Helper()
	raw := `{"name":"Mill","currency":"AED","change_reason":"open"}`
	if bank {
		raw = `{"name":"Mill","currency":"AED","change_reason":"open","bank":{"holder":"Mill LLC","bank_name":"ADCB","iban":"AE070331234567890123456"}}`
	}
	created := w.post("acct", "/vendors", raw, 0)
	if created.Err.Code != "" || created.str("approval_request_id") == "" || created.str("status") == "approved" {
		w.t.Fatalf("vendor create %+v", created)
	}
	w.bootstrapCFO()
	decided := w.approve(created.str("approval_request_id"), "cfo", "", created.ver())
	if decided.Err.Code != "" || decided.str("status") != "approved" {
		w.t.Fatalf("vendor approve %+v", decided)
	}
	return decided
}

func (w *world) bootstrapCFO() {
	w.t.Helper()
	got := w.post("mgr", "/executive-titles", fmt.Sprintf(`{"user_id":%q,"title":"cfo"}`, w.uid("cfo")), 0)
	if got.Err.Code != "" && got.str("title") != "cfo" && got.Err.Code != "CONFLICT" {
		w.t.Fatalf("title %+v", got)
	}
}

func (w *world) openSKU(class, floor string) body {
	w.t.Helper()
	raw := fmt.Sprintf(`{"code":%q,"name":%q,"item_class":%q,"base_uom":"EA","floor_price":%q,"min_margin":"0","reorder_level":"0","purchase_uom":"EA","stock_uom":"EA","production_uom":"EA","sales_uom":"EA","units":[{"uom":"EA","factor_to_base":"1"}],"change_reason":"open"}`,
		"SKU-"+uuid.NewString()[:8], class, class, floor)
	created := w.post("acct", "/skus", raw, 0)
	if created.Err.Code != "" || created.str("approval_request_id") == "" {
		w.t.Fatalf("sku create %+v", created)
	}
	decided := w.approve(created.str("approval_request_id"), "boss", "", created.ver())
	if decided.Err.Code != "" || decided.str("status") != "approved" || decided.str("item_class") != class {
		w.t.Fatalf("sku approve %+v", decided)
	}
	return decided
}

// C15: a master change creates a new version. A document keeps the version it used.
func TestC15_DocumentKeepsPriorCustomerVersion(t *testing.T) {
	w := newWorld(t)
	opened := w.openCustomer("1000.00")
	v1 := opened.str("version_id")
	doc := w.post("acct", "/document-versions", fmt.Sprintf(
		`{"doc_type":"sales_order","doc_id":"SO-15","customer_id":%q}`, opened.str("id")), 0)
	if doc.str("customer_version_id") != v1 {
		t.Fatalf("pinned %s want %s", doc.str("customer_version_id"), v1)
	}
	changed := w.post("acct", "/customers/"+opened.str("id")+"/changes",
		`{"name":"Ada","credit_limit":"40.00","change_reason":"reduce"}`, opened.ver())
	if changed.str("approval_request_id") == "" {
		t.Fatalf("change %+v", changed)
	}
	approved := w.approve(changed.str("approval_request_id"), "boss", "", changed.ver())
	if approved.str("version_id") == "" || approved.str("version_id") == v1 {
		t.Fatalf("new version %+v", approved)
	}
	still := w.get("acct", "/document-versions/sales_order/SO-15")
	if still.str("customer_version_id") != v1 {
		t.Fatalf("document moved to %s", still.str("customer_version_id"))
	}
	current := w.get("acct", "/customers/"+opened.str("id"))
	if current.str("version_id") != approved.str("version_id") || current.str("credit_limit") != "40.0000" {
		t.Fatalf("current %+v", current.Data)
	}
}

// C16: the user who enters a vendor bank change cannot approve it. Step-up is required.
func TestC16_BankChangeNeedsOtherUserAndStepUp(t *testing.T) {
	w := newWorld(t)
	vendor := w.openVendor(true)
	v1 := vendor.str("bank_version_id")
	if v1 == "" {
		t.Fatal("missing bank version")
	}
	doc := w.post("acct", "/document-versions", fmt.Sprintf(
		`{"doc_type":"supplier_invoice","doc_id":"SI-16","vendor_id":%q,"bank_version_id":%q}`, vendor.str("id"), v1), 0)
	if doc.str("bank_version_id") != v1 {
		t.Fatalf("pin %+v", doc.Data)
	}
	changed := w.post("enter", "/vendors/"+vendor.str("id")+"/bank-accounts",
		`{"holder":"Mill LLC","bank_name":"ENBD","iban":"AE070331234567890999999","change_reason":"new bank"}`, vendor.ver())
	req := changed.str("approval_request_id")
	if req == "" {
		t.Fatalf("bank change %+v", changed)
	}
	self := w.call(http.MethodPost, "enter", "/master-approvals/"+req,
		fmt.Sprintf(`{"decision":"approve","reason":"ok","step_up_token":"stepup.bank_change.%s"}`, w.uid("enter")), changed.ver())
	selfBody := decode(t, self)
	if self.Code != http.StatusForbidden || selfBody.Err.Code != "SOD_VIOLATION" {
		t.Fatalf("self approval %d %+v", self.Code, selfBody)
	}
	missing := w.call(http.MethodPost, "other", "/master-approvals/"+req,
		`{"decision":"approve","reason":"ok","step_up_token":""}`, changed.ver())
	missingBody := decode(t, missing)
	if missing.Code != http.StatusForbidden || missingBody.Err.Code != "STEP_UP_REQUIRED" {
		t.Fatalf("step-up %d %+v", missing.Code, missingBody)
	}
	ok := w.approve(req, "other", "stepup.bank_change."+w.uid("other"), changed.ver())
	if ok.Err.Code != "" || ok.str("bank_version_id") == v1 || ok.str("bank_version_id") == "" {
		t.Fatalf("approve %+v", ok)
	}
	still := w.get("acct", "/document-versions/supplier_invoice/SI-16")
	if still.str("bank_version_id") != v1 {
		t.Fatalf("document followed the new bank %s", still.str("bank_version_id"))
	}
}

// C17: an order over the credit limit is held. A credit controller override is logged.
func TestC17_CreditHoldAndOverride(t *testing.T) {
	w := newWorld(t)
	customer := w.openCustomer("100.00")
	sku := w.openSKU("finished_goods", "1.00")
	rec := w.call(http.MethodPost, "acct", "/sales-orders", fmt.Sprintf(
		`{"customer_id":%q,"lines":[{"sku_id":%q,"qty":"1","uom":"EA","unit_price":"150.00"}]}`,
		customer.str("id"), sku.str("id")), 0)
	got := decode(t, rec)
	if rec.Code != http.StatusConflict || got.Err.Code != "CREDIT_HOLD" {
		t.Fatalf("hold %d %+v", rec.Code, got)
	}
	orderID, _ := got.Err.Details["order_id"].(string)
	if orderID == "" {
		t.Fatal("held order was not stored")
	}
	denied := w.call(http.MethodPost, "acct", "/sales-orders/"+orderID+"/credit-override", `{"reason":"known buyer"}`, 1)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("override role %d %s", denied.Code, denied.Body.String())
	}
	ok := w.post("credit", "/sales-orders/"+orderID+"/credit-override", `{"reason":"known buyer"}`, 1)
	if ok.Err.Code != "" || ok.str("status") != "overridden" {
		t.Fatalf("override %+v", ok)
	}
	report := w.get("credit", "/master-overrides?kind=credit")
	if !strings.Contains(fmt.Sprint(report.Data["rows"]), "known buyer") && report.str("reason") != "known buyer" {
		raw, _ := json.Marshal(report.Data)
		if !strings.Contains(string(raw), "known buyer") {
			t.Fatalf("override not reported: %s", raw)
		}
	}
}

// C18: a line below the floor price is held and the override is reported.
func TestC18_FloorHoldAndOverride(t *testing.T) {
	w := newWorld(t)
	customer := w.openCustomer("1000.00")
	sku := w.openSKU("finished_goods", "10.00")
	rec := w.call(http.MethodPost, "acct", "/sales-orders", fmt.Sprintf(
		`{"customer_id":%q,"lines":[{"sku_id":%q,"qty":"2","uom":"EA","unit_price":"9.00"}]}`,
		customer.str("id"), sku.str("id")), 0)
	got := decode(t, rec)
	if rec.Code != http.StatusConflict || got.Err.Code != "PRICE_FLOOR_HOLD" {
		t.Fatalf("floor %d %+v", rec.Code, got)
	}
	orderID, _ := got.Err.Details["order_id"].(string)
	ok := w.post("boss", "/sales-orders/"+orderID+"/floor-override", `{"reason":"clearance"}`, 1)
	if ok.Err.Code != "" || ok.str("status") != "overridden" {
		t.Fatalf("floor override %+v", ok)
	}
	report := w.get("boss", "/master-overrides?kind=price_floor")
	raw, _ := json.Marshal(report.Data)
	if !strings.Contains(string(raw), "clearance") {
		t.Fatalf("floor override not reported: %s", raw)
	}
}

// C19: a price agreement inside its period and quantity suppresses the floor hold.
func TestC19_AgreementSuppressesFloorHold(t *testing.T) {
	w := newWorld(t)
	customer := w.openCustomer("1000.00")
	sku := w.openSKU("finished_goods", "10.00")
	ag := w.post("acct", "/price-agreements", fmt.Sprintf(
		`{"customer_id":%q,"sku_id":%q,"valid_from":"2020-01-01","valid_to":"2099-12-31","min_qty":"1","max_qty":"100","price":"8.00","currency":"AED","change_reason":"deal"}`,
		customer.str("id"), sku.str("id")), 0)
	if ag.str("approval_request_id") == "" {
		t.Fatalf("agreement %+v", ag)
	}
	if decided := w.approve(ag.str("approval_request_id"), "boss", "", ag.ver()); decided.str("status") != "approved" {
		t.Fatalf("agreement approve %+v", decided)
	}
	got := w.post("acct", "/sales-orders", fmt.Sprintf(
		`{"customer_id":%q,"lines":[{"sku_id":%q,"qty":"10","uom":"EA","unit_price":"8.00"}]}`,
		customer.str("id"), sku.str("id")), 0)
	if got.Err.Code != "" || got.str("status") != "open" {
		t.Fatalf("agreement order %+v", got)
	}
	below := w.call(http.MethodPost, "acct", "/sales-orders", fmt.Sprintf(
		`{"customer_id":%q,"lines":[{"sku_id":%q,"qty":"10","uom":"EA","unit_price":"7.00"}]}`,
		customer.str("id"), sku.str("id")), 0)
	belowBody := decode(t, below)
	if below.Code != http.StatusConflict || belowBody.Err.Code != "PRICE_FLOOR_HOLD" {
		t.Fatalf("below agreement %d %+v", below.Code, belowBody)
	}
}

// C20: unit conversions round the same way through purchase, stock, production, and sales.
func TestC20_UnitConversionsRoundConsistently(t *testing.T) {
	w := newWorld(t)
	raw := `{"code":"CONV-1","name":"pack","item_class":"finished_goods","base_uom":"EA","floor_price":"1.00","min_margin":"0","reorder_level":"0","purchase_uom":"BOX","stock_uom":"EA","production_uom":"PAIR","sales_uom":"CASE","units":[{"uom":"EA","factor_to_base":"1"},{"uom":"BOX","factor_to_base":"12"},{"uom":"PAIR","factor_to_base":"2"},{"uom":"CASE","factor_to_base":"24"},{"uom":"TEN","factor_to_base":"3"}],"change_reason":"open"}`
	created := w.post("acct", "/skus", raw, 0)
	decided := w.approve(created.str("approval_request_id"), "boss", "", created.ver())
	id := decided.str("id")
	direct := w.get("acct", "/skus/"+id+"/convert?qty=1&from=purchase&to=sales")
	stock := w.get("acct", "/skus/"+id+"/convert?qty=1&from=purchase&to=stock")
	prod := w.get("acct", "/skus/"+id+"/convert?qty="+url.QueryEscape(stock.str("qty"))+"&from=stock&to=production")
	sales := w.get("acct", "/skus/"+id+"/convert?qty="+url.QueryEscape(prod.str("qty"))+"&from=production&to=sales")
	if direct.str("qty") != "0.500000" || sales.str("qty") != direct.str("qty") {
		t.Fatalf("direct %s chain %s", direct.str("qty"), sales.str("qty"))
	}
	third := w.get("acct", "/skus/"+id+"/convert?qty=1&from=stock&to=uom:TEN")
	if third.str("qty") != "0.333333" {
		t.Fatalf("third %s", third.str("qty"))
	}
}

// C21: a BOM change is approved and versioned. Production keeps the version it used.
func TestC21_BOMChangeIsVersioned(t *testing.T) {
	w := newWorld(t)
	finished := w.openSKU("finished_goods", "5.00")
	raw := w.openSKU("raw_material", "1.00")
	bom := w.post("acct", "/boms", fmt.Sprintf(
		`{"finished_sku_id":%q,"lines":[{"raw_sku_id":%q,"qty_per_unit":"2","uom":"EA","wastage_pct":"5"}],"change_reason":"first"}`,
		finished.str("id"), raw.str("id")), 0)
	if bom.str("approval_request_id") == "" {
		t.Fatalf("bom %+v", bom)
	}
	early := w.call(http.MethodPost, "acct", "/production-entries", fmt.Sprintf(`{"bom_id":%q,"qty":"1"}`, bom.str("id")), 0)
	if early.Code < 400 {
		t.Fatalf("unapproved bom was used: %s", early.Body.String())
	}
	v1 := w.approve(bom.str("approval_request_id"), "boss", "", bom.ver())
	if v1.str("version_id") == "" {
		t.Fatalf("bom approve %+v", v1)
	}
	entry := w.post("acct", "/production-entries", fmt.Sprintf(`{"bom_id":%q,"qty":"3"}`, bom.str("id")), 0)
	if entry.str("bom_version_id") != v1.str("version_id") {
		t.Fatalf("production %+v", entry)
	}
	change := w.post("acct", "/boms/"+bom.str("id")+"/changes", fmt.Sprintf(
		`{"lines":[{"raw_sku_id":%q,"qty_per_unit":"4","uom":"EA","wastage_pct":"1"}],"change_reason":"heavier"}`, raw.str("id")), v1.ver())
	v2 := w.approve(change.str("approval_request_id"), "boss", "", change.ver())
	if v2.str("version_id") == v1.str("version_id") {
		t.Fatal("bom did not version")
	}
	kept := w.get("acct", "/production-entries/"+entry.str("id"))
	if kept.str("bom_version_id") != v1.str("version_id") {
		t.Fatalf("production moved to %s", kept.str("bom_version_id"))
	}
	versions := w.get("acct", "/boms/"+bom.str("id")+"/versions")
	rawVersions, _ := json.Marshal(versions.Data)
	if !strings.Contains(string(rawVersions), v1.str("version_id")) || !strings.Contains(string(rawVersions), v2.str("version_id")) {
		t.Fatalf("versions %s", rawVersions)
	}
}

// C22: raw material is hidden from the sales stock view. Finished goods are not purchasable.
func TestC22_ItemClassSalesAndPurchase(t *testing.T) {
	w := newWorld(t)
	wh := w.post("acct", "/warehouses", `{"code":"MAIN","name":"Main"}`, 0)
	raw := w.openSKU("raw_material", "1.00")
	finished := w.openSKU("finished_goods", "1.00")
	both := w.openSKU("both", "1.00")
	for _, id := range []string{raw.str("id"), finished.str("id"), both.str("id")} {
		got := w.post("acct", "/stock/receipts", fmt.Sprintf(
			`{"sku_id":%q,"warehouse_id":%q,"qty":"5","unit_cost":"2.00"}`, id, wh.str("id")), 0)
		if got.Err.Code != "" {
			t.Fatalf("receipt %+v", got)
		}
	}
	sales := w.get("acct", "/stock/availability?view=sales&warehouse="+wh.str("id"))
	rawSales, _ := json.Marshal(sales.Data)
	if strings.Contains(string(rawSales), raw.str("id")) {
		t.Fatalf("raw material visible in sales stock: %s", rawSales)
	}
	if !strings.Contains(string(rawSales), finished.str("id")) || !strings.Contains(string(rawSales), both.str("id")) {
		t.Fatalf("sales view %s", rawSales)
	}
	vendor := w.openVendor(false)
	finishedBuy := w.call(http.MethodPost, "acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"lpo","vendor_id":%q,"sku_id":%q}`, vendor.str("id"), finished.str("id")), 0)
	if finishedBuy.Code < 400 {
		t.Fatalf("finished goods were purchasable: %s", finishedBuy.Body.String())
	}
	rawBuy := w.call(http.MethodPost, "acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"requisition","vendor_id":%q,"sku_id":%q}`, vendor.str("id"), raw.str("id")), 0)
	if rawBuy.Code < 400 {
		t.Fatal("generally approved vendor was treated as approved for the raw SKU")
	}
	bothBuy := w.post("acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"lpo","vendor_id":%q,"sku_id":%q}`, vendor.str("id"), both.str("id")), 0)
	if bothBuy.Err.Code != "" {
		t.Fatalf("both purchase %+v", bothBuy)
	}
}

func TestVendorInsertRequiresApproval(t *testing.T) {
	w := newWorld(t)
	skipped := w.call(http.MethodPost, "acct", "/vendors", `{"name":"Skip","currency":"AED","status":"approved"}`, 0)
	if skipped.Code < 400 {
		t.Fatalf("approved insert accepted: %s", skipped.Body.String())
	}
	created := w.post("acct", "/vendors", `{"name":"Pending","currency":"AED","change_reason":"open"}`, 0)
	if created.str("status") == "approved" || created.str("approval_request_id") == "" {
		t.Fatalf("vendor %+v", created)
	}
	var status string
	var request string
	err := rls.Tx(w.ctx, w.db.App, w.who["acct"], func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT status, coalesce(approval_request_id, '') FROM erp.vendors WHERE id = $1`, created.str("id")).Scan(&status, &request)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status == "approved" || request == "" {
		t.Fatalf("stored status %s request %s", status, request)
	}
}

func TestOnboardingOnlyCFOOrPartnerAndNotDelegable(t *testing.T) {
	w := newWorld(t)
	w.bootstrapCFO()
	created := w.post("acct", "/vendors", `{"name":"Gate","currency":"AED","change_reason":"open"}`, 0)
	req := created.str("approval_request_id")
	for _, user := range []string{"acct", "finance", "boss", "mgr"} {
		rec := w.call(http.MethodPost, user, "/master-approvals/"+req, `{"decision":"approve","reason":"ok"}`, created.ver())
		got := decode(t, rec)
		if rec.Code != http.StatusForbidden || got.Err.Code != "PERMISSION_DENIED" {
			t.Fatalf("%s approve %d %+v", user, rec.Code, got)
		}
	}
	delegated := w.call(http.MethodPost, "cfo", "/master-approvals/"+req, `{"decision":"delegate","to_user_id":"partner","reason":"away"}`, created.ver())
	if delegated.Code != http.StatusForbidden {
		t.Fatalf("delegate %d %s", delegated.Code, delegated.Body.String())
	}
	opened := w.approve(req, "cfo", "", created.ver())
	if opened.str("status") != "approved" {
		t.Fatalf("cfo %+v", opened)
	}
	sku := w.openSKU("raw_material", "1.00")
	add := w.post("acct", "/vendors/"+opened.str("id")+"/approved-skus", fmt.Sprintf(
		`{"sku_id":%q,"price":"3.50","currency":"AED","change_reason":"add"}`, sku.str("id")), opened.ver())
	if add.str("status") == "approved" {
		t.Fatal("sku addition skipped approval")
	}
	denied := w.call(http.MethodPost, "finance", "/master-approvals/"+add.str("approval_request_id"), `{"decision":"approve","reason":"ok"}`, add.ver())
	if denied.Code != http.StatusForbidden {
		t.Fatalf("finance sku %d %s", denied.Code, denied.Body.String())
	}
	deniedDel := w.call(http.MethodPost, "cfo", "/master-approvals/"+add.str("approval_request_id"), `{"decision":"delegate","to_user_id":"partner"}`, add.ver())
	if deniedDel.Code != http.StatusForbidden {
		t.Fatalf("sku delegate %d %s", deniedDel.Code, deniedDel.Body.String())
	}
	if got := w.approve(add.str("approval_request_id"), "cfo", "", add.ver()); got.str("status") != "approved" {
		t.Fatalf("sku approve %+v", got)
	}
	buy := w.post("acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"lpo","vendor_id":%q,"sku_id":%q}`, opened.str("id"), sku.str("id")), 0)
	if buy.Err.Code != "" {
		t.Fatalf("approved sku purchase %+v", buy)
	}
}

func TestBlacklistAndBestPrice(t *testing.T) {
	w := newWorld(t)
	cheap := w.openVendor(false)
	good := w.openVendor(false)
	sku := w.openSKU("raw_material", "1.00")
	add := func(vendor body, price string) int64 {
		w.t.Helper()
		row := w.post("acct", "/vendors/"+vendor.str("id")+"/approved-skus", fmt.Sprintf(
			`{"sku_id":%q,"price":%q,"currency":"AED","change_reason":"quote"}`, sku.str("id"), price), vendor.ver())
		got := w.approve(row.str("approval_request_id"), "cfo", "", row.ver())
		if got.str("status") != "approved" {
			w.t.Fatalf("sku price %+v", got)
		}
		return got.ver()
	}
	cheapVer := add(cheap, "1.00")
	_ = add(good, "4.00")
	posted := w.post("acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"supplier_invoice","vendor_id":%q,"sku_id":%q,"posted":true}`, cheap.str("id"), sku.str("id")), 0)
	if posted.Err.Code != "" || posted.str("posted") != "true" {
		t.Fatalf("posted invoice %+v", posted)
	}
	black := w.post("acct", "/vendors/"+cheap.str("id")+"/blacklist", `{"change_reason":"fraud"}`, cheapVer)
	denied := w.call(http.MethodPost, "acct", "/master-approvals/"+black.str("approval_request_id"), `{"decision":"approve","reason":"ok"}`, black.ver())
	if denied.Code != http.StatusForbidden {
		t.Fatalf("accountant blacklist %d %s", denied.Code, denied.Body.String())
	}
	if got := w.approve(black.str("approval_request_id"), "cfo", "", black.ver()); got.str("status") != "blacklisted" {
		t.Fatalf("blacklist %+v", got)
	}
	kept := w.get("acct", "/purchase-documents/"+posted.str("id"))
	if kept.str("posted") != "true" || kept.str("flagged") != "true" {
		t.Fatalf("posted history %+v", kept.Data)
	}
	newLPO := w.call(http.MethodPost, "acct", "/purchase-documents", fmt.Sprintf(
		`{"kind":"lpo","vendor_id":%q,"sku_id":%q}`, cheap.str("id"), sku.str("id")), 0)
	if newLPO.Code < 400 {
		t.Fatal("blacklisted vendor accepted on a new LPO")
	}
	pay := w.call(http.MethodPost, "acct", "/vendor-payments", fmt.Sprintf(
		`{"vendor_id":%q,"invoice_id":%q,"amount":"1.00"}`, cheap.str("id"), posted.str("id")), 0)
	if pay.Code < 400 {
		t.Fatal("blacklisted payment was accepted")
	}
	payBody := decode(t, pay)
	paymentID, _ := payBody.Err.Details["payment_id"].(string)
	if paymentID == "" {
		paymentID = payBody.str("id")
	}
	relDenied := w.call(http.MethodPost, "finance", "/vendor-payments/"+paymentID+"/release", `{}`, 1)
	if relDenied.Code != http.StatusForbidden {
		t.Fatalf("finance release %d %s", relDenied.Code, relDenied.Body.String())
	}
	if got := w.post("cfo", "/vendor-payments/"+paymentID+"/release", `{}`, 1); got.str("status") != "released" {
		t.Fatalf("release %+v", got)
	}
	best := w.get("acct", "/vendors/best-price?sku_id="+sku.str("id"))
	if best.str("vendor_id") != good.str("id") || best.str("price") != "4.0000" {
		t.Fatalf("best price %+v want vendor %s", best.Data, good.str("id"))
	}
}

func TestMovingAverageAndAvailable(t *testing.T) {
	w := newWorld(t)
	wh := w.post("acct", "/warehouses", `{"code":"WH2","name":"Two"}`, 0)
	sku := w.openSKU("raw_material", "1.00")
	if got := w.post("acct", "/stock/receipts", fmt.Sprintf(
		`{"sku_id":%q,"warehouse_id":%q,"qty":"10","unit_cost":"5.00"}`, sku.str("id"), wh.str("id")), 0); got.Err.Code != "" {
		t.Fatal(got)
	}
	if got := w.post("acct", "/stock/receipts", fmt.Sprintf(
		`{"sku_id":%q,"warehouse_id":%q,"qty":"10","unit_cost":"7.00"}`, sku.str("id"), wh.str("id")), 0); got.Err.Code != "" {
		t.Fatal(got)
	}
	avail := w.get("acct", "/stock/availability?warehouse="+wh.str("id")+"&sku="+sku.str("id"))
	if avail.str("on_hand") != "20.000000" || avail.str("unit_cost") != "6.0000" || avail.str("value") != "120.0000" {
		t.Fatalf("average %+v", avail.Data)
	}
	if got := w.post("acct", "/stock/reservations", fmt.Sprintf(
		`{"sku_id":%q,"warehouse_id":%q,"qty":"4"}`, sku.str("id"), wh.str("id")), 0); got.Err.Code != "" {
		t.Fatal(got)
	}
	avail = w.get("acct", "/stock/availability?warehouse="+wh.str("id")+"&sku="+sku.str("id"))
	if avail.str("available") != "16.000000" || avail.str("on_hand") != "20.000000" {
		t.Fatalf("available %+v", avail.Data)
	}
	issue := w.call(http.MethodPost, "acct", "/stock/issues", fmt.Sprintf(
		`{"sku_id":%q,"warehouse_id":%q,"qty":"17"}`, sku.str("id"), wh.str("id")), 0)
	if issue.Code != http.StatusConflict || decode(t, issue).Err.Code != "NEGATIVE_STOCK" {
		t.Fatalf("negative %d %s", issue.Code, issue.Body.String())
	}
}

func migrateMasters(t *testing.T, db *testdb.DB) {
	t.Helper()
	dsn := withDB(os.Getenv("ERP_MIGRATOR_DATABASE_URL"), db.Name)
	sqldb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(sqldb, "."); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := outbox.Migrate(context.Background(), db.Migrator); err != nil {
		t.Fatalf("river: %v", err)
	}
}

func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(db, "/")
	return u.String()
}
