package receivables_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func TestK1_StatementImportDedupesByReference(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	csvBody := "date,amount,reference,description\n2026-09-20,100.00,REF1,wire\n"
	first := h.importStmt(acct, "csv", csvBody)
	if f64Of(t, first["imported"]) != 1 || f64Of(t, first["line_count"]) != 1 {
		t.Fatalf("first import: %v", first)
	}
	second := h.importStmt(acct, "csv", csvBody)
	if f64Of(t, second["skipped_duplicates"]) != 1 || f64Of(t, second["line_count"]) != 1 {
		t.Fatalf("duplicate csv: %v", second)
	}
	mt := ":20:X\n:61:260920C50,00NTRF//REF2\n:86:other\n"
	third := h.importStmt(acct, "mt940", mt)
	if f64Of(t, third["imported"]) != 1 || f64Of(t, third["line_count"]) != 2 {
		t.Fatalf("mt940: %v", third)
	}
	fourth := h.importStmt(acct, "mt940", mt)
	if f64Of(t, fourth["skipped_duplicates"]) != 1 || f64Of(t, fourth["line_count"]) != 2 {
		t.Fatalf("duplicate mt940: %v", fourth)
	}
}

func TestK2_OpenFinanceFeedHonoursConsentExpiry(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	h.post("/api/v1/bank/feed/consent", map[string]any{
		"account_id": acct, "expires_on": "2026-12-31", "provider": "uae-open-finance",
	}, "")
	status := h.get("/api/v1/bank/feed/status?account=" + acct)
	if status["consent_expires"] != "2026-12-31" {
		t.Fatalf("expiry: %v", status)
	}
	ran := h.post("/api/v1/bank/feed/run", map[string]any{
		"account_id": acct, "format": "csv", "as_of": "2026-09-20",
		"body": "date,amount,reference,description\n2026-09-20,10.00,FEED1,nightly\n",
	}, "")
	if f64Of(t, ran["imported"]) != 1 {
		t.Fatalf("nightly: %v", ran)
	}
	code := h.postStatus("/api/v1/bank/feed/run", map[string]any{
		"account_id": acct, "format": "csv", "as_of": "2027-01-02",
		"body": "date,amount,reference,description\n2027-01-02,10.00,FEED2,late\n",
	}, "")
	if code != http.StatusConflict && code != http.StatusBadRequest {
		t.Fatalf("expired consent status %d", code)
	}
}

func TestK3_AutoMatchProposesAndUnmatchedStaysVisible(t *testing.T) {
	h := newH(t)
	cust := h.customer("Match Co", "0.00", nil, false, 0)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("100.00"),
		"posted_on": "2026-09-20", "bank_account_id": acct,
	})
	h.importStmt(acct, "csv", "date,amount,reference,description\n2026-09-20,100.00,S1,in\n2026-09-20,20.00,S2,odd\n")
	h.post("/api/v1/bank/reconciliation/rules", map[string]any{"account_id": acct, "kind": "amount_and_date"}, "")
	h.post("/api/v1/bank/reconciliation/auto", map[string]any{"account_id": acct}, "")
	rec := h.recon(acct, "2026-09-20")
	byRef := linesByRef(t, rec["lines"])
	if byRef["S1"]["status"] != "proposed" || byRef["S2"]["status"] != "open" {
		t.Fatalf("proposals: %v", rec["lines"])
	}
	h.post("/api/v1/bank/reconciliation/explain", map[string]any{
		"line_id": byRef["S2"]["id"], "reason": "bank fee",
	}, "")
	rec = h.recon(acct, "2026-09-20")
	byRef = linesByRef(t, rec["lines"])
	if byRef["S2"]["status"] != "explained" || byRef["S1"]["status"] != "proposed" {
		t.Fatalf("after explain: %v", rec["lines"])
	}
}

