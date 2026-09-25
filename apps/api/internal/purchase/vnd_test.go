package purchase_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/internal/purchase"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

type world struct {
	t       *testing.T
	db      *testdb.DB
	svc     *purchase.Service
	appr    *approvals.Service
	company uuid.UUID
	ctx     context.Context
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db := testdb.New(t)
	migrateDB(t, db)
	client, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	appr := approvals.New(db.App, client, approvals.NewSQLDirectory(db.App), approvals.StubStepUp{}, nil)
	w := &world{t: t, db: db, svc: purchase.New(db.App, appr), appr: appr, company: uuid.New(), ctx: context.Background()}
	w.actor("accountant", "Accountant", "Accounts", "accountant")
	w.actor("finance", "Finance Manager", "Finance", "stakeholder", "finance_manager")
	w.actor("cto", "CTO", "Technology", "stakeholder", "cto")
	w.actor("approver", "Approver", "Management", "approver")
	w.actor("auditor", "Auditor", "Audit", "auditor")
	w.actor("sys", "System Manager", "IT", "system_manager")
	w.actor("cfo", "CFO", "Finance", "stakeholder")
	w.actor("partner", "Partner", "Owners", "stakeholder")
	w.actor("clerk", "Clerk", "Warehouse", "clerk")
	w.actor("reviewer", "Reviewer", "Management", "approver", "stakeholder")
	if err := w.svc.PutTitle(w.ctx, w.as("cfo"), "cfo", "cfo"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.PutTitle(w.ctx, w.as("cfo"), "partner", "partner"); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) actor(id, name, dept string, roles ...string) {
	w.t.Helper()
	if err := w.svc.EnsureActor(w.ctx, w.as(id), approvals.Actor{ID: id, Name: name, Department: dept, Roles: roles}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) as(id string) rls.Principal {
	return rls.Principal{UserID: id, CompanyID: w.company, Roles: []string{"accountant"}}
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) code(err error) apierr.Code {
	w.t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		w.t.Fatalf("want api error, got %v", err)
	}
	return ae.Code
}

func (w *world) sku(class string) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	w.must(w.svc.PutSKU(w.ctx, w.as("accountant"), id, class, class))
	return id
}

func (w *world) onboard(name string, skus ...uuid.UUID) purchase.Vendor {
	w.t.Helper()
	g, err := w.svc.PrepareOnboarding(w.ctx, w.as("accountant"), purchase.OnboardingInput{
		Name: name, BankAccount: "AE070331234567890123456", TradeLicenceKey: "TL-" + name, SKUIDs: skus,
	})
	w.must(err)
	return w.finish(g, "partner", "")
}

func (w *world) finish(g purchase.Gate, approver, step string) purchase.Vendor {
	w.t.Helper()
	out, err := w.svc.Approve(w.ctx, w.as(approver), g.ID, g.StateVersion, step)
	w.must(err)
	got, err := w.svc.Register(w.ctx, w.as(approver), g.ID, out.PostingToken, map[string]any{"status": "approved", "name": "client-rewritten"})
	w.must(err)
	v, ok := got.(purchase.Vendor)
	if !ok {
		w.t.Fatalf("register returned %T", got)
	}
	if v.Name == "client-rewritten" {
		w.t.Fatal("client decision field changed the vendor")
	}
	return v
}

func (w *world) addSKU(vendor, sku uuid.UUID) {
	w.t.Helper()
	g, err := w.svc.PrepareSKUAdd(w.ctx, w.as("finance"), vendor, sku)
	w.must(err)
	w.finish(g, "partner", "")
}

func (w *world) blacklist(vendor uuid.UUID) purchase.Vendor {
	w.t.Helper()
	g, err := w.svc.PrepareBlacklist(w.ctx, w.as("accountant"), vendor, "fraud")
	w.must(err)
	return w.finish(g, "cfo", "")
}

