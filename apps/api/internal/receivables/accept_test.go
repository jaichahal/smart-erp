package receivables_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
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
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/bank"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/receivables"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

// These cases are the receivables rules in 08 (R1-R16) plus the collection rule
// that an unallocated receipt does not clear an invoice. Bank match is a later step.

func TestR1_OpenAndPDCCoveredSumToBalance(t *testing.T) {
	h := newH(t)
	cust := h.customer("Al Noor", "10000.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	plain := h.invoice(cust, "INV-1", "2026-09-01", "2026-09-20", "1000.00")
	covered := h.invoice(cust, "INV-2", "2026-09-01", "2026-09-20", "1000.00")
	h.receipt(map[string]any{
		"customer_id": cust, "method": "pdc", "amount": money("400.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
		"allocations": []any{alloc(covered, "400.00")},
		"cheque":      cheque("1001", "ENBD", "2026-10-01"),
	})
	rows := h.list("/api/v1/receivables?as_of=2026-09-20")
	var seenPlain, seenCovered bool
	for _, row := range rows {
		switch row["number"] {
		case "INV-1":
			seenPlain = true
			if moneyOf(t, row["open_amount"]) != "1000.00" || moneyOf(t, row["pdc_covered"]) != "0.00" {
				t.Fatalf("plain invoice split: %v", row)
			}
		case "INV-2":
			seenCovered = true
			open := moneyOf(t, row["open_amount"])
			pdc := moneyOf(t, row["pdc_covered"])
			bal := moneyOf(t, row["balance"])
			if open != "600.00" || pdc != "400.00" || bal != "1000.00" {
				t.Fatalf("pdc split open=%s pdc=%s balance=%s", open, pdc, bal)
			}
		}
	}
	if !seenPlain || !seenCovered {
		t.Fatalf("missing rows plain=%v covered=%v", seenPlain, seenCovered)
	}
	only := h.list("/api/v1/receivables?as_of=2026-09-20&pdc_covered=1")
	if len(only) != 1 || only[0]["number"] != "INV-2" {
		t.Fatalf("pdc filter: %v", only)
	}
	_ = plain
}

func TestR2_AgingUsesDueDate(t *testing.T) {
	h := newH(t)
	cust := h.customer("Aging Co", "0.00", nil, false, 0)
	// Old invoice that is not yet due stays current. A same-day invoice already due is aged by the due date.
	h.invoice(cust, "OLD-CURRENT", "2026-06-01", "2026-09-25", "10.00")
	h.invoice(cust, "NEW-AGED", "2026-09-25", "2026-08-11", "10.00")
	current := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-25&bucket=current"))
	aged := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-25&bucket=31_60"))
	if _, ok := current["OLD-CURRENT"]; !ok {
		t.Fatalf("due date not used for current: %v", current)
	}
	if _, ok := current["NEW-AGED"]; ok {
		t.Fatal("invoice date was used instead of due date")
	}
	if row, ok := aged["NEW-AGED"]; !ok || row["bucket"] != "31_60" {
		t.Fatalf("45 days past due: %v", aged)
	}
}

func TestR3_AllocationLeavesOnAccountCredit(t *testing.T) {
	h := newH(t)
	cust := h.customer("Split Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	a := h.invoice(cust, "A", "2026-09-01", "2026-09-30", "60.00")
	b := h.invoice(cust, "B", "2026-09-01", "2026-09-30", "40.00")
	got := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("100.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
		"allocations": []any{alloc(a, "60.00"), alloc(b, "25.00")},
	})
	if moneyOf(t, got["on_account"]) != "15.00" {
		t.Fatalf("on account: %v", got["on_account"])
	}
	if moneyOf(t, got["customer_credit"]) != "15.00" {
		t.Fatalf("customer credit: %v", got["customer_credit"])
	}
	rows := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-20"))
	if moneyOf(t, rows["A"]["open_amount"]) != "0.00" || moneyOf(t, rows["B"]["open_amount"]) != "15.00" {
		t.Fatalf("allocated invoices: %v %v", rows["A"], rows["B"])
	}
}

func TestR4_AdvanceAppliesToLaterInvoice(t *testing.T) {
	h := newH(t)
	cust := h.customer("Advance Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	adv := h.receipt(map[string]any{
		"customer_id": cust, "method": "advance", "amount": money("80.00"),
		"posted_on": "2026-09-01", "bank_account_id": bankID,
	})
	inv := h.invoice(cust, "LATER", "2026-09-10", "2026-10-10", "100.00")
	applied := h.post("/api/v1/advances/apply", map[string]any{
		"customer_id": cust, "invoice_id": inv, "receipt_id": adv["id"], "amount": money("80.00"),
	}, "")
	if moneyOf(t, applied["open_amount"]) != "20.00" {
		t.Fatalf("advance did not reduce receivable: %v", applied)
	}
	roles := rolesOf(t, applied["postings"])
	if roles["bank"] != 0 || roles["customer_advances"] <= 0 || roles["receivable"] >= 0 {
		t.Fatalf("apply must debit advances and credit receivable: %v", applied["postings"])
	}
}

func TestR5_PDCPostsToHandThenBankThenReverses(t *testing.T) {
	h := newH(t)
	cust := h.customer("PDC Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	inv := h.invoice(cust, "PDC-INV", "2026-09-01", "2026-09-30", "1000.00")
	before := h.accountView(bankID)
	rec := h.receipt(map[string]any{
		"customer_id": cust, "method": "pdc", "amount": money("1000.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
		"allocations": []any{alloc(inv, "1000.00")},
		"cheque":      cheque("555", "FAB", "2026-10-01"),
	})
	if moneyOf(t, h.accountView(bankID)["balance"]) != moneyOf(t, before["balance"]) {
		t.Fatal("PDC receipt increased the bank")
	}
	if moneyOf(t, rec["pdc_in_balance"]) != "1000.00" {
		t.Fatalf("PDC in hand: %v", rec["pdc_in_balance"])
	}
	pdcID := strOf(t, rec["pdc_id"])
	dep := h.post("/api/v1/pdcs/"+pdcID+"/deposit", map[string]any{}, strOf(t, rec["pdc_state_version"]))
	if moneyOf(t, dep["bank_balance"]) != "1000.00" || moneyOf(t, dep["pdc_in_balance"]) != "0.00" {
		t.Fatalf("deposit: %v", dep)
	}
	bounced := h.post("/api/v1/pdcs/"+pdcID+"/bounce", map[string]any{"reason": "returned by bank"}, strOf(t, dep["state_version"]))
	if moneyOf(t, bounced["bank_balance"]) != "0.00" {
		t.Fatalf("bounce left cash in the bank: %v", bounced["bank_balance"])
	}
	row := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-10-02"))["PDC-INV"]
	if moneyOf(t, row["open_amount"]) != "1000.00" || row["bounced"] != true {
		t.Fatalf("invoice not reopened and flagged: %v", row)
	}
}

func TestR6_IssuedPDCIsLiabilityUntilPresented(t *testing.T) {
	h := newH(t)
	bankID := h.account("bank", "500.00", "2026-09-01", "")
	issued := h.post("/api/v1/pdcs/issued", map[string]any{
		"amount": money("250.00"), "posted_on": "2026-09-20", "bank_account_id": bankID,
		"cheque": cheque("900", "ADCB", "2026-10-15"),
	}, "")
	if moneyOf(t, issued["pdc_out_liability"]) != "250.00" {
		t.Fatalf("liability: %v", issued)
	}
	if moneyOf(t, issued["bank_balance"]) != "500.00" {
		t.Fatal("issuing a PDC moved the bank")
	}
	presented := h.post("/api/v1/pdcs/"+strOf(t, issued["id"])+"/present", map[string]any{}, strOf(t, issued["state_version"]))
	if moneyOf(t, presented["pdc_out_liability"]) != "0.00" || moneyOf(t, presented["bank_balance"]) != "250.00" {
		t.Fatalf("presentation: %v", presented)
	}
}

func TestR7_CashReceiptShowsOnCollectorDashboardWithinOneSecond(t *testing.T) {
	h := newH(t)
	cust := h.customer("Walk in", "0.00", nil, false, 0)
	cashID := h.account("cash", "0.00", "2026-09-01", "")
	start := time.Now()
	rec := h.receipt(map[string]any{
		"customer_id": cust, "method": "cash", "amount": money("25.00"),
		"collector_id": "collector-7", "posted_on": "2026-09-25", "cash_account_id": cashID,
	})
	dash := h.get("/api/v1/collectors/collector-7/dashboard?date=2026-09-25")
	if time.Since(start) >= time.Second {
		t.Fatalf("dashboard lag %s", time.Since(start))
	}
	found := false
	for _, item := range sliceOf(t, dash["receipts"]) {
		if mapOf(t, item)["id"] == rec["id"] {
			found = true
		}
	}
	if !found {
		t.Fatalf("dashboard: %v", dash)
	}
}

func TestR8_ChequeReceiptRequiresNumberBankAndDate(t *testing.T) {
	h := newH(t)
	cust := h.customer("Cheque Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	code := h.postStatus("/api/v1/receipts", map[string]any{
		"customer_id": cust, "method": "current_cheque", "amount": money("10.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
	}, "")
	if code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
}

func TestR9_UnallocatedReceiptStaysInAccountantQueue(t *testing.T) {
	h := newH(t)
	cust := h.customer("Queue Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	inv := h.invoice(cust, "STILL-OPEN", "2026-09-01", "2026-09-30", "50.00")
	rec := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("50.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
	})
	queued := h.get("/api/v1/receipts/unallocated")
	found := false
	for _, item := range sliceOf(t, queued["receipts"]) {
		if mapOf(t, item)["id"] == rec["id"] {
			found = true
		}
	}
	if !found {
		t.Fatalf("queue: %v", queued)
	}
	row := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-20"))["STILL-OPEN"]
	if moneyOf(t, row["open_amount"]) != "50.00" {
		t.Fatalf("unallocated receipt cleared the invoice: %v", row)
	}
	_ = inv
}

func TestR10_AllocationCannotExceedInvoiceBalance(t *testing.T) {
	h := newH(t)
	cust := h.customer("Cap Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	inv := h.invoice(cust, "CAP", "2026-09-01", "2026-09-30", "100.00")
	code := h.postStatus("/api/v1/receipts", map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("150.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
		"allocations": []any{alloc(inv, "150.00")},
	}, "")
	if code != http.StatusBadRequest && code != http.StatusConflict {
		t.Fatalf("status %d", code)
	}
	row := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-20"))["CAP"]
	if moneyOf(t, row["open_amount"]) != "100.00" {
		t.Fatalf("open changed: %v", row)
	}
}

func TestR11_StatementMatchesPartyLedger(t *testing.T) {
	h := newH(t)
	cust := h.customer("Statement Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	inv := h.invoice(cust, "S-1", "2026-09-01", "2026-09-30", "100.00")
	h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("40.00"),
		"posted_on": "2026-09-05", "bank_account_id": bankID,
		"allocations": []any{alloc(inv, "40.00")},
	})
	h.post("/api/v1/receivables/credits", map[string]any{
		"customer_id": cust, "invoice_id": inv, "number": "CN-1", "posted_on": "2026-09-06",
		"amount": money("10.00"),
	}, "")
	stmt := h.get("/api/v1/customers/" + cust + "/statement?from=2026-09-01&to=2026-09-30")
	ledger := h.get("/api/v1/customers/" + cust + "/ledger?from=2026-09-01&to=2026-09-30")
	if moneyOf(t, stmt["running_balance"]) != "50.00" || moneyOf(t, ledger["running_balance"]) != "50.00" {
		t.Fatalf("balances stmt=%v ledger=%v", stmt["running_balance"], ledger["running_balance"])
	}
	if lineText(t, stmt["lines"]) != lineText(t, ledger["lines"]) {
		t.Fatalf("statement %s ledger %s", lineText(t, stmt["lines"]), lineText(t, ledger["lines"]))
	}
}

func TestR12_DunningSendsOnConfiguredDaysAndLogs(t *testing.T) {
	h := newH(t)
	cust := h.customer("Dunning Co", "0.00", []any{7, 14}, false, 0)
	inv := h.invoice(cust, "DUN", "2026-09-01", "2026-09-18", "20.00")
	h.post("/api/v1/receivables/dunning/run", map[string]any{"as_of": "2026-09-25"}, "")
	logs := h.get("/api/v1/receivables/invoices/" + inv + "/dunning")
	items := sliceOf(t, logs["logs"])
	if len(items) != 1 || f64Of(t, mapOf(t, items[0])["days"]) != 7 {
		t.Fatalf("dunning log: %v", logs)
	}
	h.post("/api/v1/receivables/dunning/run", map[string]any{"as_of": "2026-09-25"}, "")
	logs = h.get("/api/v1/receivables/invoices/" + inv + "/dunning")
	if len(sliceOf(t, logs["logs"])) != 1 {
		t.Fatalf("duplicate dunning: %v", logs)
	}
	early := h.invoice(cust, "EARLY", "2026-09-20", "2026-09-22", "5.00")
	h.post("/api/v1/receivables/dunning/run", map[string]any{"as_of": "2026-09-25"}, "")
	earlyLogs := h.get("/api/v1/receivables/invoices/" + early + "/dunning")
	if len(sliceOf(t, earlyLogs["logs"])) != 0 {
		t.Fatalf("sent before configured day: %v", earlyLogs)
	}
}

func TestR13_OverdueInterestDraftIsNotPostedUntilIssued(t *testing.T) {
	h := newH(t)
	cust := h.customer("Interest Co", "0.00", nil, true, 1800)
	inv := h.invoice(cust, "INT", "2026-09-01", "2026-09-15", "1000.00")
	draft := h.post("/api/v1/receivables/interest/drafts", map[string]any{
		"customer_id": cust, "invoice_id": inv, "as_of": "2026-09-25",
	}, "")
	if draft["status"] != "draft" || draft["books_posted"] != false || moneyOf(t, draft["amount"]) != "4.93" {
		t.Fatalf("draft: %v", draft)
	}
	issued := h.post("/api/v1/receivables/debit-notes/"+strOf(t, draft["id"])+"/issue", map[string]any{}, strOf(t, draft["state_version"]))
	if issued["status"] != "issued" {
		t.Fatalf("issue: %v", issued)
	}
	roles := rolesOf(t, issued["postings"])
	if roles["receivable"] <= 0 || roles["interest_income"] >= 0 {
		t.Fatalf("interest posting: %v", issued["postings"])
	}
}

func TestR14_CreditReviewQueue(t *testing.T) {
	h := newH(t)
	overLimit := h.customer("Big", "1000.00", nil, false, 0)
	overdue := h.customer("Late", "10000.00", nil, false, 0)
	fine := h.customer("Fine", "1000.00", nil, false, 0)
	h.invoice(overLimit, "BIG", "2026-09-01", "2026-09-30", "850.00")
	h.invoice(overdue, "LATE", "2026-05-01", "2026-06-01", "10.00")
	h.invoice(fine, "FINE", "2026-09-01", "2026-09-30", "500.00")
	q := h.get("/api/v1/receivables/credit-review?as_of=2026-09-25")
	got := map[string]string{}
	for _, item := range sliceOf(t, q["customers"]) {
		row := mapOf(t, item)
		got[strOf(t, row["customer_id"])] = strOf(t, row["reason"])
	}
	if got[overLimit] != "over_limit" || got[overdue] != "over_90" {
		t.Fatalf("queue: %v", got)
	}
	if _, ok := got[fine]; ok {
		t.Fatalf("fine customer queued: %v", got)
	}
}

func TestR15_ConcentrationFlagsShare(t *testing.T) {
	h := newH(t)
	h.post("/api/v1/receivables/settings", map[string]any{"concentration_share": "0.25"}, "")
	big := h.customer("Major", "0.00", nil, false, 0)
	small := h.customer("Minor", "0.00", nil, false, 0)
	h.invoice(big, "MAJ", "2026-09-01", "2026-09-30", "800.00")
	h.invoice(small, "MIN", "2026-09-01", "2026-09-30", "200.00")
	body := h.get("/api/v1/receivables/concentration?as_of=2026-09-25")
	flagged := map[string]bool{}
	for _, item := range sliceOf(t, body["flagged"]) {
		flagged[strOf(t, mapOf(t, item)["customer_id"])] = true
	}
	if !flagged[big] || flagged[small] {
		t.Fatalf("flags: %v", body)
	}
}

func TestR16_StatementShareAttachesPDF(t *testing.T) {
	h := newH(t)
	cust := h.customer("Share Co", "0.00", nil, false, 0)
	h.invoice(cust, "SH", "2026-09-01", "2026-09-30", "15.00")
	for _, channel := range []string{"email", "whatsapp"} {
		sent := h.post("/api/v1/customers/"+cust+"/statement/send", map[string]any{
			"channel": channel, "from": "2026-09-01", "to": "2026-09-30",
		}, "")
		if sent["channel"] != channel {
			t.Fatalf("channel: %v", sent)
		}
		raw, err := base64.StdEncoding.DecodeString(strOf(t, mapOf(t, sent["attachment"])["content_base64"]))
		if err != nil || !bytes.HasPrefix(raw, []byte("%PDF")) {
			t.Fatalf("%s pdf: %v %s", channel, err, raw)
		}
	}
}

func TestSkill_UnallocatedReceiptDoesNotClearInvoice(t *testing.T) {
	h := newH(t)
	cust := h.customer("Open Co", "0.00", nil, false, 0)
	bankID := h.account("bank", "0.00", "2026-09-01", "")
	h.invoice(cust, "UNPAID", "2026-09-01", "2026-09-30", "75.00")
	got := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("75.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
	})
	if moneyOf(t, got["allocated"]) != "0.00" {
		t.Fatalf("allocated: %v", got)
	}
	row := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-20"))["UNPAID"]
	if moneyOf(t, row["balance"]) != "75.00" || moneyOf(t, row["open_amount"]) != "75.00" {
		t.Fatalf("invoice moved: %v", row)
	}
}

func TestSkill_PDCIsNotBankCash(t *testing.T) {
	h := newH(t)
	cust := h.customer("Cheque Hold", "0.00", nil, false, 0)
	bankID := h.account("bank", "100.00", "2026-09-01", "")
	inv := h.invoice(cust, "HOLD", "2026-09-01", "2026-09-30", "40.00")
	got := h.receipt(map[string]any{
		"customer_id": cust, "method": "pdc", "amount": money("40.00"),
		"posted_on": "2026-09-20", "bank_account_id": bankID,
		"allocations": []any{alloc(inv, "40.00")},
		"cheque":      cheque("42", "Mashreq", "2026-10-02"),
	})
	view := h.accountView(bankID)
	if moneyOf(t, view["balance"]) != "100.00" {
		t.Fatalf("bank changed: %v", view)
	}
	if moneyOf(t, got["pdc_in_balance"]) != "40.00" {
		t.Fatalf("pdc in hand: %v", got["pdc_in_balance"])
	}
}

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	p      rls.Principal
	router http.Handler
	logs   *bytes.Buffer
}

func newH(t *testing.T) *harness {
	t.Helper()
	pool := openERPAr(t)
	company := uuid.New()
	p := rls.Principal{UserID: "user-" + company.String(), CompanyID: company, Roles: []string{"Accountant", "Stakeholder"}}
	ctx := rls.WithPrincipal(context.Background(), p)
	if err := insertCompany(ctx, pool, p); err != nil {
		t.Fatalf("company: %v", err)
	}
	deps := httpx.Deps{Pool: pool}
	r := chi.NewRouter()
	logs := &bytes.Buffer{}
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
			next.ServeHTTP(w, r)
		})
	})
	r.Use(apierr.RequestIDMiddleware)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), p)))
		})
	})
	r.Route("/api/v1", func(v1 chi.Router) {
		receivables.Mount(v1, deps)
		bank.Mount(v1, deps)
	})
	return &harness{t: t, pool: pool, p: p, router: r, logs: logs}
}