func TestK4_ReconciledAccountHasZeroDifference(t *testing.T) {
	h := newH(t)
	cust := h.customer("Zero Co", "0.00", nil, false, 0)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	got := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("100.00"),
		"posted_on": "2026-09-20", "bank_account_id": acct,
	})
	h.importStmt(acct, "csv", "date,amount,reference,description\n2026-09-20,100.00,ONLY,in\n")
	rec := h.recon(acct, "2026-09-20")
	lineID := strOf(t, linesByRef(t, rec["lines"])["ONLY"]["id"])
	h.post("/api/v1/bank/reconciliation/match", map[string]any{
		"line_id": lineID, "book_line_id": got["bank_line_id"],
	}, "")
	rec = h.recon(acct, "2026-09-20")
	if moneyOf(t, rec["difference"]) != "0.00" || rec["status"] != "reconciled" {
		t.Fatalf("reconciliation: %v", rec)
	}
}

func TestK5_ReconciliationStatusGatesMonthEnd(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	h.importStmt(acct, "csv", "date,amount,reference,description\n2026-09-20,15.00,GATE,in\n")
	if h.recon(acct, "2026-09-20")["blocks_month_end"] != true {
		t.Fatal("unmatched line did not gate month end")
	}
	lineID := strOf(t, linesByRef(t, h.recon(acct, "2026-09-20")["lines"])["GATE"]["id"])
	h.post("/api/v1/bank/reconciliation/explain", map[string]any{"line_id": lineID, "reason": "timing"}, "")
	if h.recon(acct, "2026-09-20")["blocks_month_end"] != false {
		t.Fatal("explained line still gates month end")
	}
}

func TestK6_AllocationDoesNotCreateABankMovement(t *testing.T) {
	h := newH(t)
	cust := h.customer("Alloc Co", "0.00", nil, false, 0)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	inv := h.invoice(cust, "K6", "2026-09-01", "2026-09-30", "80.00")
	rec := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("80.00"),
		"posted_on": "2026-09-20", "bank_account_id": acct,
	})
	before := h.accountView(acct)
	h.post("/api/v1/receipts/"+strOf(t, rec["id"])+"/allocate", map[string]any{
		"allocations": []any{alloc(inv, "80.00")},
	}, strOf(t, rec["state_version"]))
	after := h.accountView(acct)
	if moneyOf(t, after["balance"]) != moneyOf(t, before["balance"]) || after["line_count"] != before["line_count"] {
		t.Fatalf("bank moved before=%v after=%v", before, after)
	}
	row := indexByNumber(t, h.list("/api/v1/receivables?as_of=2026-09-20"))["K6"]
	if moneyOf(t, row["open_amount"]) != "0.00" {
		t.Fatalf("allocation: %v", row)
	}
}

