package ledger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func TestChartTaxAndDimensions(t *testing.T) {
	db, svc, enterer, chart := seeded(t)
	ctx := context.Background()
	codes, err := svc.TaxCodes(ctx, enterer)
	if err != nil {
		t.Fatal(err)
	}
	wantRates := map[string]string{
		"standard": "0.05", "zero_rated": "0", "exempt": "0", "reverse_charge": "0.05", "out_of_scope": "0",
	}
	if len(codes) != len(wantRates) {
		t.Fatalf("tax codes = %+v", codes)
	}
	for _, code := range codes {
		rate, ok := wantRates[code.Code]
		if !ok || !sameRate(code.Rate, rate) {
			t.Fatalf("tax code %+v", code)
		}
	}
	dims, err := svc.Dimensions(ctx, enterer)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDimension(dims, "cost_centre") {
		t.Fatalf("dimensions = %+v", dims)
	}
	for _, role := range []string{"cash", "receivable", "revenue", "vat_output", "discount", "rounding", "stock"} {
		if chart[role] == uuid.Nil {
			t.Fatalf("missing role %s", role)
		}
	}
	change, err := svc.ProposeAccountName(ctx, enterer, chart["revenue"], "Revenue renamed", "correct the name")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveAccountName(ctx, enterer, change); err == nil {
		t.Fatal("proposer approved the account change")
	}
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	if got := accountName(t, db, enterer, chart["revenue"]); got == "Revenue renamed" {
		t.Fatal("unapproved edit changed the account")
	}
	if err := svc.ApproveAccountName(ctx, approver, change); err != nil {
		t.Fatal(err)
	}
	if got := accountName(t, db, enterer, chart["revenue"]); got != "Revenue renamed" {
		t.Fatalf("approved name = %s", got)
	}
}

