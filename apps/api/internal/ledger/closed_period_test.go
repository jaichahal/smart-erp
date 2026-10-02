package ledger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// A post into a hard-closed period is refused with PERIOD_CLOSED.
func TestHardClosedPeriodRefusesJournal(t *testing.T) {
	db, svc, enterer := newLedger(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, enterer, 2026, day)
	ar := insertAccount(t, db, enterer, "1100", "receivable")
	revenue := insertAccount(t, db, enterer, "4000", "")
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()
	version, err := svc.ProposePostingRule(ctx, enterer, RuleProposal{
		Match: RuleMatch{DocType: "sales_invoice"}, DebitAccountID: ar, CreditAccountID: revenue, Reason: "map",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApprovePostingRule(ctx, approver, version.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrator.Exec(ctx, `UPDATE erp.periods SET status = 'hard_closed' WHERE company_id = $1`, enterer.CompanyID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.PostByRule(ctx, enterer, RulePosting{
		Match: RuleMatch{DocType: "sales_invoice"}, PostingDate: day, DocID: "inv-closed", Net: "10.00", Tax: "0.00",
	})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.PeriodClosed {
		t.Fatalf("closed period post = %v", err)
	}
	if countJournals(t, db, enterer) != 0 {
		t.Fatal("closed period left a journal")
	}
}
