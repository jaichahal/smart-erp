package immutable

import (
	"strings"
	"testing"
)

func TestCreateTableSQLHasGuardsAndChain(t *testing.T) {
	sql := CreateTableSQL(New("erp", "widgets", Column{"name", "text NOT NULL"}))
	for _, want := range []string{
		"CREATE TABLE erp.widgets",
		"id uuid PRIMARY KEY DEFAULT gen_random_uuid()",
		"company_id uuid NOT NULL",
		"chain_seq bigint NOT NULL",
		"hash text NOT NULL CHECK (length(hash) = 64)",
		"UNIQUE (company_id, chain_seq)",
		"SELECT erp.make_immutable('erp.widgets');",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestMigrationFileHasUpAndDown(t *testing.T) {
	f := MigrationFile(New("erp", "widgets"))
	if !strings.HasPrefix(f, "-- +goose Up") || !strings.Contains(f, "-- +goose Down") || !strings.Contains(f, "DROP TABLE IF EXISTS erp.widgets") {
		t.Fatal(f)
	}
}
