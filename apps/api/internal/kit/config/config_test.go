package config

import (
	"strings"
	"testing"
)

func TestMissingNamedInOneError(t *testing.T) {
	t.Setenv("ERP_MIGRATOR_DATABASE_URL", "")
	t.Setenv("ERP_ADMIN_DATABASE_URL", "")
	_, err := Load(MigrateProfile)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, k := range []string{"ERP_ADMIN_DATABASE_URL", "ERP_MIGRATOR_DATABASE_URL"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("error must name %s: %v", k, err)
		}
	}
}

func TestBlankIsMissingButZeroIsNot(t *testing.T) {
	t.Setenv("ERP_MIGRATOR_DATABASE_URL", "   ")
	t.Setenv("ERP_ADMIN_DATABASE_URL", "0")
	_, err := Load(MigrateProfile)
	if err == nil || !strings.Contains(err.Error(), "ERP_MIGRATOR_DATABASE_URL") || strings.Contains(err.Error(), "ERP_ADMIN_DATABASE_URL") {
		t.Fatalf("blank must be missing, '0' must be valid: %v", err)
	}
}

func TestProdRefusesSinkPush(t *testing.T) {
	for _, k := range APIProfile.Required {
		t.Setenv(k, "x")
	}
	t.Setenv("ERP_ENV", "prod")
	t.Setenv("ERP_PUSH_MODE", "sink")
	if _, err := Load(APIProfile); err == nil {
		t.Fatal("prod with sink push must fail fast")
	}
}
