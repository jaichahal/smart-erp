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

// C6: a journal with unequal debits and credits per currency cannot commit.
// The refusal is a deferred database constraint, not a handler check.
func TestC6(t *testing.T) {
	db, p := newCompany(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	openYear(t, db, p, 2026, day)
	cash := insertAccount(t, db, p, "1000", "cash")
	revenue := insertAccount(t, db, p, "4000", "")

	err := insertJournal(ctx, db, p, day, "system", "", "", []jLine{
		{cash, "10.00", "0.00", "AED"},
		{revenue, "0.00", "4.00", "AED"},
	})
	if err == nil || !strings.Contains(err.Error(), "debits must equal credits") {
		t.Fatalf("unbalanced journal commit = %v", err)
	}
	if countJournals(t, db, p) != 0 {
		t.Fatal("unbalanced journal left a row")
	}

	// Each currency stands alone. A balanced AED pair does not cover an unbalanced USD pair.
	err = insertJournal(ctx, db, p, day, "system", "", "", []jLine{
		{cash, "10.00", "0.00", "AED"},
		{revenue, "0.00", "10.00", "AED"},
		{cash, "5.00", "0.00", "USD"},
		{revenue, "0.00", "4.00", "USD"},
	})
	if err == nil || !strings.Contains(err.Error(), "debits must equal credits") {
		t.Fatalf("mixed-currency unbalanced commit = %v", err)
	}

	err = insertJournal(ctx, db, p, day, "system", "", "", []jLine{
		{cash, "10.00", "0.00", "AED"},
		{revenue, "0.00", "10.00", "AED"},
		{cash, "5.00", "0.00", "USD"},
		{revenue, "0.00", "5.00", "USD"},
	})
	if err != nil {
		t.Fatalf("balanced per currency: %v", err)
	}
	if countJournals(t, db, p) != 1 {
		t.Fatalf("balanced journals = %d", countJournals(t, db, p))
	}

	var journalID uuid.UUID
	err = rls.Tx(ctx, db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM erp.journals WHERE company_id = $1`, p.CompanyID).Scan(&journalID)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPostedLinesImmutable(t, db, journalID)
}

// Posted lines stay insert-only. The application role is refused, and the table
// owner is refused by the guard trigger. The original debit is unchanged.
func assertPostedLinesImmutable(t *testing.T, db *testdb.DB, journalID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`UPDATE erp.journal_lines SET debit = 0 WHERE journal_id = $1`,
		`DELETE FROM erp.journal_lines WHERE journal_id = $1`,
	} {
		_, err := db.App.Exec(ctx, stmt, journalID)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "immutable") && !strings.Contains(err.Error(), "42501") {
			t.Fatalf("app role %s = %v", stmt, err)
		}
		_, err = db.Migrator.Exec(ctx, stmt, journalID)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("owner role %s = %v", stmt, err)
		}
	}
	var debit string
	if err := db.Migrator.QueryRow(ctx, `SELECT debit::text FROM erp.journal_lines WHERE journal_id = $1 AND currency = 'AED' AND debit > 0`, journalID).Scan(&debit); err != nil {
		t.Fatal(err)
	}
	if debit != "10.00" {
		t.Fatalf("posted debit changed to %s", debit)
	}
}