func (w *world) lines(sku uuid.UUID, qty, price string) []purchase.LineInput {
	return []purchase.LineInput{{SKUID: sku, Qty: qty, UnitPrice: price}}
}

func (w *world) vendorCount() int {
	w.t.Helper()
	var n int
	err := rls.Tx(w.ctx, w.db.App, w.as("cfo"), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM erp.purchase_vendors WHERE company_id=$1`, w.company).Scan(&n)
	})
	w.must(err)
	return n
}

func TestVND1_OnlyCFOOrPartnerApprovesAndDelegationRefused(t *testing.T) {
	w := newWorld(t)
	g, err := w.svc.PrepareOnboarding(w.ctx, w.as("accountant"), purchase.OnboardingInput{Name: "Prep", BankAccount: "AE1", TradeLicenceKey: "TL"})
	w.must(err)
	if err := w.svc.Delegate(w.ctx, w.as("cfo"), g.ID, "partner", time.Now().Add(time.Hour), g.StateVersion); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("delegation: %v", err)
	}
	for _, id := range []string{"accountant", "finance", "cto", "approver", "auditor", "sys"} {
		_, err := w.svc.Approve(w.ctx, w.as(id), g.ID, g.StateVersion, "")
		if w.code(err) != apierr.PermissionDenied {
			t.Fatalf("%s approved: %v", id, err)
		}
	}
	hole, err := w.svc.PrepareOnboarding(w.ctx, w.as("accountant"), purchase.OnboardingInput{Name: "Hole", BankAccount: "AE2", TradeLicenceKey: "TL2"})
	w.must(err)
	if _, err := w.appr.Delegate(w.ctx, w.as("cfo"), hole.ApprovalID, "clerk", time.Now().Add(time.Hour), hole.StateVersion); err != nil {
		t.Fatal(err)
	}
	delegated, err := w.appr.Approve(w.ctx, w.as("clerk"), approvals.DecisionInput{RequestID: hole.ApprovalID, StateVersion: hole.StateVersion + 1})
	w.must(err)
	if _, err := w.svc.Register(w.ctx, w.as("cfo"), hole.ID, delegated.PostingToken, map[string]any{"status": "approved"}); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("delegated registration: %v", err)
	}
	if w.vendorCount() != 0 {
		t.Fatal("delegated approval stored a vendor")
	}
	v := w.finish(g, "partner", "")
	if v.Name != "Prep" {
		t.Fatalf("vendor %s", v.Name)
	}
	raw := w.sku("raw_material")
	add, err := w.svc.PrepareSKUAdd(w.ctx, w.as("finance"), v.ID, raw)
	w.must(err)
	if _, err := w.svc.Approve(w.ctx, w.as("accountant"), add.ID, add.StateVersion, ""); w.code(err) != apierr.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(w.ctx, w.as("finance"), add.ID, add.StateVersion, ""); w.code(err) != apierr.PermissionDenied {
		t.Fatal(err)
	}
	if err := w.svc.Delegate(w.ctx, w.as("partner"), add.ID, "finance", time.Now().Add(time.Hour), add.StateVersion); w.code(err) != apierr.PermissionDenied {
		t.Fatal(err)
	}
	w.finish(add, "cfo", "")
}

func TestVND2_RawMaterialRequiresSKUApproval(t *testing.T) {
	w := newWorld(t)
	v := w.onboard("General")
	raw := w.sku("raw_material")
	for _, call := range []func(purchase.DocInput) (purchase.Document, error){
		func(in purchase.DocInput) (purchase.Document, error) {
			return w.svc.CreateRequisition(w.ctx, w.as("accountant"), in)
		},
		func(in purchase.DocInput) (purchase.Document, error) {
			return w.svc.CreateQuote(w.ctx, w.as("accountant"), in)
		},
		func(in purchase.DocInput) (purchase.Document, error) {
			return w.svc.CreateLPO(w.ctx, w.as("accountant"), in)
		},
		func(in purchase.DocInput) (purchase.Document, error) {
			return w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), in)
		},
	} {
		_, err := call(purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "2")})
		if w.code(err) != apierr.ValidationError {
			t.Fatalf("raw line accepted: %v", err)
		}
	}
	w.addSKU(v.ID, raw)
	if _, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "2")}); err != nil {
		t.Fatal(err)
	}
}

func TestVND3_BlacklistBlocksLPOInvoiceAndPayment(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Hold", raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "2", "5")})
	w.must(err)
	if _, err := w.svc.ReceiveGoods(w.ctx, w.as("clerk"), lpo.ID, w.lines(raw, "2", "5")); err != nil {
		t.Fatal(err)
	}
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "S-1", Lines: w.lines(raw, "2", "5"),
	})
	w.must(err)
	posted, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), inv.ID, false)
	w.must(err)
	if posted.Status != "posted" || posted.LedgerPosted {
		t.Fatalf("post %+v", posted)
	}
	w.blacklist(v.ID)
	if _, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "1")}); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("lpo: %v", err)
	}
	if _, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, SupplierNumber: "S-2", Lines: w.lines(raw, "1", "1")}); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("invoice: %v", err)
	}
	started, err := w.svc.StartPayment(w.ctx, w.as("accountant"), posted.ID)
	w.must(err)
	if _, err := w.svc.Pay(w.ctx, w.as("clerk"), started.Document.ID); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("pay before release: %v", err)
	}
	out, err := w.svc.Approve(w.ctx, w.as("cfo"), started.Gate.ID, started.Gate.StateVersion, "stepup.payment_release.cfo")
	w.must(err)
	if _, err := w.svc.Register(w.ctx, w.as("cfo"), started.Gate.ID, out.PostingToken, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Pay(w.ctx, w.as("accountant"), started.Document.ID); w.code(err) != apierr.SoDViolation {
		t.Fatalf("preparer pay: %v", err)
	}
	paid, err := w.svc.Pay(w.ctx, w.as("clerk"), started.Document.ID)
	w.must(err)
	if paid.Status != "paid" {
		t.Fatalf("paid %s", paid.Status)
	}
}

func TestVND4_BestPriceExcludesUnapprovedAndBlacklisted(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	good := w.onboard("Good", raw)
	bad := w.onboard("Black", raw)
	onlyGeneral := w.onboard("General")
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
	draft := window
	draft.VendorID = good.ID
	draft.Lines = w.lines(raw, "1", "1")
	if _, err := w.svc.CreateQuote(w.ctx, w.as("accountant"), draft); err != nil {
		t.Fatal(err)
	}
	norate := window
	norate.VendorID = good.ID
	norate.Currency = "USD"
	norate.Lines = w.lines(raw, "1", "1")
	unranked, err := w.svc.CreatePriceAgreement(w.ctx, w.as("accountant"), norate)
	w.must(err)
	if _, err := w.svc.ApproveCommercial(w.ctx, w.as("accountant"), unranked.ID); err != nil {
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
	dash, err := w.svc.Dashboard(w.ctx, w.as("clerk"), &raw)
	w.must(err)
	if dash.Best == nil || dash.Best.DocumentID != best.ID || dash.Best.Currency != "USD" || dash.Best.UnitPrice == "" || dash.Best.Number == "" {
		t.Fatalf("best %+v", dash.Best)
	}
	if dash.Best.AED != "36.7000" {
		t.Fatalf("aed %s", dash.Best.AED)
	}
	if dash.Best.VendorID == bad.ID || dash.Best.VendorID == onlyGeneral.ID {
		t.Fatal("blocked vendor ranked")
	}
	reasons := map[string]bool{}
	for _, b := range dash.Blocked {
		reasons[b.Reason] = true
		if b.VendorID == good.ID && b.Reason == "blacklisted" {
			t.Fatal("approved vendor marked blacklisted")
		}
	}
	for _, reason := range []string{"blacklisted", "not_approved_for_sku", "quote_not_approved"} {
		if !reasons[reason] {
			t.Fatalf("missing blocked reason %s in %+v", reason, dash.Blocked)
		}
	}
	if len(dash.ShownUnranked) != 1 || dash.ShownUnranked[0].DocumentID != unranked.ID {
		t.Fatalf("unranked %+v", dash.ShownUnranked)
	}
}

func TestVND5_FinishedGoodsUsesGeneralApproval(t *testing.T) {
	w := newWorld(t)
	v := w.onboard("Mill")
	fg := w.sku("finished_goods")
	both := w.sku("both")
	if _, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(fg, "1", "3")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.CreateRequisition(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(both, "1", "3")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.GetVendor(w.ctx, w.as("accountant"), v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestVND6_ReviewShowsLicenceBankSKUsAndPriorInvoices(t *testing.T) {
	w := newWorld(t)
	g, err := w.svc.PrepareOnboarding(w.ctx, w.as("accountant"), purchase.OnboardingInput{
		Name: "New", BankAccount: "AE99", TradeLicenceKey: "TL-9", SKUIDs: []uuid.UUID{uuid.New()},
	})
	w.must(err)
	rev, err := w.svc.Review(w.ctx, w.as("cfo"), g.ID)
	w.must(err)
	if rev.TradeLicenceKey != "TL-9" || rev.BankAccount != "AE99" || len(rev.SKUIDs) != 1 || len(rev.PriorInvoices) != 0 {
		t.Fatalf("review %+v", rev)
	}
	raw, _ := json.Marshal(rev)
	if !bytes.Contains(raw, []byte(`"prior_invoices":[]`)) {
		t.Fatalf("empty list hidden: %s", raw)
	}
	v := w.finish(g, "partner", "")
	rawSKU := w.sku("raw_material")
	w.addSKU(v.ID, rawSKU)
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, SupplierNumber: "OLD", Lines: w.lines(rawSKU, "1", "1")})
	w.must(err)
	add, err := w.svc.PrepareSKUAdd(w.ctx, w.as("accountant"), v.ID, w.sku("raw_material"))
	w.must(err)
	again, err := w.svc.Review(w.ctx, w.as("partner"), add.ID)
	w.must(err)
	if again.BankAccount != "AE99" || again.TradeLicenceKey != "TL-9" || len(again.PriorInvoices) != 1 || again.PriorInvoices[0].ID != inv.ID {
		t.Fatalf("sku review %+v", again)
	}
}

func TestVND7_BankChangeFourEyesAndStepUp(t *testing.T) {
	w := newWorld(t)
	v := w.onboard("Banked")
	fg := w.sku("finished_goods")
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(fg, "1", "4")})
	w.must(err)
	g, err := w.svc.ProposeBank(w.ctx, w.as("accountant"), v.ID, "AE-NEW")
	w.must(err)
	if err := w.svc.ApproveBank(w.ctx, w.as("accountant"), g.ID, g.StateVersion, "stepup.bank_change.accountant"); w.code(err) != apierr.SoDViolation {
		t.Fatalf("self approval: %v", err)
	}
	if err := w.svc.ApproveBank(w.ctx, w.as("reviewer"), g.ID, g.StateVersion, ""); w.code(err) != apierr.StepUpRequired {
		t.Fatalf("step-up: %v", err)
	}
	w.must(w.svc.ApproveBank(w.ctx, w.as("reviewer"), g.ID, g.StateVersion, "stepup.bank_change.reviewer"))
	got, err := w.svc.GetVendor(w.ctx, w.as("accountant"), v.ID)
	w.must(err)
	if got.BankAccount != "AE-NEW" {
		t.Fatalf("bank %s", got.BankAccount)
	}
	kept, err := w.svc.GetDocument(w.ctx, w.as("accountant"), lpo.ID)
	w.must(err)
	if kept.BankAccount != "AE070331234567890123456" {
		t.Fatalf("old bank rewritten to %s", kept.BankAccount)
	}
}

func TestVND8_DirectApprovedInsertRefused(t *testing.T) {
	w := newWorld(t)
	if w.code(w.svc.RefuseDirectVendor(w.ctx, w.as("cfo"))) != apierr.PermissionDenied {
		t.Fatal("service accepted a direct vendor")
	}
	err := rls.Tx(w.ctx, w.db.App, w.as("cfo"), func(tx pgx.Tx) error {
		_, err := tx.Exec(w.ctx, `INSERT INTO erp.purchase_vendors(id, company_id, name, status, prepared_by, approval_request_id)
			VALUES ($1,$2,'Direct','approved','cfo','no-request')`, uuid.New(), w.company)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "skipped the approval") {
		t.Fatalf("direct insert: %v", err)
	}
	if w.vendorCount() != 0 {
		t.Fatal("direct insert stored a vendor")
	}
}

func TestVND9_ClientDecisionAndTokenIgnoredAndRLS(t *testing.T) {
	w := newWorld(t)
	g, err := w.svc.PrepareOnboarding(w.ctx, w.as("accountant"), purchase.OnboardingInput{Name: "Real", BankAccount: "AE1", TradeLicenceKey: "TL"})
	w.must(err)
	out, err := w.svc.Approve(w.ctx, w.as("cfo"), g.ID, g.StateVersion, "")
	w.must(err)
	if _, err := w.svc.Register(w.ctx, w.as("cfo"), g.ID, "client-token", map[string]any{"status": "approved", "decision": "approved"}); err == nil {
		t.Fatal("client token approved the vendor")
	}
	got, err := w.svc.Register(w.ctx, w.as("accountant"), g.ID, out.PostingToken, map[string]any{"name": "Rewritten", "status": "approved"})
	w.must(err)
	v := got.(purchase.Vendor)
	if v.Name != "Real" || v.BankAccount != "AE1" {
		t.Fatalf("client fields applied: %+v", v)
	}
	other := rls.Principal{UserID: "stranger", CompanyID: uuid.New(), Roles: []string{"cfo"}}
	if _, err := w.svc.GetVendor(w.ctx, other, v.ID); w.code(err) != apierr.NotFound {
		t.Fatalf("rls: %v", err)
	}
}

func TestVND10_BlacklistKeepsOpenDocuments(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Open", raw)
	in := purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "3", "4")}
	req, err := w.svc.CreateRequisition(w.ctx, w.as("accountant"), in)
	w.must(err)
	q, err := w.svc.CreateQuote(w.ctx, w.as("accountant"), in)
	w.must(err)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), in)
	w.must(err)
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, SupplierNumber: "OPEN", Lines: w.lines(raw, "3", "4")})
	w.must(err)
	before := []purchase.Document{req, q, lpo, inv}
	w.blacklist(v.ID)
	for _, doc := range before {
		got, err := w.svc.GetDocument(w.ctx, w.as("accountant"), doc.ID)
		w.must(err)
		if got.Number != doc.Number || got.Status != doc.Status || got.Amount != doc.Amount || len(got.Lines) != 1 || got.Lines[0].Qty != doc.Lines[0].Qty {
			t.Fatalf("rewritten %+v -> %+v", doc, got)
		}
	}
}

func TestVND11_PostedInvoiceStaysPostedAndFlagged(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Hist", raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "8")})
	w.must(err)
	w.must(receive(w, lpo.ID, raw, "1", "8"))
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "HIST", Lines: w.lines(raw, "1", "8"),
	})
	w.must(err)
	posted, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), inv.ID, false)
	w.must(err)
	w.blacklist(v.ID)
	got, err := w.svc.GetDocument(w.ctx, w.as("accountant"), posted.ID)
	w.must(err)
	if got.Status != "posted" || !got.Flagged || got.LedgerPosted {
		t.Fatalf("document %+v", got)
	}
	list, err := w.svc.ListPayables(w.ctx, w.as("clerk"))
	w.must(err)
	if len(list) != 1 || !list[0].Flagged || list[0].Status != "posted" {
		t.Fatalf("payables %+v", list)
	}
}

func TestVND12_ReleaseDoesNotClearBlacklistOrFlag(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Flagged", raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "2")})
	w.must(err)
	w.must(receive(w, lpo.ID, raw, "1", "2"))
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "F1", Lines: w.lines(raw, "1", "2"),
	})
	w.must(err)
	posted, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), inv.ID, false)
	w.must(err)
	w.blacklist(v.ID)
	started, err := w.svc.StartPayment(w.ctx, w.as("clerk"), posted.ID)
	w.must(err)
	out, err := w.svc.Approve(w.ctx, w.as("partner"), started.Gate.ID, started.Gate.StateVersion, "stepup.payment_release.partner")
	w.must(err)
	if _, err := w.svc.Register(w.ctx, w.as("partner"), started.Gate.ID, out.PostingToken, nil); err != nil {
		t.Fatal(err)
	}
	still, err := w.svc.GetVendor(w.ctx, w.as("cfo"), v.ID)
	w.must(err)
	flagged, err := w.svc.GetDocument(w.ctx, w.as("cfo"), posted.ID)
	w.must(err)
	if !still.Blacklisted || !flagged.Flagged || flagged.Status != "posted" {
		t.Fatalf("vendor %+v invoice %+v", still, flagged)
	}
	second, err := w.svc.StartPayment(w.ctx, w.as("clerk"), posted.ID)
	w.must(err)
	if _, err := w.svc.Pay(w.ctx, w.as("clerk"), second.Document.ID); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("second payment: %v", err)
	}
}

func TestVND13_OtherRolesCannotBlacklistOrRelease(t *testing.T) {
	w := newWorld(t)
	v := w.onboard("Guarded")
	g, err := w.svc.PrepareBlacklist(w.ctx, w.as("accountant"), v.ID, "fraud")
	w.must(err)
	for _, id := range []string{"accountant", "finance", "cto", "approver", "auditor", "sys"} {
		if _, err := w.svc.Approve(w.ctx, w.as(id), g.ID, g.StateVersion, "stepup.payment_release."+id); w.code(err) != apierr.PermissionDenied {
			t.Fatalf("%s blacklisted: %v", id, err)
		}
	}
	raw := w.sku("raw_material")
	w.addSKU(v.ID, raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "1", "2")})
	w.must(err)
	w.must(receive(w, lpo.ID, raw, "1", "2"))
	inv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "R", Lines: w.lines(raw, "1", "2"),
	})
	w.must(err)
	posted, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), inv.ID, false)
	w.must(err)
	w.blacklist(v.ID)
	started, err := w.svc.StartPayment(w.ctx, w.as("clerk"), posted.ID)
	w.must(err)
	if _, err := w.svc.Approve(w.ctx, w.as("finance"), started.Gate.ID, started.Gate.StateVersion, "stepup.payment_release.finance"); w.code(err) != apierr.PermissionDenied {
		t.Fatalf("release: %v", err)
	}
}

func TestVND15_ActionsOnlyForCFOAndPartner(t *testing.T) {
	w := newWorld(t)
	for _, id := range []string{"accountant", "finance", "cto", "approver", "auditor", "sys", "clerk"} {
		dash, err := w.svc.Dashboard(w.ctx, w.as(id), nil)
		w.must(err)
		if len(dash.Actions) != 0 {
			t.Fatalf("%s actions %v", id, dash.Actions)
		}
	}
	for _, id := range []string{"cfo", "partner"} {
		dash, err := w.svc.Dashboard(w.ctx, w.as(id), nil)
		w.must(err)
		if strings.Join(dash.Actions, ",") != "approve,blacklist" {
			t.Fatalf("%s actions %v", id, dash.Actions)
		}
	}
	v := w.onboard("Acts")
	g, err := w.svc.PrepareBlacklist(w.ctx, w.as("accountant"), v.ID, "fraud")
	w.must(err)
	if _, err := w.svc.Approve(w.ctx, w.as("finance"), g.ID, g.StateVersion, ""); w.code(err) != apierr.PermissionDenied {
		t.Fatal(err)
	}
}

func TestVND16_ActiveAndPastCounts(t *testing.T) {
	w := newWorld(t)
	a := w.sku("raw_material")
	b := w.sku("raw_material")
	v := w.onboard("Counted", a, b)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: []purchase.LineInput{
		{SKUID: a, Qty: "1", UnitPrice: "2"}, {SKUID: b, Qty: "1", UnitPrice: "3"},
	}})
	w.must(err)
	if _, err := w.svc.ReceiveGoods(w.ctx, w.as("clerk"), lpo.ID, []purchase.LineInput{
		{SKUID: a, Qty: "1", UnitPrice: "2"}, {SKUID: b, Qty: "1", UnitPrice: "3"},
	}); err != nil {
		t.Fatal(err)
	}
	openInv, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SupplierNumber: "OPEN-1", Lines: w.lines(a, "1", "2"),
	})
	w.must(err)
	posted, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "PAST-1",
		Lines: []purchase.LineInput{{SKUID: a, Qty: "1", UnitPrice: "2"}, {SKUID: b, Qty: "1", UnitPrice: "3"}},
	})
	w.must(err)
	if _, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), posted.ID, false); err != nil {
		t.Fatal(err)
	}
	dash, err := w.svc.Dashboard(w.ctx, w.as("auditor"), &a)
	w.must(err)
	if len(dash.Vendors) != 1 || dash.Vendors[0].ActiveInvoices != 1 || dash.Vendors[0].PastInvoices != 1 {
		t.Fatalf("counts %+v", dash.Vendors)
	}
	got := map[uuid.UUID]purchase.SKUCount{}
	for _, c := range dash.Vendors[0].SKUs {
		got[c.SKUID] = c
	}
	if got[a].Active != 1 || got[a].Past != 1 || got[b].Past != 1 || got[b].Active != 0 {
		t.Fatalf("sku counts %+v open %s", got, openInv.ID)
	}
}

func TestVND17_SKUFilterOmitsGenerallyApprovedOnly(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	approved := w.onboard("Linked", raw)
	general := w.onboard("OnlyGeneral")
	dash, err := w.svc.Dashboard(w.ctx, w.as("finance"), &raw)
	w.must(err)
	seen := map[uuid.UUID]bool{}
	for _, v := range dash.Vendors {
		seen[v.VendorID] = true
	}
	if !seen[approved.ID] || seen[general.ID] {
		t.Fatalf("vendors %+v", dash.Vendors)
	}
}

func TestThreeWayAndDuplicateAndPostingGap(t *testing.T) {
	w := newWorld(t)
	raw := w.sku("raw_material")
	v := w.onboard("Match", raw)
	lpo, err := w.svc.CreateLPO(w.ctx, w.as("accountant"), purchase.DocInput{VendorID: v.ID, Lines: w.lines(raw, "10", "5")})
	w.must(err)
	receipt, err := w.svc.ReceiveGoods(w.ctx, w.as("clerk"), lpo.ID, w.lines(raw, "10", "5"))
	w.must(err)
	if receipt.LedgerPosted || !strings.Contains(receipt.PostingGap, "ledger") {
		t.Fatalf("receipt gap %+v", receipt)
	}
	over, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "OVER", Lines: w.lines(raw, "11", "5"),
	})
	w.must(err)
	blocked, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), over.ID, false)
	if w.code(err) != apierr.Conflict || blocked.Status != "blocked" || blocked.LedgerPosted {
		t.Fatalf("over tolerance %+v %v", blocked, err)
	}
	first, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "DUP-1", Lines: w.lines(raw, "10", "5"),
	})
	w.must(err)
	posted, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), first.ID, false)
	w.must(err)
	if posted.Status != "posted" || posted.LedgerPosted || !strings.Contains(posted.PostingGap, "ledger") {
		t.Fatalf("posted %+v", posted)
	}
	dupN, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "DUP-1", Lines: w.lines(raw, "10", "5"),
	})
	w.must(err)
	if _, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), dupN.ID, false); w.code(err) != apierr.Conflict {
		t.Fatalf("duplicate number: %v", err)
	}
	sameAmt, err := w.svc.CreateSupplierInvoice(w.ctx, w.as("accountant"), purchase.DocInput{
		VendorID: v.ID, SourceID: &lpo.ID, SupplierNumber: "DUP-2", Lines: w.lines(raw, "10", "5"),
	})
	w.must(err)
	if _, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), sameAmt.ID, false); w.code(err) != apierr.Conflict {
		t.Fatalf("duplicate amount: %v", err)
	}
	confirmed, err := w.svc.PostInvoice(w.ctx, w.as("accountant"), sameAmt.ID, true)
	w.must(err)
	if confirmed.Status != "posted" || confirmed.LedgerPosted {
		t.Fatalf("confirmed %+v", confirmed)
	}
	var journal *string
	err = rls.Tx(w.ctx, w.db.App, w.as("accountant"), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT to_regclass('erp.journal_lines')::text`).Scan(&journal)
	})
	w.must(err)
	if journal != nil {
		t.Fatalf("ledger journal exists: %s", *journal)
	}
}

