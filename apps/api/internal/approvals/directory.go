package approvals

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Actor is a person the engine can evaluate. Identity owns the real directory;
// this is the read model approvals needs.
type Actor struct {
	ID         string
	Name       string
	Department string
	Roles      []string
}

// Directory looks up actors. A1 can replace SQLDirectory without an import.
type Directory interface {
	Get(ctx context.Context, company uuid.UUID, userID string) (Actor, error)
}

// SQLDirectory reads erp.approval_actors under the caller's company RLS scope.
type SQLDirectory struct {
	Pool *pgxpool.Pool
}

// NewSQLDirectory returns the table-backed directory.
func NewSQLDirectory(pool *pgxpool.Pool) SQLDirectory { return SQLDirectory{Pool: pool} }

// Get returns one actor or a not-found API error.
func (d SQLDirectory) Get(ctx context.Context, company uuid.UUID, userID string) (Actor, error) {
	p := rls.Principal{UserID: userID, CompanyID: company, Roles: []string{"system"}}
	var a Actor
	err := rls.Tx(ctx, d.Pool, p, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT user_id, name, department, roles FROM erp.approval_actors WHERE company_id=$1 AND user_id=$2`, company, userID).
			Scan(&a.ID, &a.Name, &a.Department, &a.Roles)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Actor{}, apierr.New(apierr.NotFound, t(ctx, "auth.not_in_directory"))
		}
		return Actor{}, err
	}
	return a, nil
}
