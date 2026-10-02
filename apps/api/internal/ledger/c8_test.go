package ledger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// C8: a manual journal without a reason and an attachment cannot be submitted.
// Every manual journal appears by user on the exceptions report.
func TestC8(t *testing.T) {
	db, svc, accountant := newLedger(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, accountant, 2026, day)
	cash := insertAccount(t, db, accountant, "1000", "cash")
	expense := insertAccount(t, db, accountant, "5500", "")
	lines := []ManualLine{
		{AccountID: cash, Debit: "25.00", Credit: "0.00"},
		{AccountID: expense, Debit: "0.00", Credit: "25.00"},
	}

	_, err := svc.SubmitManualJournal(ctx, accountant, ManualJournal{
		PostingDate: day, Description: "no reason", Lines: lines, AttachmentKey: "obj/memo",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reason") {
		t.Fatalf("missing reason = %v", err)
	}
	_, err = svc.SubmitManualJournal(ctx, accountant, ManualJournal{
		PostingDate: day, Description: "no file", Reason: "reclass", Lines: lines,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "attachment") {
		t.Fatalf("missing attachment = %v", err)
	}
	err = insertJournal(ctx, db, accountant, day, "manual", "", "", []jLine{
		{cash, "25.00", "0.00", "AED"},
		{expense, "0.00", "25.00", "AED"},
	})
	if err == nil {
		t.Fatal("database accepted a manual journal without reason and attachment")
	}

	first, err := svc.SubmitManualJournal(ctx, accountant, ManualJournal{
		PostingDate: day, Description: "reclass", Reason: "reclass cash", AttachmentKey: "obj/memo-1", Lines: lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	other := accountant
	other.UserID = "user-other-" + accountant.CompanyID.String()
	if _, err := svc.SubmitManualJournal(ctx, other, ManualJournal{
		PostingDate: day, Description: "accrue locally", Reason: "local accrual", AttachmentKey: "obj/memo-2", Lines: lines,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.ManualJournalExceptions(ctx, accountant, day)
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[string]int{}
	for _, row := range rows {
		byUser[row.UserID]++
		if row.JournalID == first && row.UserID != accountant.UserID {
			t.Fatalf("first journal attributed to %s", row.UserID)
		}
	}
	if byUser[accountant.UserID] != 1 || byUser[other.UserID] != 1 {
		t.Fatalf("exceptions by user = %v", byUser)
	}
	_ = rls.Principal{}
}
