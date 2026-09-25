package identity

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type sqlDirectory struct {
	pool *pgxpool.Pool
}

// ByLogin returns the ERP directory account for a login name.
func (d sqlDirectory) ByLogin(ctx context.Context, loginName string) (Account, error) {
	return d.one(ctx, `SELECT id, login_name, display_name, company_id, roles, personas, disabled, step_up_methods FROM erp.identity_users WHERE login_name=$1`, loginName)
}

// ByID returns the ERP directory account for a user id.
func (d sqlDirectory) ByID(ctx context.Context, id string) (Account, error) {
	return d.one(ctx, `SELECT id, login_name, display_name, company_id, roles, personas, disabled, step_up_methods FROM erp.identity_users WHERE id=$1`, id)
}

func (d sqlDirectory) one(ctx context.Context, q, arg string) (Account, error) {
	var a Account
	err := d.pool.QueryRow(ctx, q, arg).Scan(&a.ID, &a.LoginName, &a.Name, &a.CompanyID, &a.Roles, &a.Personas, &a.Disabled, &a.StepUpMethods)
	if err == pgx.ErrNoRows {
		return Account{}, ErrUnknown
	}
	if err != nil {
		return Account{}, err
	}
	return a, nil
}

// UpsertUser stores the ERP directory row keyed by the Zitadel user id.
func (s *Service) UpsertUser(ctx context.Context, a Account) error {
	return rls.Tx(ctx, s.pool, sqlPrincipal(rls.Principal{UserID: a.ID, CompanyID: a.CompanyID, Roles: a.Roles}), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.identity_users(id, login_name, display_name, company_id, roles, personas, disabled, step_up_methods)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE SET login_name=$2, display_name=$3, company_id=$4, roles=$5, personas=$6, disabled=$7, step_up_methods=$8`,
			a.ID, a.LoginName, a.Name, a.CompanyID, rolesOrEmpty(a.Roles), rolesOrEmpty(a.Personas), a.Disabled, rolesOrEmpty(a.StepUpMethods))
		return err
	})
}

func rolesOrEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func normalizeMethods(methods []string) []string {
	if len(methods) == 0 {
		return []string{"totp"}
	}
	out := make([]string, 0, len(methods))
	for _, m := range methods {
		switch strings.ToLower(m) {
		case "totp", "webauthn", "biometric":
			out = append(out, strings.ToLower(m))
		}
	}
	if len(out) == 0 {
		return []string{"totp"}
	}
	return out
}
