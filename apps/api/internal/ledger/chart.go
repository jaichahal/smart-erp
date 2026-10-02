package ledger

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// TaxCode is a configured UAE tax treatment.
type TaxCode struct {
	Code string
	Name string
	Rate string
}

// Dimension is a configurable accounting dimension. Cost centre is one of them.
type Dimension struct {
	Code string
	Name string
}

// SeedUAEChart loads the trading chart, the five tax codes, and a cost centre.
// It returns account ids by posting role. Account codes stay in the migration.
func (s *Service) SeedUAEChart(ctx context.Context, p rls.Principal) (map[string]uuid.UUID, error) {
	ctx = rls.WithPrincipal(ctx, p)
	out := map[string]uuid.UUID{}
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT posting_role, account_id FROM erp.seed_uae_trading_chart($1)`, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			var id uuid.UUID
			if err := rows.Scan(&role, &id); err != nil {
				return err
			}
			out[role] = id
		}
		return rows.Err()
	})
	return out, err
}

// TaxCodes lists the company's tax codes.
func (s *Service) TaxCodes(ctx context.Context, p rls.Principal) ([]TaxCode, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out []TaxCode
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code, name, rate::text FROM erp.tax_codes WHERE company_id = $1 ORDER BY code`, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row TaxCode
			if err := rows.Scan(&row.Code, &row.Name, &row.Rate); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

// Dimensions lists dimension definitions.
func (s *Service) Dimensions(ctx context.Context, p rls.Principal) ([]Dimension, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out []Dimension
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code, name FROM erp.dimension_definitions WHERE company_id = $1 ORDER BY code`, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row Dimension
			if err := rows.Scan(&row.Code, &row.Name); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

// ProposeAccountName stores a pending rename. The current name stays until a different user approves it.
func (s *Service) ProposeAccountName(ctx context.Context, p rls.Principal, accountID uuid.UUID, name, reason string) (uuid.UUID, error) {
	if name == "" || reason == "" {
		return uuid.Nil, apierr.New(apierr.ValidationError, "account change is incomplete")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var id uuid.UUID
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO erp.account_versions
			(company_id, account_id, name, status, requested_by, change_reason)
			VALUES ($1,$2,$3,'pending_approval',$4,$5) RETURNING id`,
			p.CompanyID, accountID, name, p.UserID, reason).Scan(&id)
	})
	return id, err
}

// ApproveAccountName applies a pending rename when the approver is not the proposer.
func (s *Service) ApproveAccountName(ctx context.Context, p rls.Principal, versionID uuid.UUID) error {
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var requested, status, name string
		var accountID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT requested_by, status, name, account_id FROM erp.account_versions
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, versionID, p.CompanyID).Scan(&requested, &status, &name, &accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "account change not found")
		}
		if err != nil {
			return err
		}
		if status != "pending_approval" {
			return apierr.New(apierr.Conflict, "account change is not pending")
		}
		if requested == p.UserID {
			return apierr.New(apierr.PermissionDenied, "the proposer cannot approve this account change")
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.account_versions
			SET status = 'approved', approved_by = $2, approved_at = clock_timestamp() WHERE id = $1`, versionID, p.UserID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE erp.accounts SET name = $2, state_version = state_version + 1
			WHERE id = $1 AND company_id = $3`, accountID, name, p.CompanyID)
		return err
	})
}
