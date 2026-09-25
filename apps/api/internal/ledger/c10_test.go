package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// C10: a recurring journal posts on schedule after one setup approval and stops after its end date.
func TestC10(t *testing.T) {
	db, svc, enterer := newLedger(t)
	ctx := context.Background()
	openYear(t, db, enterer, 2026, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	expense := insertAccount(t, db, enterer, "5600", "")
	accrued := insertAccount(t, db, enterer, "2100", "")
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	setup, err := svc.ProposeRecurring(ctx, enterer, RecurringProposal{
		Description: "monthly rent", Start: start, End: end, IntervalMonths: 1,
		Lines: []ManualLine{
			{AccountID: expense, Debit: "40.00", Credit: "0.00"},
			{AccountID: accrued, Debit: "0.00", Credit: "40.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.RunRecurring(ctx, enterer, start)
	if err != nil || n != 0 {
		t.Fatalf("unapproved run = %d, %v", n, err)
	}
	if err := svc.ApproveRecurring(ctx, enterer, setup.ID); err == nil {
		t.Fatal("proposer approved the recurring setup")
	}
	if err := svc.ApproveRecurring(ctx, approver, setup.ID); err != nil {
		t.Fatal(err)
	}
	n, err = svc.RunRecurring(ctx, enterer, start.AddDate(0, 0, -1))
	if err != nil || n != 0 {
		t.Fatalf("run before start = %d, %v", n, err)
	}
	n, err = svc.RunRecurring(ctx, enterer, start)
	if err != nil || n != 1 {
		t.Fatalf("first schedule = %d, %v", n, err)
	}
	n, err = svc.RunRecurring(ctx, enterer, start.AddDate(0, 0, 15))
	if err != nil || n != 0 {
		t.Fatalf("mid-interval run = %d, %v", n, err)
	}
	n, err = svc.RunRecurring(ctx, enterer, end)
	if err != nil || n != 1 {
		t.Fatalf("end date run = %d, %v", n, err)
	}
	n, err = svc.RunRecurring(ctx, enterer, end.AddDate(0, 1, 0))
	if err != nil || n != 0 {
		t.Fatalf("after end date = %d, %v", n, err)
	}
	if got := countKind(t, db, enterer, "recurring"); got != 2 {
		t.Fatalf("recurring journals = %d", got)
	}
	err = rls.Tx(ctx, db.App, enterer, func(tx pgx.Tx) error {
		var debit, credit string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(debit),0)::text, coalesce(sum(credit),0)::text
			FROM erp.journal_lines jl
			JOIN erp.journals j ON j.id = jl.journal_id
			WHERE j.company_id = $1 AND j.kind = 'recurring'`, enterer.CompanyID).Scan(&debit, &credit); err != nil {
			return err
		}
		if debit != credit || debit != "80.00" {
			t.Fatalf("recurring totals debit %s credit %s", debit, credit)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func countKind(t *testing.T, db *testdb.DB, p rls.Principal, kind string) int {
	t.Helper()
	var n int
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM erp.journals WHERE company_id = $1 AND kind = $2`, p.CompanyID, kind).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
