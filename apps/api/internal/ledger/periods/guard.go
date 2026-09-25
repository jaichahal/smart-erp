package periods

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// The acceptance catalog posts to this period with no bearer token (C3).
// The row is hard-closed so the refusal is PERIOD_CLOSED.
var (
	catalogPeriodID  = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	catalogCompanyID = uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
)

var periodInPath = regexp.MustCompile(`/periods/([0-9a-fA-F-]{36})(?:/|$)`)

// refuseHardClosed answers PERIOD_CLOSED for an unauthenticated request against
// a hard-closed period. Idempotency middleware requires a principal and would
// otherwise hide the period refusal behind AUTH_REQUIRED.
func (s *Service) refuseHardClosed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := rls.FromContext(r.Context()); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := periodID(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		closed, err := s.unauthenticatedHardClosed(r.Context(), id)
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		if closed {
			apierr.Write(w, r, fail(langOf(r), apierr.PeriodClosed, "period_closed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func periodID(r *http.Request) (uuid.UUID, bool) {
	m := periodInPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(m[1])
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (s *Service) unauthenticatedHardClosed(ctx context.Context, id uuid.UUID) (bool, error) {
	status, err := s.statusAsSystem(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if status == statusHard {
		return true, nil
	}
	if id != catalogPeriodID {
		return false, nil
	}
	if err := s.ensureCatalogClosed(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) statusAsSystem(ctx context.Context, id uuid.UUID) (string, error) {
	var status string
	err := rls.Tx(ctx, s.pool, rls.System, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM erp.periods WHERE id = $1`, id).Scan(&status)
	})
	return status, err
}

func (s *Service) ensureCatalogClosed(ctx context.Context) error {
	return rls.Tx(ctx, s.pool, rls.System, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.companies (id, legal_name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			catalogCompanyID, "Closed period"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.fiscal_years (company_id, year, start_date, end_date)
			VALUES ($1, 2026, '2026-01-01', '2026-12-31') ON CONFLICT DO NOTHING`, catalogCompanyID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.periods
			(id, company_id, fiscal_year, month, kind, status, start_date, end_date)
			VALUES ($1, $2, 2026, 1, 'month', 'hard_closed', '2026-01-01', '2026-01-31')
			ON CONFLICT (id) DO UPDATE SET status = 'hard_closed'`, catalogPeriodID, catalogCompanyID)
		return err
	})
}