func TestK7_UnmatchRequiresReasonAndAudit(t *testing.T) {
	h := newH(t)
	cust := h.customer("Audit Co", "0.00", nil, false, 0)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	got := h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("30.00"),
		"posted_on": "2026-09-20", "bank_account_id": acct,
	})
	h.importStmt(acct, "csv", "date,amount,reference,description\n2026-09-20,30.00,AUD,in\n")
	lineID := strOf(t, linesByRef(t, h.recon(acct, "2026-09-20")["lines"])["AUD"]["id"])
	h.post("/api/v1/bank/reconciliation/match", map[string]any{
		"line_id": lineID, "book_line_id": got["bank_line_id"],
	}, "")
	if code := h.postStatus("/api/v1/bank/reconciliation/unmatch", map[string]any{"line_id": lineID}, ""); code != http.StatusBadRequest {
		t.Fatalf("missing reason status %d", code)
	}
	h.post("/api/v1/bank/reconciliation/unmatch", map[string]any{"line_id": lineID, "reason": "wrong invoice"}, "")
	var reason, after string
	err := rls.Tx(rls.WithPrincipal(context.Background(), h.p), h.pool, h.p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT reason, after_state::text FROM erp.audit_events
			WHERE company_id = $1 AND event_type = 'bank.line_unmatched'
			ORDER BY chain_seq DESC LIMIT 1`, h.p.CompanyID).Scan(&reason, &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	if reason != "wrong invoice" || !strings.Contains(after, "request_id") {
		t.Fatalf("audit reason=%s after=%s", reason, after)
	}
}

func TestK8_BankFeedFailureAlerts(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	h.post("/api/v1/bank/feed/fail", map[string]any{"account_id": acct, "message": "provider timeout"}, "")
	status := h.get("/api/v1/bank/feed/status?account=" + acct)
	if status["alert"] != true || status["status"] != "failed" {
		t.Fatalf("status: %v", status)
	}
}

func TestK9_PettyVoucherPostsAndCannotExceedFloat(t *testing.T) {
	h := newH(t)
	petty := h.account("petty", "100.00", "2026-09-01", "custodian-1")
	ok := h.post("/api/v1/petty-cash-vouchers", map[string]any{
		"account_id": petty, "posted_on": "2026-09-20",
		"expense": money("70.00"), "vat": money("10.00"),
	}, "")
	roles := rolesOf(t, ok["postings"])
	if roles["expense"] != 7000 || roles["vat_input"] != 1000 || roles["petty_cash"] != -8000 {
		t.Fatalf("postings: %v", ok["postings"])
	}
	code := h.postStatus("/api/v1/petty-cash-vouchers", map[string]any{
		"account_id": petty, "posted_on": "2026-09-20", "expense": money("30.00"),
	}, "")
	if code != http.StatusBadRequest && code != http.StatusConflict {
		t.Fatalf("over float status %d", code)
	}
}

func TestK10_ContraMovesCashAndBank(t *testing.T) {
	h := newH(t)
	cash := h.account("cash", "100.00", "2026-09-01", "")
	acct := h.account("bank", "0.00", "2026-09-01", "")
	h.post("/api/v1/bank/contra", map[string]any{
		"kind": "deposit", "cash_account_id": cash, "bank_account_id": acct,
		"amount": money("40.00"), "posted_on": "2026-09-20",
	}, "")
	if moneyOf(t, h.accountView(cash)["balance"]) != "60.00" || moneyOf(t, h.accountView(acct)["balance"]) != "40.00" {
		t.Fatalf("deposit cash=%v bank=%v", h.accountView(cash), h.accountView(acct))
	}
	h.post("/api/v1/bank/contra", map[string]any{
		"kind": "withdraw", "cash_account_id": cash, "bank_account_id": acct,
		"amount": money("10.00"), "posted_on": "2026-09-21",
	}, "")
	if moneyOf(t, h.accountView(cash)["balance"]) != "70.00" || moneyOf(t, h.accountView(acct)["balance"]) != "30.00" {
		t.Fatalf("withdraw cash=%v bank=%v", h.accountView(cash), h.accountView(acct))
	}
}

func TestK11_ChequeNumbersHaveOneStateAndGapsAreExceptions(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	for _, n := range []string{"101", "102", "104"} {
		h.post("/api/v1/bank/cheques", map[string]any{"account_id": acct, "number": n, "state": "issued"}, "")
	}
	gaps := h.get("/api/v1/bank/cheques/gaps?account=" + acct)
	raw := sliceOf(t, gaps["gaps"])
	if len(raw) != 1 || strOf(t, raw[0]) != "103" {
		t.Fatalf("gaps: %v", gaps)
	}
	code := h.postStatus("/api/v1/bank/cheques", map[string]any{"account_id": acct, "number": "101", "state": "voided"}, "")
	if code != http.StatusConflict {
		t.Fatalf("duplicate cheque status %d", code)
	}
}

func TestK12_StoppedChequeCannotClear(t *testing.T) {
	h := newH(t)
	acct := h.account("bank", "0.00", "2026-09-01", "")
	issued := h.post("/api/v1/bank/cheques", map[string]any{"account_id": acct, "number": "50", "state": "issued"}, "")
	id := strOf(t, issued["id"])
	h.post("/api/v1/bank/cheques/"+id+"/stop", map[string]any{}, strOf(t, issued["state_version"]))
	code := h.postStatus("/api/v1/bank/cheques/"+id+"/clear", map[string]any{}, "2")
	if code != http.StatusConflict {
		t.Fatalf("clear stopped status %d", code)
	}
}

func TestK13_FacilityUtilisationAndInterest(t *testing.T) {
	h := newH(t)
	fac := h.post("/api/v1/bank/facilities", map[string]any{
		"name": "OD", "limit": money("1000.00"), "annual_rate_bp": 1200, "maturity": "2026-12-31",
	}, "")
	drawn := h.post("/api/v1/bank/facilities/"+strOf(t, fac["id"])+"/draw", map[string]any{
		"amount": money("400.00"), "as_of": "2026-09-01",
	}, strOf(t, fac["state_version"]))
	if moneyOf(t, drawn["utilisation"]) != "400.00" {
		t.Fatalf("draw: %v", drawn)
	}
	code := h.postStatus("/api/v1/bank/facilities/"+strOf(t, fac["id"])+"/draw", map[string]any{
		"amount": money("700.00"), "as_of": "2026-09-02",
	}, strOf(t, drawn["state_version"]))
	if code != http.StatusBadRequest && code != http.StatusConflict {
		t.Fatalf("over limit status %d", code)
	}
	interest := h.post("/api/v1/bank/facilities/"+strOf(t, fac["id"])+"/interest", map[string]any{
		"as_of": "2026-10-01", "days": 30,
	}, "")
	if moneyOf(t, interest["amount"]) != "3.95" {
		t.Fatalf("interest: %v", interest)
	}
	roles := rolesOf(t, interest["postings"])
	if roles["expense"] <= 0 || roles["payable"] >= 0 {
		t.Fatalf("interest postings: %v", interest["postings"])
	}
}

func TestK20_DailyCashPositionReconcilesWithoutPDC(t *testing.T) {
	h := newH(t)
	cust := h.customer("Cash Co", "0.00", nil, false, 0)
	acct := h.account("bank", "1000.00", "2026-09-01", "")
	h.invoice(cust, "DAY", "2026-09-01", "2026-09-30", "200.00")
	h.receipt(map[string]any{
		"customer_id": cust, "method": "transfer", "amount": money("200.00"),
		"posted_on": "2026-09-25", "bank_account_id": acct,
		"allocations": []any{alloc(h.invoice(cust, "DAY2", "2026-09-01", "2026-09-30", "200.00"), "200.00")},
	})
	h.receipt(map[string]any{
		"customer_id": cust, "method": "pdc", "amount": money("500.00"),
		"posted_on": "2026-09-25", "bank_account_id": acct,
		"allocations": []any{alloc(h.invoice(cust, "PDCDAY", "2026-09-01", "2026-09-30", "500.00"), "500.00")},
		"cheque":      cheque("77", "ENBD", "2026-09-28"),
	})
	h.post("/api/v1/bank/payments", map[string]any{
		"account_id": acct, "amount": money("50.00"), "posted_on": "2026-09-25",
	}, "")
	h.importStmt(acct, "csv", "date,amount,reference,description\n2026-09-25,5.00,UNMATCHED,odd\n")
	pos := h.get("/api/v1/bank/cash-position?date=2026-09-25")
	want := map[string]string{
		"opening": "1000.00", "receipts": "200.00", "payments": "50.00",
		"pdcs_due_this_week": "500.00", "closing": "1150.00", "bank_and_cash": "1150.00",
	}
	for key, amount := range want {
		if moneyOf(t, pos[key]) != amount {
			t.Fatalf("%s = %v", key, pos[key])
		}
	}
	if f64Of(t, pos["unreconciled_count"]) != 1 {
		t.Fatalf("unreconciled: %v", pos["unreconciled_count"])
	}
	if moneyOf(t, pos["closing"]) != moneyOf(t, pos["bank_and_cash"]) {
		t.Fatalf("closing does not reconcile: %v", pos)
	}
}

func (h *harness) importStmt(account, format, body string) map[string]any {
	h.t.Helper()
	return h.post("/api/v1/bank/statements/import", map[string]any{
		"account_id": account, "format": format, "body": body,
	}, "")
}

func (h *harness) recon(account, asOf string) map[string]any {
	h.t.Helper()
	return h.get("/api/v1/bank/reconciliation?account=" + account + "&as_of=" + asOf)
}

func linesByRef(t *testing.T, raw any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, item := range sliceOf(t, raw) {
		row := mapOf(t, item)
		out[strOf(t, row["reference"])] = row
	}
	return out
}
