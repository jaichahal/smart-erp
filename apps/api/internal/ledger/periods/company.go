package periods

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// CreateCompany inserts the company row for the principal's company.
func (s *Service) CreateCompany(ctx context.Context, p rls.Principal, legalName, lang string) error {
	if legalName == "" || p.CompanyID.String() == "" {
		return fail(lang, apierr.ValidationError, "company_invalid")
	}
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.companies (id, legal_name) VALUES ($1, $2)`, p.CompanyID, legalName)
		if err != nil {
			return fmt.Errorf("periods: insert company: %w", err)
		}
		return nil
	})
}

// OpenFiscalYear creates twelve monthly periods beginning on the first day of start's month.
func (s *Service) OpenFiscalYear(ctx context.Context, p rls.Principal, year int, start time.Time, lang string) error {
	if year < 2000 || year > 2200 {
		return fail(lang, apierr.ValidationError, "year_invalid")
	}
	start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 12, -1)
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO erp.fiscal_years (company_id, year, start_date, end_date) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			p.CompanyID, year, ymd(start), ymd(end))
		if err != nil {
			return fmt.Errorf("periods: insert fiscal year: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fail(lang, apierr.Conflict, "year_invalid")
		}
		for i := 0; i < 12; i++ {
			ps := start.AddDate(0, i, 0)
			pe := ps.AddDate(0, 1, -1)
			month := int(ps.Month())
			if _, err := tx.Exec(ctx, `INSERT INTO erp.periods (company_id, fiscal_year, month, kind, status, start_date, end_date)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				p.CompanyID, year, month, kindMonth, statusOpen, ymd(ps), ymd(pe)); err != nil {
				return fmt.Errorf("periods: insert period: %w", err)
			}
		}
		return nil
	})
}

func ymd(t time.Time) string { return t.UTC().Format("2006-01-02") }

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
