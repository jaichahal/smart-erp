package periods

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// SoftClose marks the period soft-closed. Accountant only (R4.6).
func (s *Service) SoftClose(ctx context.Context, p rls.Principal, periodID uuid.UUID, expected int64, lang string) (Period, error) {
	if !hasRole(p, RoleAccountant) {
		return Period{}, fail(lang, apierr.PermissionDenied, "period_soft_denied")
	}
	return s.close(ctx, p, periodID, expected, statusSoft, "", lang)
}

// HardClose marks the period hard-closed. Stakeholder only, and only with an approval (R4.6, C3).
func (s *Service) HardClose(ctx context.Context, p rls.Principal, periodID uuid.UUID, expected int64, approvalID, lang string) (Period, error) {
	if !hasRole(p, RoleStakeholder) {
		return Period{}, fail(lang, apierr.PermissionDenied, "period_hard_denied")
	}
	if approvalID == "" {
		return Period{}, fail(lang, apierr.ValidationError, "period_approval_missing")
	}
	if s.Approvals == nil {
		return Period{}, fail(lang, apierr.MissingConfig, "approval_unconfigured")
	}
	if err := s.Approvals.Approved(ctx, p.CompanyID, periodID, approvalID); err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return Period{}, err
		}
		return Period{}, fail(lang, apierr.PermissionDenied, "period_approval_required")
	}
	return s.close(ctx, p, periodID, expected, statusHard, approvalID, lang)
}

// OpenAuditAdjustment opens the thirteenth period for the fiscal year (R4.8).
// expected is 0 when the period does not exist yet; otherwise it is the period's state_version.
func (s *Service) OpenAuditAdjustment(ctx context.Context, p rls.Principal, year int, expected int64, approvalID, lang string) (Period, error) {
	if !hasRole(p, RoleStakeholder) {
		return Period{}, fail(lang, apierr.PermissionDenied, "period_hard_denied")
	}
	if approvalID == "" {
		return Period{}, fail(lang, apierr.ValidationError, "period_approval_missing")
	}
	if s.Approvals == nil {
		return Period{}, fail(lang, apierr.MissingConfig, "approval_unconfigured")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var out Period
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var end time.Time
		err := tx.QueryRow(ctx, `SELECT end_date FROM erp.fiscal_years WHERE company_id = $1 AND year = $2`, p.CompanyID, year).Scan(&end)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(lang, apierr.NotFound, "year_invalid")
		}
		if err != nil {
			return fmt.Errorf("periods: fiscal year: %w", err)
		}
		existing, err := loadAudit(ctx, tx, p.CompanyID, year)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if expected != existing.StateVersion {
				return ifmatch.Check(expected, existing.StateVersion, existing)
			}
			if existing.Status == statusOpen {
				out = existing
				return nil
			}
			return fail(lang, apierr.Conflict, "period_conflict")
		}
		if expected != 0 {
			return ifmatch.Check(expected, 0, nil)
		}
		day := ymd(end.AddDate(0, 0, 1))
		var id uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO erp.periods (company_id, fiscal_year, month, kind, status, start_date, end_date)
			VALUES ($1, $2, NULL, $3, $4, $5, $5) RETURNING id`,
			p.CompanyID, year, kindAudit, statusOpen, day).Scan(&id)
		if err != nil {
			return fmt.Errorf("periods: open audit period: %w", err)
		}
		out, err = loadPeriod(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if err := s.Approvals.Approved(ctx, p.CompanyID, id, approvalID); err != nil {
			return fail(lang, apierr.PermissionDenied, "period_approval_required")
		}
		return nil
	})
	return out, err
}

func (s *Service) close(ctx context.Context, p rls.Principal, periodID uuid.UUID, expected int64, target, approvalID, lang string) (Period, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out Period
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		cur, err := loadPeriod(ctx, tx, p.CompanyID, periodID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(lang, apierr.NotFound, "period_not_found")
		}
		if err != nil {
			return err
		}
		if expected != cur.StateVersion {
			return ifmatch.Check(expected, cur.StateVersion, cur)
		}
		if cur.Status == target {
			out = cur
			return nil
		}
		if cur.Status == statusHard {
			return fail(lang, apierr.Conflict, "period_conflict")
		}
		tag, err := tx.Exec(ctx, `UPDATE erp.periods SET status = $1, state_version = state_version + 1, closed_at = clock_timestamp(), closed_by = $2
			WHERE id = $3 AND company_id = $4 AND state_version = $5`,
			target, p.UserID, periodID, p.CompanyID, expected)
		if err != nil {
			return fmt.Errorf("periods: close: %w", err)
		}
		if tag.RowsAffected() == 0 {
			again, loadErr := loadPeriod(ctx, tx, p.CompanyID, periodID)
			if loadErr != nil {
				return loadErr
			}
			return ifmatch.Check(expected, again.StateVersion, again)
		}
		out, err = loadPeriod(ctx, tx, p.CompanyID, periodID)
		if err != nil {
			return err
		}
		if _, err := audit.Emit(ctx, tx, audit.Event{
			Type: "period." + target, ReferenceType: "period", ReferenceID: periodID.String(),
			Before: cur, After: out, Reason: approvalID,
		}); err != nil {
			return err
		}
		kind := "soft"
		if target == statusHard {
			kind = "hard"
		}
		return s.enqueue(ctx, tx, p, "period.closed", "fiscal_period", periodID.String(), out.StateVersion, "HIGH", map[string]any{
			"period_id": periodID.String(), "fiscal_year": out.FiscalYear, "close": kind,
		}, lang)
	})
	return out, err
}

func loadPeriod(ctx context.Context, tx pgx.Tx, company, id uuid.UUID) (Period, error) {
	row := tx.QueryRow(ctx, `SELECT id, fiscal_year, month, kind, status, start_date, end_date, state_version
		FROM erp.periods WHERE company_id = $1 AND id = $2`, company, id)
	p, err := scanDated(row)
	return p.Period, err
}

func loadAudit(ctx context.Context, tx pgx.Tx, company uuid.UUID, year int) (Period, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM erp.periods WHERE company_id = $1 AND fiscal_year = $2 AND kind = $3`, company, year, kindAudit).Scan(&id)
	if err != nil {
		return Period{}, err
	}
	return loadPeriod(ctx, tx, company, id)
}