func insertCompany(ctx context.Context, pool *pgxpool.Pool, p rls.Principal) error {
	return rls.Tx(ctx, pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.companies (id, legal_name) VALUES ($1, $2)`, p.CompanyID, "Receivables Co")
		return err
	})
}

func (h *harness) customer(name, limit string, dunning []any, interest bool, annualBP int) string {
	h.t.Helper()
	body := map[string]any{
		"name": name, "credit_limit": money(limit), "dunning_days": dunning,
		"interest_enabled": interest, "annual_interest_bp": annualBP,
	}
	got := h.post("/api/v1/receivables/customers", body, "")
	return strOf(h.t, got["id"])
}

func (h *harness) invoice(customer, number, invoiceDate, due, amount string) string {
	h.t.Helper()
	got := h.post("/api/v1/receivables/invoices", map[string]any{
		"customer_id": customer, "number": number, "invoice_date": invoiceDate,
		"due_date": due, "amount": money(amount),
	}, "")
	return strOf(h.t, got["id"])
}

func (h *harness) account(kind, opening, openingDate, custodian string) string {
	h.t.Helper()
	got := h.post("/api/v1/bank/accounts", map[string]any{
		"name": kind + " account", "kind": kind, "opening": money(opening),
		"opening_date": openingDate, "custodian_id": custodian, "float": money(opening),
	}, "")
	return strOf(h.t, got["id"])
}

func (h *harness) accountView(id string) map[string]any {
	h.t.Helper()
	return h.get("/api/v1/bank/accounts/" + id)
}

func (h *harness) receipt(body map[string]any) map[string]any {
	h.t.Helper()
	return h.post("/api/v1/receipts", body, "")
}

func (h *harness) list(path string) []map[string]any {
	h.t.Helper()
	body := h.get(path)
	raw, _ := json.Marshal(body["rows"])
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		h.t.Fatalf("rows: %v %s", err, body)
	}
	return rows
}

func (h *harness) post(path string, body any, ifMatch string) map[string]any {
	h.t.Helper()
	rec := h.call(http.MethodPost, path, body, ifMatch)
	if rec.Code >= 300 {
		h.t.Fatalf("%s %d %s", path, rec.Code, rec.Body.String())
	}
	return dataOf(h.t, rec)
}

func (h *harness) postStatus(path string, body any, ifMatch string) int {
	h.t.Helper()
	rec := h.call(http.MethodPost, path, body, ifMatch)
	assertRequestID(h.t, rec, h.logs)
	return rec.Code
}

func (h *harness) get(path string) map[string]any {
	h.t.Helper()
	rec := h.call(http.MethodGet, path, nil, "")
	if rec.Code >= 300 {
		h.t.Fatalf("GET %s %d %s", path, rec.Code, rec.Body.String())
	}
	return dataOf(h.t, rec)
}

func (h *harness) call(method, path string, body any, ifMatch string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	rid := uuid.NewString()
	req.Header.Set("X-Request-ID", rid)
	req.Header.Set(idempotency.Header, uuid.NewString())
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	assertRequestID(h.t, rec, h.logs)
	return rec
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("json: %v %s", err, rec.Body.String())
	}
	if env.Meta.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("meta request id %s", env.Meta.RequestID)
	}
	return env.Data
}

func assertRequestID(t *testing.T, rec *httptest.ResponseRecorder, logs *bytes.Buffer) {
	t.Helper()
	rid := rec.Header().Get("X-Request-ID")
	if rid == "" {
		t.Fatal("missing X-Request-ID")
	}
	if !strings.Contains(logs.String(), rid) {
		t.Fatalf("log missing %s in %s", rid, logs.String())
	}
}

func moneyOf(t testing.TB, v any) string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("money %T", v)
	}
	s, ok := m["amount"].(string)
	if !ok {
		t.Fatalf("amount %v", m["amount"])
	}
	return s
}

func strOf(t testing.TB, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("string %T %v", v, v)
	}
	return s
}

func f64Of(t testing.TB, v any) float64 {
	t.Helper()
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("number %T %v", v, v)
	}
	return n
}

func sliceOf(t testing.TB, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("list %T", v)
	}
	return s
}

func mapOf(t testing.TB, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("object %T", v)
	}
	return m
}

func money(amount string) map[string]string {
	return map[string]string{"amount": amount, "currency": "AED"}
}

func alloc(invoice, amount string) map[string]any {
	return map[string]any{"invoice_id": invoice, "amount": money(amount)}
}

func cheque(number, bankName, date string) map[string]string {
	return map[string]string{"number": number, "bank": bankName, "date": date}
}

func lineText(t *testing.T, raw any) string {
	t.Helper()
	var b strings.Builder
	for _, item := range sliceOf(t, raw) {
		fmtLine(t, &b, mapOf(t, item))
	}
	return b.String()
}

func fmtLine(t *testing.T, b *strings.Builder, row map[string]any) {
	t.Helper()
	b.WriteString(strOf(t, row["date"]))
	b.WriteByte('|')
	b.WriteString(strOf(t, row["kind"]))
	b.WriteByte('|')
	b.WriteString(strOf(t, row["number"]))
	b.WriteByte('|')
	b.WriteString(moneyOf(t, row["debit"]))
	b.WriteByte('|')
	b.WriteString(moneyOf(t, row["credit"]))
	b.WriteByte('|')
	b.WriteString(moneyOf(t, row["running"]))
	b.WriteByte('\n')
}

func indexByNumber(t *testing.T, rows []map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, row := range rows {
		out[strOf(t, row["number"])] = row
	}
	return out
}

func rolesOf(t *testing.T, raw any) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, item := range sliceOf(t, raw) {
		row := mapOf(t, item)
		role := strOf(t, row["role"])
		out[role] += parseFils(moneyOf(t, row["debit"])) - parseFils(moneyOf(t, row["credit"]))
	}
	return out
}

func parseFils(amount string) int64 {
	neg := strings.HasPrefix(amount, "-")
	amount = strings.TrimPrefix(amount, "-")
	parts := strings.Split(amount, ".")
	var whole, frac int64
	for _, c := range parts[0] {
		whole = whole*10 + int64(c-'0')
	}
	if len(parts) == 2 {
		frac = int64(parts[1][0]-'0')*10 + int64(parts[1][1]-'0')
	}
	v := whole*100 + frac
	if neg {
		return -v
	}
	return v
}

func openERPAr(t *testing.T) *pgxpool.Pool {
	t.Helper()
	appURL := os.Getenv("ERP_DATABASE_URL")
	migURL := os.Getenv("ERP_MIGRATOR_DATABASE_URL")
	adminURL := os.Getenv("ERP_ADMIN_DATABASE_URL")
	if appURL == "" || migURL == "" || adminURL == "" {
		t.Skip("database tests need ERP_ADMIN_DATABASE_URL, ERP_MIGRATOR_DATABASE_URL, ERP_DATABASE_URL")
	}
	admin, err := sql.Open("pgx", withDB(adminURL, "erp_ar"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.Exec(`SELECT pg_advisory_lock(34100)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(`SELECT pg_advisory_unlock(34100)`) }()
	sqldb, err := sql.Open("pgx", withDB(migURL, "erp_ar"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqldb.Close() }()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(sqldb, "."); err != nil {
		t.Fatalf("migrate erp_ar: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), withDB(appURL, "erp_ar"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + db
	return u.String()
}
