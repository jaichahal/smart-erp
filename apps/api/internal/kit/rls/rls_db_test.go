package rls_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func TestSessionVariablesAreLocalToTransaction(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	p := rls.Principal{UserID: "u@example.com", CompanyID: uuid.New(), Roles: []string{"accountant", "approver"}}

	err := rls.Tx(ctx, db.App, p, func(tx pgx.Tx) error {
		var company uuid.UUID
		var user string
		var roles []string
		if err := tx.QueryRow(ctx, `SELECT erp.current_company(), erp.current_user_id(), erp.current_roles()`).Scan(&company, &user, &roles); err != nil {
			return err
		}
		if company != p.CompanyID || user != p.UserID || len(roles) != 2 || roles[1] != "approver" {
			t.Fatalf("session vars wrong: %s %s %v", company, user, roles)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Outside any scoped transaction the accessors return NULL / empty, never a stale principal.
	var company *uuid.UUID
	var user *string
	if err := db.App.QueryRow(ctx, `SELECT erp.current_company(), erp.current_user_id()`).Scan(&company, &user); err != nil {
		t.Fatal(err)
	}
	if company != nil || user != nil {
		t.Fatalf("principal leaked across transactions: %v %v", company, user)
	}
}

func TestRoleNamesAreValidated(t *testing.T) {
	db := testdb.New(t)
	p := rls.Principal{UserID: "u", CompanyID: uuid.New(), Roles: []string{"admin,superuser"}}
	if _, err := rls.Begin(context.Background(), db.App, p); err == nil {
		t.Fatal("role with a comma must be rejected")
	}
}
