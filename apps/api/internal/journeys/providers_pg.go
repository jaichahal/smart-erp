package journeys

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// PostgresProviders reads personas, permissions, and approval decisions from
// the database. Client request bodies are never a source. A missing approval
// row is pending. The opening trial balance stays non-zero until the ledger
// module reports otherwise through Books.
func PostgresProviders(pool *pgxpool.Pool) Providers {
	return Providers{
		Personas:    dbPersonas{pool: pool},
		Permissions: dbPerms{pool: pool},
		Approvals:   dbApprovals{pool: pool},
		Books:       dbBooks{},
	}
}

type dbPersonas struct{ pool *pgxpool.Pool }

// List returns personas the server has stored for this user: role rows and the
// identity persona list. The user id must be the principal on the context.
func (d dbPersonas) List(ctx context.Context, userID string) ([]string, error) {
	p, err := rls.FromContext(ctx)
	if err != nil || p.UserID != userID || d.pool == nil {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT DISTINCT persona FROM (
			SELECT persona FROM erp.roles WHERE name = ANY($1::text[])
			UNION
			SELECT unnest(personas) FROM erp.identity_users WHERE id = $2
		) personas
		WHERE persona <> ''
		ORDER BY persona`, p.Roles, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var persona string
		if err := rows.Scan(&persona); err != nil {
			return nil, err
		}
		out = append(out, persona)
	}
	return out, rows.Err()
}

type dbPerms struct{ pool *pgxpool.Pool }

// Allowed is true only when one of the principal's roles holds permission in
// this company. A body field named permissions is never consulted.
func (d dbPerms) Allowed(ctx context.Context, userID, companyID, permission string) (bool, error) {
	p, err := rls.FromContext(ctx)
	if err != nil || d.pool == nil || p.UserID != userID || p.CompanyID.String() != companyID {
		return false, nil
	}
	var ok bool
	err = d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM erp.role_permissions
			WHERE role_name = ANY($1::text[]) AND permission = $2
		)`, p.Roles, permission).Scan(&ok)
	if err != nil {
		return false, err
	}
	return ok, nil
}

type dbApprovals struct{ pool *pgxpool.Pool }

// Status reads the latest approval_requests.state for the server-generated
// reference. No row is pending. Client input is not a decision.
func (d dbApprovals) Status(ctx context.Context, companyID, ref string) (Decision, error) {
	p, err := rls.FromContext(ctx)
	if err != nil || d.pool == nil || p.CompanyID.String() != companyID {
		return Pending, nil
	}
	var state string
	err = rls.Tx(ctx, d.pool, p, func(tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `
			SELECT state FROM erp.approval_requests
			WHERE company_id = $1 AND request_id = $2
			ORDER BY state_version DESC
			LIMIT 1`, companyID, ref).Scan(&state)
		if errors.Is(qerr, pgx.ErrNoRows) {
			state = ""
			return nil
		}
		return qerr
	})
	if err != nil {
		return Pending, err
	}
	switch strings.ToLower(state) {
	case string(Approved):
		return Approved, nil
	case string(Rejected):
		return Rejected, nil
	default:
		return Pending, nil
	}
}

type dbBooks struct{}

// TrialBalanceNetsToZero stays false. The ledger module owns the real total;
// this engine does not invent one from client input.
func (dbBooks) TrialBalanceNetsToZero(context.Context, string) (bool, error) {
	return false, nil
}
