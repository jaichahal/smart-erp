// Package testdb gives every test package a fresh database cloned from the
// erp_template database (10 "Local stack"): CREATE DATABASE erp_test_<rand>
// TEMPLATE erp_template. Tests connect as erp_app so grants and RLS are exercised
// exactly as in production; AdminPool and MigratorPool are available for tests
// that need to tamper (chain-break tests) or inspect.
//
// Requires ERP_ADMIN_DATABASE_URL, ERP_MIGRATOR_DATABASE_URL, ERP_DATABASE_URL and
// a migrated erp_template (just template). Set ERP_TEST_DATABASE to reuse a
// named database instead of creating one (per-agent databases, see CONTRIBUTING).
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB bundles the three connection pools to one fresh database.
type DB struct {
	Name     string
	App      *pgxpool.Pool
	Migrator *pgxpool.Pool
	Admin    *pgxpool.Pool
}

// New creates (or reuses) a test database and returns pools. It skips the test if
// the environment is not configured, so unit-only runs stay green.
func New(t testing.TB) *DB {
	t.Helper()
	admin, mig, app := os.Getenv("ERP_ADMIN_DATABASE_URL"), os.Getenv("ERP_MIGRATOR_DATABASE_URL"), os.Getenv("ERP_DATABASE_URL")
	if admin == "" || mig == "" || app == "" {
		t.Skip("database tests need ERP_ADMIN_DATABASE_URL, ERP_MIGRATOR_DATABASE_URL, ERP_DATABASE_URL")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	name := os.Getenv("ERP_TEST_DATABASE")
	created := false
	if name == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		name = "erp_test_" + hex.EncodeToString(b[:])
		if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE erp_template OWNER erp_migrator`, name)); err != nil {
			t.Fatalf("create test database from erp_template (run `just template`): %v", err)
		}
		created = true
	}
	appPool, err := pgxpool.New(ctx, withDB(app, name))
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	migPool, err := pgxpool.New(ctx, withDB(mig, name))
	if err != nil {
		t.Fatalf("migrator pool: %v", err)
	}
	dbAdmin, err := pgxpool.New(ctx, withDB(admin, name))
	if err != nil {
		t.Fatalf("db admin pool: %v", err)
	}
	t.Cleanup(func() {
		appPool.Close()
		migPool.Close()
		dbAdmin.Close()
		if created && os.Getenv("ERP_TEST_KEEP") != "1" {
			_, _ = adminPool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
		}
		adminPool.Close()
	})
	return &DB{Name: name, App: appPool, Migrator: migPool, Admin: dbAdmin}
}

func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(db, "/")
	return u.String()
}
