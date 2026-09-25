package stock_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/internal/stock"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

type env struct {
	db    *testdb.DB
	svc   *stock.Service
	actor rls.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testdb.New(t)
	migrateDB(t, db)
	company := uuid.New()
	actor := rls.Principal{UserID: "stock-" + company.String(), CompanyID: company, Roles: []string{"accountant"}}
	if err := rls.Tx(context.Background(), db.App, actor, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO erp.companies (id, legal_name) VALUES ($1, $2)`, company, "Stock Co")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &env{db: db, svc: stock.New(db.App), actor: actor}
}

func (e *env) item(t *testing.T, sku, class, uom string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := e.svc.RegisterItem(context.Background(), e.actor, stock.Item{
		ID: id, SKU: sku, ItemClass: class, UOM: uom,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) warehouse(t *testing.T, code string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := e.svc.RegisterWarehouse(context.Background(), e.actor, stock.Warehouse{ID: id, Code: code}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) position(t *testing.T, sku, wh uuid.UUID) stock.Position {
	t.Helper()
	rows, err := e.svc.Availability(context.Background(), e.actor, stock.Query{SKUID: sku, WarehouseID: wh})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("positions %d", len(rows))
	}
	return rows[0]
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

func withDB(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(name, "/")
	return u.String()
}
