package ledger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// C7: a posting rule change is versioned and requires approval. A document
// registered before the change still resolves to the old accounts.
func TestC7(t *testing.T) {
	db, svc, enterer := newLedger(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, enterer, 2026, day)
	ar := insertAccount(t, db, enterer, "1100", "receivable")
	revenueA := insertAccount(t, db, enterer, "4000", "")
	revenueB := insertAccount(t, db, enterer, "4100", "")
	vat := insertAccount(t, db, enterer, "2200", "vat_output")
	approver := enterer
	approver.UserID = "approver-" + enterer.CompanyID.String()

	match := RuleMatch{DocType: "sales_invoice", TaxCode: "standard"}
	v1, err := svc.ProposePostingRule(ctx, enterer, RuleProposal{
		Match: match, DebitAccountID: ar, CreditAccountID: revenueA, TaxAccountID: &vat, Reason: "initial map",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApprovePostingRule(ctx, enterer, v1.ID); err == nil {
		t.Fatal("the user who proposed the rule approved it")
	}
	if err := svc.ApprovePostingRule(ctx, approver, v1.ID); err != nil {
		t.Fatal(err)
	}

	journalID, err := svc.PostByRule(ctx, enterer, RulePosting{
		Match: match, PostingDate: day, DocID: "inv-1", Net: "100.00", Tax: "5.00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := creditAccount(t, db, enterer, journalID); got != revenueA {
		t.Fatalf("registered credit account = %s, want %s", got, revenueA)
	}

	v2, err := svc.ProposePostingRule(ctx, enterer, RuleProposal{
		Match: match, DebitAccountID: ar, CreditAccountID: revenueB, TaxAccountID: &vat, Reason: "remap revenue",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := svc.ResolvePostingRule(ctx, enterer, match)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != v1.ID || current.CreditAccountID != revenueA {
		t.Fatalf("unapproved change affected resolution: %+v", current)
	}
	if err := svc.ApprovePostingRule(ctx, approver, v2.ID); err != nil {
		t.Fatal(err)
	}
	current, err = svc.ResolvePostingRule(ctx, enterer, match)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID == v1.ID || current.CreditAccountID != revenueB {
		t.Fatalf("approved change did not become current: %+v", current)
	}

	reprint, err := svc.RuleForJournal(ctx, enterer, journalID)
	if err != nil {
		t.Fatal(err)
	}
	if reprint.ID != v1.ID || reprint.CreditAccountID != revenueA {
		t.Fatalf("reprint resolved to %+v, want version %s account %s", reprint, v1.ID, revenueA)
	}
	if got := creditAccount(t, db, enterer, journalID); got != revenueA {
		t.Fatalf("audit line account changed to %s", got)
	}
	stored, err := svc.PostingRuleVersion(ctx, enterer, v1.ID)
	if err != nil || stored.CreditAccountID != revenueA {
		t.Fatalf("stored version = %+v, %v", stored, err)
	}
	err = rls.Tx(ctx, db.App, enterer, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE erp.posting_rule_versions SET credit_account_id = $2 WHERE id = $1`, v1.ID, revenueB)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("approved rule version update = %v", err)
	}
}

func creditAccount(t *testing.T, db *testdb.DB, p rls.Principal, journalID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT account_id FROM erp.journal_lines
			WHERE journal_id = $1 AND credit > 0 AND account_id <> (
				SELECT account_id FROM erp.journal_lines WHERE journal_id = $1 AND debit > 0 LIMIT 1
			)
			ORDER BY line_no LIMIT 1`, journalID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
