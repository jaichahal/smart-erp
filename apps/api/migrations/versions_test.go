package migrations_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func TestDuplicateGooseVersion(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"00001_a.sql", "00001_b.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- +goose Up\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := migrations.CheckDir(dir)
	if err == nil || !strings.Contains(err.Error(), "00001") {
		t.Fatalf("duplicate version must fail naming 00001, got %v", err)
	}
}

func TestMigrationVersionsUniqueAndOrdered(t *testing.T) {
	if err := migrations.CheckDir("."); err != nil {
		t.Fatal(err)
	}
	if err := migrations.CheckDir("superuser"); err != nil {
		t.Fatal(err)
	}
}
