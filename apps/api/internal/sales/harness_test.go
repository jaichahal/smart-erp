package sales

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

const salesDB = "erp_sales"

var (
	migrateOnce sync.Once
	migrateErr  error
	appURL      string
	migURL      string
)

func newWorld(t *testing.T) *world {
	t.Helper()
	if os.Getenv("ERP_ADMIN_DATABASE_URL") == "" || os.Getenv("ERP_DATABASE_URL") == "" || os.Getenv("ERP_MIGRATOR_DATABASE_URL") == "" {
		t.Skip("database tests need ERP_ADMIN_DATABASE_URL, ERP_MIGRATOR_DATABASE_URL, ERP_DATABASE_URL")
	}
	app, mig := pools(t)
	company := uuid.New()
	p := rls.Principal{UserID: "agent-" + company.String()[:8], CompanyID: company, Roles: []string{"sales_agent"}}
	w := &world{t: t, pool: app, mig: mig, p: p, svc: New(app)}
	ctx := context.Background()
	if err := rls.Tx(ctx, app, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.companies (id, legal_name) VALUES ($1, 'Sales Co')`, company); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.business_calendars
			(company_id, timezone, open_minute, close_minute, weekend_days, holidays)
			VALUES ($1, 'Asia/Dubai', 480, 960, '{friday,saturday}', '[]'::jsonb)`, company)
		return err
	}); err != nil {
		t.Fatalf("company: %v", err)
	}
	w.supplier("Acme Trading LLC", "Dubai", "100234567800003")
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "1000.00", 30, 0, "0", "cust-v1", true, false)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0.05")
	w.stock("WH", "100", "500.00")
	return w
}

func pools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	migrateOnce.Do(func() { migrateErr = migrateSales() })
	if migrateErr != nil {
		t.Fatalf("erp_sales: %v", migrateErr)
	}
	ctx := context.Background()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	mig, err := pgxpool.New(ctx, migURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.Close()
		mig.Close()
	})
	return app, mig
}

func migrateSales() error {
	admin := os.Getenv("ERP_ADMIN_DATABASE_URL")
	app := os.Getenv("ERP_DATABASE_URL")
	mig := os.Getenv("ERP_MIGRATOR_DATABASE_URL")
	if admin == "" || app == "" || mig == "" {
		return errors.New("database tests need ERP_ADMIN_DATABASE_URL, ERP_MIGRATOR_DATABASE_URL, ERP_DATABASE_URL")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		return err
	}
	defer adminPool.Close()
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE erp_sales TEMPLATE erp_template OWNER erp_migrator`); err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	appURL = withDB(app, salesDB)
	migURL = withDB(mig, salesDB)
	db, err := sql.Open("pgx", migURL)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func withDB(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

type world struct {
	t    *testing.T
	pool *pgxpool.Pool
	mig  *pgxpool.Pool
	p    rls.Principal
	svc  *Service
}

func (w *world) supplier(name, address, trn string) {
	w.t.Helper()
	w.exec(w.t, `INSERT INTO erp.sales_suppliers (company_id, name, address, trn) VALUES ($1,$2,$3,$4)
		ON CONFLICT (company_id) DO UPDATE SET name=$2, address=$3, trn=$4`, w.p.CompanyID, name, address, trn)
}

func (w *world) customer(id, name, address, trn, limit string, terms, discountDays int, discountRate, version string, vat, cash bool) {
	w.t.Helper()
	w.exec(w.t, `INSERT INTO erp.sales_customers
		(company_id, id, name, address, trn, credit_limit, payment_terms_days, discount_days, discount_rate, version_id, vat_registered, cash_sale)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (company_id, id) DO UPDATE SET name=$3, address=$4, trn=$5, credit_limit=$6, payment_terms_days=$7,
			discount_days=$8, discount_rate=$9, version_id=$10, vat_registered=$11, cash_sale=$12`,
		w.p.CompanyID, id, name, address, trn, limit, terms, discountDays, discountRate, version, vat, cash)
}

func (w *world) sku(sku, description, floor, taxCode, taxVersion, taxRate string) {
	w.t.Helper()
	w.exec(w.t, `INSERT INTO erp.sales_skus
		(company_id, sku, description, floor_price, tax_code, tax_code_version_id, tax_rate)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (company_id, sku) DO UPDATE SET description=$3, floor_price=$4, tax_code=$5, tax_code_version_id=$6, tax_rate=$7`,
		w.p.CompanyID, sku, description, floor, taxCode, taxVersion, taxRate)
}

func (w *world) stock(warehouse, onHand, value string) {
	w.t.Helper()
	w.exec(w.t, `INSERT INTO erp.sales_stock (company_id, sku, warehouse_id, on_hand, value) VALUES ($1,'SKU1',$2,$3,$4)
		ON CONFLICT (company_id, sku, warehouse_id) DO UPDATE SET on_hand=$3, value=$4`,
		w.p.CompanyID, warehouse, onHand, value)
}

func (w *world) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := rls.Tx(context.Background(), w.pool, w.p, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), q, args...)
		return err
	}); err != nil {
		t.Fatalf("sql: %v", err)
	}
}

func (w *world) scalar(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s string
	if err := rls.Tx(context.Background(), w.pool, w.p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), q, args...).Scan(&s)
	}); err != nil {
		t.Fatalf("scalar: %v", err)
	}
	return s
}

func (w *world) roles(t *testing.T, docID string) map[string]JournalLine {
	t.Helper()
	out := map[string]JournalLine{}
	if err := rls.Tx(context.Background(), w.pool, w.p, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT role, debit, credit FROM erp.sales_postings WHERE company_id=$1 AND doc_id=$2`, w.p.CompanyID, docID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line JournalLine
			if err := rows.Scan(&line.Role, &line.Debit, &line.Credit); err != nil {
				return err
			}
			out[line.Role] = line
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("roles: %v", err)
	}
	return out
}

func (w *world) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := rls.Tx(context.Background(), w.pool, w.p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), q, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (w *world) orderInput() OrderInput {
	return OrderInput{
		CustomerID: "cust-1", WarehouseID: "WH", Currency: "AED",
		AsOf:  time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC),
		Lines: []LineInput{{SKU: "SKU1", Qty: "2", UnitPrice: "20.00"}},
	}
}

type countingStock struct {
	calls int
	saw   bool
	fail  error
}

func (c *countingStock) Reserve(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string, _ []StockLine) error {
	c.calls++
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.sales_orders WHERE company_id=$1 AND id=$2`, company, orderID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("reservation ran outside the order transaction")
	}
	c.saw = true
	if c.fail != nil {
		return c.fail
	}
	return nil
}

func (c *countingStock) Release(context.Context, pgx.Tx, uuid.UUID, string) error { return c.fail }
func (c *countingStock) Consume(context.Context, pgx.Tx, uuid.UUID, string, []StockLine) ([]Consumed, error) {
	return nil, c.fail
}
func (c *countingStock) Restore(context.Context, pgx.Tx, uuid.UUID, []RestoreLine) error {
	return c.fail
}

type countingLedger struct{ n int }

func (c *countingLedger) Post(context.Context, pgx.Tx, uuid.UUID, Journal) error {
	c.n++
	return errors.New("ledger must not be called for an order")
}

type countingCredit struct{ calls int }

func (c *countingCredit) Check(context.Context, pgx.Tx, uuid.UUID, string, string) (CreditDecision, error) {
	c.calls++
	return CreditDecision{Open: "0.00", Limit: "1000.00"}, nil
}
