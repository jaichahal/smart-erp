package periods

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func newService(t *testing.T) (*testdb.DB, *Service, rls.Principal) {
	t.Helper()
	db := testdb.New(t)
	migrateDB(t, db)
	client, err := outbox.NewClient(db.App, nil, nil)
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	svc := New(db.App, client)
	svc.Approvals = allowGate{}
	company := uuid.New()
	p := rls.Principal{UserID: "user-" + company.String(), CompanyID: company, Roles: []string{RoleAccountant}}
	if err := svc.CreateCompany(context.Background(), p, "Test Company", "en"); err != nil {
		t.Fatalf("company: %v", err)
	}
	return db, svc, p
}

type allowGate struct{}

func (allowGate) Approved(context.Context, uuid.UUID, uuid.UUID, string) error { return nil }

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

func schemaDoc(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "contracts", "events", "event.schema.json")
		b, err := os.ReadFile(p)
		if err == nil {
			return b
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("event schema not found")
	return nil
}

func listNumbers(t *testing.T, db *testdb.DB, p rls.Principal, voids bool) []int64 {
	t.Helper()
	q := `SELECT number FROM erp.number_allocations WHERE company_id = $1 ORDER BY number`
	if voids {
		q = `SELECT number FROM erp.number_voids WHERE company_id = $1 ORDER BY number`
	}
	var nums []int64
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), q, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int64
			if err := rows.Scan(&n); err != nil {
				return err
			}
			nums = append(nums, n)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return nums
}

func companyName(t *testing.T, db *testdb.DB, p rls.Principal) string {
	t.Helper()
	var name string
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT legal_name FROM erp.companies WHERE id = $1`, p.CompanyID).Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func periodCovering(t *testing.T, db *testdb.DB, p rls.Principal, day string) Period {
	t.Helper()
	var out Period
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		row := tx.QueryRow(context.Background(), `SELECT id, fiscal_year, month, kind, status, start_date, end_date, state_version
			FROM erp.periods WHERE company_id = $1 AND start_date <= $2 AND end_date >= $2 AND kind = 'month'`, p.CompanyID, day)
		got, err := scanDated(row)
		out = got.Period
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func jobArgs(t *testing.T, db *testdb.DB, kind, docID string) []byte {
	t.Helper()
	var raw []byte
	err := db.App.QueryRow(context.Background(), `SELECT args FROM river_job WHERE kind = $1 AND args->'subject'->>'doc_id' = $2 ORDER BY id DESC LIMIT 1`, kind, docID).Scan(&raw)
	if err != nil {
		t.Fatalf("job %s %s: %v", kind, docID, err)
	}
	return raw
}
