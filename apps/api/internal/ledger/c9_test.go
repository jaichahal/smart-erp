package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// C9: an accrual auto-reverses on the first day of the next period.
func TestC9(t *testing.T) {
	db, svc, enterer := newLedger(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, enterer, 2026, day)
	expense := insertAccount(t, db, enterer, "5500", "")
	accrued := insertAccount(t, db, enterer, "2100", "")
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	v1, err := svc.ProposePostingRule(ctx, enterer, RuleProposal{
		Match: RuleMatch{DocType: "accrual"}, DebitAccountID: expense, CreditAccountID: accrued, Reason: "accrual map",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolvePostingRule(ctx, enterer, RuleMatch{DocType: "accrual"}); err == nil {
		t.Fatal("unapproved accrual rule resolved")
	}
	if err := svc.ApprovePostingRule(ctx, approver, v1.ID); err != nil {
		t.Fatal(err)
	}

	journalID, err := svc.PostAccrual(ctx, enterer, SchedulePosting{
		PostingDate: day, DocID: "acc-1", Amount: "80.00",
	})
	if err != nil {
		t.Fatal(err)
	}
	nextFirst := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	n, err := svc.ReverseDue(ctx, enterer, day)
	if err != nil || n != 0 {
		t.Fatalf("reverse on posting day = %d, %v", n, err)
	}
	n, err = svc.ReverseDue(ctx, enterer, nextFirst.AddDate(0, 0, -1))
	if err != nil || n != 0 {
		t.Fatalf("reverse before next period = %d, %v", n, err)
	}
	n, err = svc.ReverseDue(ctx, enterer, nextFirst)
	if err != nil || n != 1 {
		t.Fatalf("reverse on first day = %d, %v", n, err)
	}
	n, err = svc.ReverseDue(ctx, enterer, nextFirst)
	if err != nil || n != 0 {
		t.Fatalf("second reverse = %d, %v", n, err)
	}

	if got := lineAmount(t, db, enterer, journalID, expense, "debit"); got != "80.00" {
		t.Fatalf("original accrual debit changed to %s", got)
	}
	revID := reversalOf(t, db, enterer, journalID)
	if got := lineAmount(t, db, enterer, revID, accrued, "debit"); got != "80.00" {
		t.Fatalf("reversal debit of liability = %s", got)
	}
	if got := lineAmount(t, db, enterer, revID, expense, "credit"); got != "80.00" {
		t.Fatalf("reversal credit of expense = %s", got)
	}
	var posted time.Time
	err = rls.Tx(ctx, db.App, enterer, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT posting_date FROM erp.journals WHERE id = $1`, revID).Scan(&posted)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !posted.Equal(nextFirst) {
		t.Fatalf("reversal date = %s", posted.Format("2006-01-02"))
	}
}

func TestPrepaymentAutoReverses(t *testing.T) {
	db, svc, enterer := newLedger(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, enterer, 2026, day)
	prepaid := insertAccount(t, db, enterer, "1300", "")
	bank := insertAccount(t, db, enterer, "1010", "bank")
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	v, err := svc.ProposePostingRule(ctx, enterer, RuleProposal{
		Match: RuleMatch{DocType: "prepayment"}, DebitAccountID: prepaid, CreditAccountID: bank, Reason: "prepayment map",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApprovePostingRule(ctx, approver, v.ID); err != nil {
		t.Fatal(err)
	}
	journalID, err := svc.PostPrepayment(ctx, enterer, SchedulePosting{PostingDate: day, DocID: "pre-1", Amount: "120.00"})
	if err != nil {
		t.Fatal(err)
	}
	nextFirst := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	n, err := svc.ReverseDue(ctx, enterer, nextFirst)
	if err != nil || n != 1 {
		t.Fatalf("prepayment reverse = %d, %v", n, err)
	}
	if got := lineAmount(t, db, enterer, journalID, prepaid, "debit"); got != "120.00" {
		t.Fatalf("original prepayment changed to %s", got)
	}
	revID := reversalOf(t, db, enterer, journalID)
	if got := lineAmount(t, db, enterer, revID, bank, "debit"); got != "120.00" {
		t.Fatalf("prepayment reversal = %s", got)
	}
}

func lineAmount(t *testing.T, db *testdb.DB, p rls.Principal, journalID, accountID uuid.UUID, side string) string {
	t.Helper()
	var amount string
	col := "debit"
	if side == "credit" {
		col = "credit"
	}
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT `+col+`::text FROM erp.journal_lines
			WHERE journal_id = $1 AND account_id = $2`, journalID, accountID).Scan(&amount)
	})
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func reversalOf(t *testing.T, db *testdb.DB, p rls.Principal, source uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT id FROM erp.journals WHERE reverses_journal_id = $1 AND company_id = $2`, source, p.CompanyID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