// C11: line and document discounts post to the discount account and appear on the tax invoice.
func TestC11(t *testing.T) {
	db, svc, enterer, chart := seeded(t)
	ctx := context.Background()
	approveSales(t, svc, enterer, chart)
	posted, err := svc.PostSalesInvoice(ctx, enterer, SalesInvoice{
		DocID: "inv-discount", PostingDate: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		DocumentDiscount: "5.00",
		Lines:            []SalesLine{{Qty: "1", UnitPrice: "100.00", Discount: "10.00", TaxCode: "standard"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := sideSum(t, db, enterer, posted.JournalID, chart["discount"], "debit"); got != "15.00" {
		t.Fatalf("discount posted %s", got)
	}
	printed, err := svc.TaxInvoice(ctx, enterer, posted.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(printed.Lines) != 1 || printed.Lines[0].Discount != "10.00" || printed.DocumentDiscount != "5.00" {
		t.Fatalf("discounts on tax invoice = %+v", printed)
	}
	if printed.Total != "89.25" || printed.Total != printed.LedgerTotal {
		t.Fatalf("printed total %s ledger %s", printed.Total, printed.LedgerTotal)
	}
}

// C12: invoice totals round to the fils. The difference posts to the rounding account.
// The printed total equals the ledger total.
func TestC12(t *testing.T) {
	db, svc, enterer, chart := seeded(t)
	ctx := context.Background()
	approveSales(t, svc, enterer, chart)
	posted, err := svc.PostSalesInvoice(ctx, enterer, SalesInvoice{
		DocID: "inv-round", PostingDate: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		Lines: []SalesLine{
			{Qty: "1", UnitPrice: "10.10", TaxCode: "standard"},
			{Qty: "1", UnitPrice: "10.10", TaxCode: "standard"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := sideSum(t, db, enterer, posted.JournalID, chart["rounding"], "credit"); got != "0.01" {
		t.Fatalf("rounding posted %s", got)
	}
	printed, err := svc.TaxInvoice(ctx, enterer, posted.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if printed.Rounding != "0.01" || printed.Total != "21.22" || printed.Total != printed.LedgerTotal {
		t.Fatalf("printed = %+v", printed)
	}
	if got := sideSum(t, db, enterer, posted.JournalID, chart["vat_output"], "credit"); got != printed.Tax {
		t.Fatalf("printed tax %s ledger vat %s", printed.Tax, got)
	}
}

// C13: a stock movement that would make on-hand negative is refused with NEGATIVE_STOCK.
func TestC13(t *testing.T) {
	_, svc, enterer, _ := seeded(t)
	ctx := context.Background()
	sku, warehouse := uuid.New(), uuid.New()
	err := svc.PostStockMovement(ctx, enterer, StockMovement{SKU: sku, Warehouse: warehouse, Qty: "-1", DocID: "issue-1"})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.NegativeStock {
		t.Fatalf("opening issue = %v", err)
	}
	if err := svc.PostStockMovement(ctx, enterer, StockMovement{SKU: sku, Warehouse: warehouse, Qty: "5", DocID: "receipt-1"}); err != nil {
		t.Fatal(err)
	}
	err = svc.PostStockMovement(ctx, enterer, StockMovement{SKU: sku, Warehouse: warehouse, Qty: "-6", DocID: "issue-2"})
	if !errors.As(err, &ae) || ae.Code != apierr.NegativeStock {
		t.Fatalf("over issue = %v", err)
	}
	if err := svc.PostStockMovement(ctx, enterer, StockMovement{SKU: sku, Warehouse: warehouse, Qty: "-5", DocID: "issue-3"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.OnHand(ctx, enterer, sku, warehouse); got != "0" && got != "0.0000" && got != "0.00" {
		t.Fatalf("on hand = %s", got)
	}
}

// C14: a cash payment that would make a cash account negative is refused.
func TestC14(t *testing.T) {
	db, svc, enterer, chart := seeded(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openAlready(t, db, enterer)
	expense := chart["expense"]
	cash := chart["cash"]
	err := svc.PostCashPayment(ctx, enterer, CashPayment{
		PostingDate: day, DocID: "pay-1", CashAccountID: cash, ExpenseAccountID: expense, Amount: "10.00",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "negative") {
		t.Fatalf("payment from empty cash = %v", err)
	}
	err = insertJournal(ctx, db, enterer, day, "system", "", "", []jLine{
		{expense, "10.00", "0.00", "AED"},
		{cash, "0.00", "10.00", "AED"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "negative") {
		t.Fatalf("database accepted negative cash: %v", err)
	}
	if err := svc.PostCashReceipt(ctx, enterer, CashPayment{
		PostingDate: day, DocID: "rcpt-1", CashAccountID: cash, ExpenseAccountID: expense, Amount: "10.00",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PostCashPayment(ctx, enterer, CashPayment{
		PostingDate: day, DocID: "pay-2", CashAccountID: cash, ExpenseAccountID: expense, Amount: "10.00",
	}); err != nil {
		t.Fatal(err)
	}
	err = svc.PostCashPayment(ctx, enterer, CashPayment{
		PostingDate: day, DocID: "pay-3", CashAccountID: cash, ExpenseAccountID: expense, Amount: "0.01",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "negative") {
		t.Fatalf("overdraw by one fils = %v", err)
	}
	if cashPosition(t, db, enterer, cash) != "0.00" {
		t.Fatalf("cash position = %s", cashPosition(t, db, enterer, cash))
	}
}

func seeded(t *testing.T) (*testdb.DB, *Service, rls.Principal, map[string]uuid.UUID) {
	t.Helper()
	db, svc, p := newLedger(t)
	openYear(t, db, p, 2026, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	chart, err := svc.SeedUAEChart(ctxBackground(), p)
	if err != nil {
		t.Fatal(err)
	}
	return db, svc, p, chart
}

func ctxBackground() context.Context { return context.Background() }

func approveSales(t *testing.T, svc *Service, enterer rls.Principal, chart map[string]uuid.UUID) {
	t.Helper()
	version, err := svc.ProposePostingRule(context.Background(), enterer, RuleProposal{
		Match:             RuleMatch{DocType: "sales_invoice", TaxCode: "standard"},
		DebitAccountID:    chart["receivable"],
		CreditAccountID:   chart["revenue"],
		TaxAccountID:      idPtr(chart["vat_output"]),
		DiscountAccountID: idPtr(chart["discount"]),
		RoundingAccountID: idPtr(chart["rounding"]),
		Reason:            "sales map",
	})
	if err != nil {
		t.Fatal(err)
	}
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	if err := svc.ApprovePostingRule(context.Background(), approver, version.ID); err != nil {
		t.Fatal(err)
	}
}

func idPtr(id uuid.UUID) *uuid.UUID { return &id }

func openAlready(t *testing.T, db *testdb.DB, p rls.Principal) {
	t.Helper()
	_ = db
	_ = p
}

func sideSum(t *testing.T, db *testdb.DB, p rls.Principal, journalID, account uuid.UUID, side string) string {
	t.Helper()
	col := "debit"
	if side == "credit" {
		col = "credit"
	}
	var amount string
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT coalesce(sum(`+col+`), 0)::text FROM erp.journal_lines
			WHERE journal_id = $1 AND account_id = $2`, journalID, account).Scan(&amount)
	})
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func accountName(t *testing.T, db *testdb.DB, p rls.Principal, id uuid.UUID) string {
	t.Helper()
	var name string
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT name FROM erp.accounts WHERE id = $1`, id).Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func cashPosition(t *testing.T, db *testdb.DB, p rls.Principal, account uuid.UUID) string {
	t.Helper()
	var amount string
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT coalesce(sum(debit - credit), 0)::text
			FROM erp.journal_lines WHERE company_id = $1 AND account_id = $2`, p.CompanyID, account).Scan(&amount)
	})
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func sameRate(got, want string) bool {
	g, err1 := parseScale4(got)
	w, err2 := parseScale4(want)
	return err1 == nil && err2 == nil && g == w
}

func hasDimension(dims []Dimension, code string) bool {
	for _, d := range dims {
		if d.Code == code {
			return true
		}
	}
	return false
}
