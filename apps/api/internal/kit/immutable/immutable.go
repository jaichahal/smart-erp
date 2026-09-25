// Package immutable generates the migration SQL for an immutable table so that every
// module creates them the same way (05 "Immutable tables"): owner erp_migrator,
// erp_app gets INSERT and SELECT only, BEFORE UPDATE/DELETE/TRUNCATE guards, the
// chain columns, and registration for the nightly presence check.
//
// Modules call CreateTableSQL from a goose migration file they write by hand, or
// use `go run ./cmd/migrate new-immutable <schema.table>` to scaffold one.
package immutable

import (
	"fmt"
	"strings"
)

// Column is one user column; chain columns are added automatically.
type Column struct {
	Name string
	Type string // full SQL type with constraints, e.g. "uuid NOT NULL"
}

// Table describes an immutable table to create.
type Table struct {
	Schema      string
	Name        string
	Columns     []Column
	PrimaryKey  []string // defaults to id
	CompanyCol  string   // defaults to company_id
	ExtraIndex  []string // raw index column lists, e.g. "company_id, occurred_at"
	WithChain   bool     // adds chain_seq, prev_hash, hash (default true when zero value used via New)
	Description string
}

// New returns a Table with defaults applied.
func New(schema, name string, cols ...Column) Table {
	return Table{Schema: schema, Name: name, Columns: cols, PrimaryKey: []string{"id"}, CompanyCol: "company_id", WithChain: true}
}

// CreateTableSQL returns the Up SQL. Every immutable table gets:
//   - id uuid primary key default gen_random_uuid() unless the caller defines id
//   - company_id uuid not null unless defined
//   - chain_seq bigint, prev_hash text, hash text (length 64) when WithChain
//   - UNIQUE (company_id, chain_seq) when WithChain
//   - SELECT erp.make_immutable('schema.name')
func CreateTableSQL(t Table) string {
	full := t.Schema + "." + t.Name
	has := map[string]bool{}
	for _, c := range t.Columns {
		has[c.Name] = true
	}
	var cols []string
	if !has["id"] {
		cols = append(cols, "    id uuid PRIMARY KEY DEFAULT gen_random_uuid()")
	}
	if !has[t.CompanyCol] {
		cols = append(cols, fmt.Sprintf("    %s uuid NOT NULL", t.CompanyCol))
	}
	for _, c := range t.Columns {
		cols = append(cols, fmt.Sprintf("    %s %s", c.Name, c.Type))
	}
	if t.WithChain {
		cols = append(cols,
			"    chain_seq bigint NOT NULL",
			"    prev_hash text NOT NULL",
			"    hash text NOT NULL CHECK (length(hash) = 64)",
			fmt.Sprintf("    UNIQUE (%s, chain_seq)", t.CompanyCol),
		)
	}
	if has["id"] && len(t.PrimaryKey) > 0 && (len(t.PrimaryKey) != 1 || t.PrimaryKey[0] != "id") {
		cols = append(cols, fmt.Sprintf("    PRIMARY KEY (%s)", strings.Join(t.PrimaryKey, ", ")))
	}
	var b strings.Builder
	if t.Description != "" {
		fmt.Fprintf(&b, "-- %s\n", t.Description)
	}
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);\n", full, strings.Join(cols, ",\n"))
	for i, idx := range t.ExtraIndex {
		fmt.Fprintf(&b, "CREATE INDEX %s_idx%d ON %s (%s);\n", t.Name, i+1, full, idx)
	}
	fmt.Fprintf(&b, "SELECT erp.make_immutable('%s');\n", full)
	return b.String()
}

// DropTableSQL returns the Down SQL.
func DropTableSQL(t Table) string {
	full := t.Schema + "." + t.Name
	return fmt.Sprintf("DROP TABLE IF EXISTS %s;\nDELETE FROM erp.immutable_tables WHERE table_name = '%s';\n", full, full)
}

// MigrationFile renders a complete goose migration for the table.
func MigrationFile(t Table) string {
	return "-- +goose Up\n" + CreateTableSQL(t) + "\n-- +goose Down\n" + DropTableSQL(t)
}
