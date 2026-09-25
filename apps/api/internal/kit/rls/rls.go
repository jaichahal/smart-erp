// Package rls scopes every database transaction to the calling principal so that
// PostgreSQL row-level-security policies (owned by Track A2) can read
// erp.current_company(), erp.current_user_id(), and erp.current_roles().
//
// Nothing in a business module opens a transaction directly; it calls Begin or
// Tx with a Principal and gets a pgx.Tx whose session variables are already set
// with SET LOCAL, so they vanish with the transaction and can never leak between
// pooled connections.
package rls

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Principal is the authenticated caller. Roles are plain names from identity.
type Principal struct {
	UserID    string
	CompanyID uuid.UUID
	Roles     []string
}

// System is the principal used by workers and schedulers; policies treat it as trusted.
var System = Principal{UserID: "system", Roles: []string{"system"}}

type ctxKey struct{}

// WithPrincipal stores the principal on the context (set by the auth middleware).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal or an error if none was set.
func FromContext(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	if !ok {
		return Principal{}, errors.New("rls: no principal on context")
	}
	return p, nil
}

// Begin opens a transaction and applies the principal's session variables.
func Begin(ctx context.Context, pool *pgxpool.Pool, p Principal) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := Apply(ctx, tx, p); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// Apply sets the session variables on an existing transaction (SET LOCAL).
func Apply(ctx context.Context, tx pgx.Tx, p Principal) error {
	for _, r := range p.Roles {
		if strings.ContainsAny(r, ", '\"") {
			return fmt.Errorf("rls: invalid role name %q", r)
		}
	}
	company := ""
	if p.CompanyID != uuid.Nil {
		company = p.CompanyID.String()
	}
	// set_config with is_local=true is the parameterisable form of SET LOCAL.
	_, err := tx.Exec(ctx, `SELECT set_config('erp.company_id', $1, true), set_config('erp.user_id', $2, true), set_config('erp.roles', $3, true)`,
		company, p.UserID, strings.Join(p.Roles, ","))
	return err
}

// Tx runs fn inside a scoped transaction, committing on nil error.
func Tx(ctx context.Context, pool *pgxpool.Pool, p Principal, fn func(pgx.Tx) error) error {
	tx, err := Begin(ctx, pool, p)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
