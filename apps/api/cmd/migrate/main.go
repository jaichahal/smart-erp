// migrate applies goose migrations as erp_migrator, the superuser-only migrations
// (event triggers) as the admin role, River's schema, and refreshes erp_template.
//
//	migrate up            apply everything (default)
//	migrate template      recreate erp_template from the erp database
//	migrate new-immutable erp.table_name   print a scaffold migration for an immutable table
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/immutable"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/obs"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}
	if cmd == "new-immutable" {
		if len(args) < 2 || !strings.Contains(args[1], ".") {
			return fmt.Errorf("usage: migrate new-immutable schema.table")
		}
		parts := strings.SplitN(args[1], ".", 2)
		fmt.Print(immutable.MigrationFile(immutable.New(parts[0], parts[1])))
		return nil
	}
	cfg, err := config.Load(config.MigrateProfile)
	if err != nil {
		return err
	}
	log := obs.Logger(cfg.LogLevel, "migrate", cfg.Version)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	switch cmd {
	case "up":
		if err := gooseRun(cfg.MigratorDatabaseURL, "."); err != nil {
			return fmt.Errorf("migrator migrations: %w", err)
		}
		log.Info("migrator migrations applied")
		erpDB, err := targetDB(cfg.MigratorDatabaseURL)
		if err != nil {
			return err
		}
		adminOnErp, err := withDB(cfg.AdminDatabaseURL, erpDB)
		if err != nil {
			return err
		}
		if err := gooseRun(adminOnErp, "superuser"); err != nil {
			return fmt.Errorf("superuser migrations: %w", err)
		}
		log.Info("superuser migrations applied")
		pool, err := pgxpool.New(ctx, cfg.MigratorDatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := outbox.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("river migrations: %w", err)
		}
		log.Info("river migrations applied")
		return refreshTemplate(ctx, cfg.AdminDatabaseURL, erpDB, log)
	case "template":
		erpDB, err := targetDB(cfg.MigratorDatabaseURL)
		if err != nil {
			return err
		}
		return refreshTemplate(ctx, cfg.AdminDatabaseURL, erpDB, log)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func gooseRun(dsn, dir string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	table := "goose_db_version"
	if dir != "." {
		table = "goose_db_version_" + strings.ReplaceAll(dir, "/", "_")
	}
	goose.SetTableName(table)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, dir)
}

// refreshTemplate rebuilds erp_template from erp so tests clone a migrated schema.
func refreshTemplate(ctx context.Context, adminDSN, erpDB string, log interface{ Info(string, ...any) }) error {
	pool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	stmts := []string{
		`UPDATE pg_database SET datistemplate = false WHERE datname = 'erp_template'`,
		`DROP DATABASE IF EXISTS erp_template WITH (FORCE)`,
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, erpDB),
		fmt.Sprintf(`CREATE DATABASE erp_template TEMPLATE %s OWNER erp_migrator IS_TEMPLATE true`, erpDB),
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("template: %s: %w", s, err)
		}
	}
	log.Info("erp_template refreshed")
	return nil
}

func targetDB(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

func withDB(dsn, db string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + db
	return u.String(), nil
}
