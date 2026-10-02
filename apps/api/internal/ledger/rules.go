package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// ProposePostingRule stores a pending version. It does not change what Resolve returns.
func (s *Service) ProposePostingRule(ctx context.Context, p rls.Principal, in RuleProposal) (RuleVersion, error) {
	if in.Match.DocType == "" || in.Reason == "" || in.DebitAccountID == uuid.Nil || in.CreditAccountID == uuid.Nil {
		return RuleVersion{}, apierr.New(apierr.ValidationError, "posting rule is incomplete")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var out RuleVersion
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO erp.posting_rule_versions (
			company_id, doc_type, tax_code, item_class, dimension_value_id, party_group,
			debit_account_id, credit_account_id, tax_account_id, discount_account_id, rounding_account_id,
			priority, status, requested_by, change_reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending_approval',$13,$14)
			RETURNING id, debit_account_id, credit_account_id, tax_account_id, discount_account_id, rounding_account_id, status`,
			p.CompanyID, in.Match.DocType, in.Match.TaxCode, in.Match.ItemClass, in.Match.DimensionValueID, in.Match.PartyGroup,
			in.DebitAccountID, in.CreditAccountID, in.TaxAccountID, in.DiscountAccountID, in.RoundingAccountID,
			in.Priority, p.UserID, in.Reason)
		return scanRule(row, &out)
	})
	return out, err
}

// ApprovePostingRule requires a user other than the proposer (four-eyes).
func (s *Service) ApprovePostingRule(ctx context.Context, p rls.Principal, versionID uuid.UUID) error {
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var requested, status string
		err := tx.QueryRow(ctx, `SELECT requested_by, status FROM erp.posting_rule_versions
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, versionID, p.CompanyID).Scan(&requested, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "posting rule not found")
		}
		if err != nil {
			return err
		}
		if status != "pending_approval" {
			return apierr.New(apierr.Conflict, "posting rule is not pending approval")
		}
		if requested == p.UserID {
			return apierr.New(apierr.PermissionDenied, "the proposer cannot approve this posting rule")
		}
		_, err = tx.Exec(ctx, `UPDATE erp.posting_rule_versions
			SET status = 'approved', approved_by = $2, approved_at = clock_timestamp()
			WHERE id = $1 AND company_id = $3`, versionID, p.UserID, p.CompanyID)
		return mapRuleErr(err)
	})
}

// ResolvePostingRule returns the latest approved version for the match.
func (s *Service) ResolvePostingRule(ctx context.Context, p rls.Principal, match RuleMatch) (RuleVersion, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out RuleVersion
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, approvedRuleSQL+` LIMIT 1`,
			p.CompanyID, match.DocType, match.TaxCode, match.ItemClass, match.PartyGroup, match.DimensionValueID)
		err := scanRule(row, &out)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.MissingConfig, "no approved posting rule")
		}
		return err
	})
	return out, err
}

// PostingRuleVersion returns the stored snapshot used on reprint and audit.
func (s *Service) PostingRuleVersion(ctx context.Context, p rls.Principal, id uuid.UUID) (RuleVersion, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out RuleVersion
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT id, debit_account_id, credit_account_id, tax_account_id, discount_account_id, rounding_account_id, status
			FROM erp.posting_rule_versions WHERE id = $1 AND company_id = $2`, id, p.CompanyID)
		err := scanRule(row, &out)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "posting rule not found")
		}
		return err
	})
	return out, err
}

// RuleForJournal returns the version the journal was posted with, not the current rule.
func (s *Service) RuleForJournal(ctx context.Context, p rls.Principal, journalID uuid.UUID) (RuleVersion, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var versionID uuid.UUID
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT rule_version_id FROM erp.journals WHERE id = $1 AND company_id = $2`, journalID, p.CompanyID).Scan(&versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "journal not found")
		}
		return err
	})
	if err != nil {
		return RuleVersion{}, err
	}
	if versionID == uuid.Nil {
		return RuleVersion{}, apierr.New(apierr.NotFound, "journal has no posting rule")
	}
	return s.PostingRuleVersion(ctx, p, versionID)
}

const approvedRuleSQL = `SELECT id, debit_account_id, credit_account_id, tax_account_id, discount_account_id, rounding_account_id, status
	FROM erp.posting_rule_versions
	WHERE company_id = $1 AND status = 'approved' AND doc_type = $2
		AND (tax_code = '' OR tax_code = $3)
		AND (item_class = '' OR item_class = $4)
		AND (party_group = '' OR party_group = $5)
		AND (dimension_value_id IS NULL OR dimension_value_id IS NOT DISTINCT FROM $6)
	ORDER BY
		(CASE WHEN tax_code <> '' THEN 1 ELSE 0 END
			+ CASE WHEN item_class <> '' THEN 1 ELSE 0 END
			+ CASE WHEN party_group <> '' THEN 1 ELSE 0 END
			+ CASE WHEN dimension_value_id IS NOT NULL THEN 1 ELSE 0 END) DESC,
		priority DESC,
		created_at DESC`

func scanRule(row pgx.Row, out *RuleVersion) error {
	return row.Scan(&out.ID, &out.DebitAccountID, &out.CreditAccountID, &out.TaxAccountID, &out.DiscountAccountID, &out.RoundingAccountID, &out.Status)
}

func mapRuleErr(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23514" || pg.Code == "P0001") {
		return fmt.Errorf("ledger: %w", apierr.New(apierr.PermissionDenied, pg.Message))
	}
	return err
}