func TestHTTP_RequestIDAndRefusedVendorInsert(t *testing.T) {
	w := newWorld(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := chi.NewRouter()
	r.Use(apierr.RequestIDMiddleware)
	purchase.Handler{Svc: w.svc, Log: log}.Routes(r)
	req := httptest.NewRequest(http.MethodPost, "/purchase/vendors", strings.NewReader(`{"status":"approved","name":"Skip"}`))
	req.Header.Set("X-Request-ID", "req-vnd8")
	req = req.WithContext(rls.WithPrincipal(req.Context(), w.as("cfo")))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") != "req-vnd8" || !strings.Contains(rec.Body.String(), `"request_id":"req-vnd8"`) {
		t.Fatalf("header %s body %s", rec.Header().Get("X-Request-ID"), rec.Body.String())
	}
	if !strings.Contains(buf.String(), `"request_id":"req-vnd8"`) {
		t.Fatalf("log %s", buf.String())
	}
	buf.Reset()
	raw := w.sku("raw_material")
	v := w.onboard("Dash")
	w.addSKU(v.ID, raw)
	dashReq := httptest.NewRequest(http.MethodGet, "/purchase/dashboard?sku="+url.QueryEscape(raw.String()), nil)
	dashReq.Header.Set("X-Request-ID", "req-vnd17")
	dashReq = dashReq.WithContext(rls.WithPrincipal(dashReq.Context(), w.as("clerk")))
	dashRec := httptest.NewRecorder()
	r.ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK || !strings.Contains(dashRec.Body.String(), v.ID.String()) || !strings.Contains(buf.String(), "req-vnd17") {
		t.Fatalf("dashboard %d %s log %s", dashRec.Code, dashRec.Body.String(), buf.String())
	}
}

func receive(w *world, lpo, sku uuid.UUID, qty, price string) error {
	_, err := w.svc.ReceiveGoods(w.ctx, w.as("clerk"), lpo, w.lines(sku, qty, price))
	return err
}

func migrateDB(t *testing.T, db *testdb.DB) {
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

func withDB(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(name, "/")
	return u.String()
}
