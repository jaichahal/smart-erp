package ledger

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func newCompany(t *testing.T) (*testdb.DB, rls.Principal) {
	t.Helper()
	db := testdb.New(t)
	migrateDB(t, db)
	company := uuid.New()
	p := rls.Principal{UserID: "user-" + company.String(), CompanyID: company, Roles: []string{"accountant"}}
	svc := periods.New(db.App, nil)
	if err := svc.CreateCompany(context.Background(), p, "UAE Trading", "en"); err != nil {
		t.Fatalf("company: %v", err)
	}
	return db, p
}

func openYear(t *testing.T, db *testdb.DB, p rls.Principal, year int, start time.Time) {
	t.Helper()
	svc := periods.New(db.App, nil)
	if err := svc.OpenFiscalYear(context.Background(), p, year, start, "en"); err != nil {
		t.Fatal(err)
	}
}

type jLine struct {
	account  uuid.UUID
	debit    string
	credit   string
	currency string
}

func insertAccount(t *testing.T, db *testdb.DB, p rls.Principal, code, control string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO erp.accounts
			(id, company_id, code, name, account_type, control_type)
			VALUES ($1, $2, $3, $4, 'asset', NULLIF($5, ''))`,
			id, p.CompanyID, code, code, control)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertJournal(ctx context.Context, db *testdb.DB, p rls.Principal, day time.Time, kind, reason, attachment string, lines []jLine) error {
	return rls.Tx(ctx, db.App, p, func(tx pgx.Tx) error {
		var periodID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM erp.periods
			WHERE company_id = $1 AND start_date <= $2 AND end_date >= $2 AND kind = 'month'`,
			p.CompanyID, day.Format("2006-01-02")).Scan(&periodID); err != nil {
			return err
		}
		journalID := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, kind, reason, attachment_key, created_by)
			VALUES ($1, $2, 'journal', $3, $4, $5, $6, $7, $8, $9)`,
			journalID, p.CompanyID, journalID.String(), periodID, day.Format("2006-01-02"), kind, reason, attachment, p.UserID); err != nil {
			return err
		}
		for i, line := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
				(journal_id, company_id, line_no, account_id, debit, credit, currency)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				journalID, p.CompanyID, i+1, line.account, line.debit, line.credit, line.currency); err != nil {
				return err
			}
		}
		return nil
	})
}

func countJournals(t *testing.T, db *testdb.DB, p rls.Principal) int {
	t.Helper()
	var n int
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM erp.journals WHERE company_id = $1`, p.CompanyID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newLedger(t *testing.T) (*testdb.DB, *Service, rls.Principal) {
	t.Helper()
	db, p := newCompany(t)
	return db, New(db.App), p
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
		t.Fatalf("river migrate: %v", err)
	}
}

func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(db, "/")
	return u.String()
}
