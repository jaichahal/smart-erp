package stock_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// A fresh database must apply every migration, including both historical
// CREATE TABLE statements for erp.stock_reservations, and end on the stock
// lifecycle with foreign keys to the masters tables.
func TestFreshDatabaseHasOneStockReservationShape(t *testing.T) {
	db := testdb.New(t)
	migrateDB(t, db)
	ctx := context.Background()

	var uniqueDef string
	if err := db.Migrator.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'erp.stock_reservations'::regclass AND contype = 'u'`).Scan(&uniqueDef); err != nil {
		t.Fatal(err)
	}
	if uniqueDef != "UNIQUE (company_id, order_line_id)" {
		t.Fatalf("unique constraint %s", uniqueDef)
	}

	rows, err := db.Migrator.Query(ctx, `
		SELECT conname, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'erp.stock_reservations'::regclass AND contype = 'f'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	fks := map[string]string{}
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			t.Fatal(err)
		}
		fks[name] = body
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"FOREIGN KEY (company_id) REFERENCES erp.companies(id)",
		"FOREIGN KEY (sku_id) REFERENCES erp.skus(id)",
		"FOREIGN KEY (warehouse_id) REFERENCES erp.warehouses(id)",
	}
	got := make([]string, 0, len(fks))
	for _, body := range fks {
		got = append(got, body)
	}
	if len(got) != len(want) {
		t.Fatalf("foreign keys %v", fks)
	}
	for _, body := range want {
		if _, ok := indexOf(got, body); !ok {
			t.Fatalf("missing %s in %v", body, fks)
		}
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "stock_skus") || strings.Contains(joined, "stock_warehouses") {
		t.Fatalf("snapshot foreign keys remain: %v", fks)
	}

	var indexDef string
	if err := db.Migrator.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = 'erp' AND indexname = 'stock_reservations_active_idx'`).Scan(&indexDef); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexDef, "WHERE (status = 'active'") && !strings.Contains(indexDef, "WHERE ((status)::text = 'active'") && !strings.Contains(strings.ToLower(indexDef), "status") {
		t.Fatalf("active partial index %s", indexDef)
	}
	if !strings.Contains(indexDef, "active") {
		t.Fatalf("active partial index %s", indexDef)
	}

	var statusCheck string
	if err := db.Migrator.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'erp.stock_reservations'::regclass AND contype = 'c' AND conname LIKE '%status%'`).Scan(&statusCheck); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"pending", "active", "released", "consumed"} {
		if !strings.Contains(statusCheck, word) {
			t.Fatalf("status check %s missing %s", statusCheck, word)
		}
	}

	var cols int
	if err := db.Migrator.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'erp' AND table_name = 'stock_reservations'
		  AND column_name IN ('order_line_id', 'delivery_note_id')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatalf("stock lifecycle columns present: %d", cols)
	}
}

func indexOf(items []string, want string) (int, bool) {
	for i, item := range items {
		if item == want {
			return i, true
		}
	}
	return 0, false
}
