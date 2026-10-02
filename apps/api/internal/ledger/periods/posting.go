package periods

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// CheckPosting refuses a hard-closed or soft-closed period with PERIOD_CLOSED (C3).
// A prior open period requires RoleBackdate and is written to the exceptions report (C4).
func (s *Service) CheckPosting(ctx context.Context, p rls.Principal, in Posting, lang string) error {
	if !docTypeRe.MatchString(in.DocType) || in.DocID == "" {
		return fail(lang, apierr.ValidationError, "doc_type_invalid")
	}
	ctx = rls.WithPrincipal(ctx, p)
	day := dateOnly(in.PostingDate)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		period, err := periodOn(ctx, tx, p.CompanyID, day)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(lang, apierr.NotFound, "period_not_found")
		}
		if err != nil {
			return err
		}
		if period.Status != statusOpen {
			closed := fail(lang, apierr.PeriodClosed, "period_closed")
			var ae *apierr.Error
			if errors.As(closed, &ae) {
				return ae.WithDetails(map[string]any{"period_id": period.ID, "status": period.Status})
			}
			return closed
		}
		prior, err := isPriorOpen(ctx, tx, p.CompanyID, period)
		if err != nil {
			return err
		}
		if !prior {
			return nil
		}
		if !hasRole(p, RoleBackdate) {
			return fail(lang, apierr.PermissionDenied, "period_backdate_denied")
		}
		periodID, err := uuid.Parse(period.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.ledger_exceptions (company_id, kind, period_id, posting_date, doc_type, doc_id, actor_id, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.CompanyID, reasonBackdated, periodID, ymd(day), in.DocType, in.DocID, p.UserID, reasonBackdated); err != nil {
			return fmt.Errorf("periods: exception: %w", err)
		}
		if _, err := audit.Emit(ctx, tx, audit.Event{
			Type: reasonBackdated, ReferenceType: "posting", ReferenceID: in.DocID,
			After:  map[string]any{"period_id": period.ID, "posting_date": ymd(day), "doc_type": in.DocType},
			Reason: reasonBackdated,
		}); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, p, "exception.raised", in.DocType, in.DocID, period.StateVersion, "HIGH", map[string]any{
			"period_id": period.ID, "posting_date": ymd(day), "kind": reasonBackdated,
		}, lang)
	})
}

// ListExceptions returns exceptions-report rows whose occurred_at falls in month.
func (s *Service) ListExceptions(ctx context.Context, p rls.Principal, month time.Time, lang string) ([]Exception, error) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	ctx = rls.WithPrincipal(ctx, p)
	var out []Exception
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, kind, period_id, posting_date, doc_type, doc_id, actor_id, reason, occurred_at
			FROM erp.ledger_exceptions WHERE company_id = $1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at`,
			p.CompanyID, start, end)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row Exception
			if err := rows.Scan(&row.ID, &row.Kind, &row.PeriodID, &row.PostingDate, &row.DocType, &row.DocID, &row.ActorID, &row.ReasonCode, &row.OccurredAt); err != nil {
				return err
			}
			row.Reason = tr(lang, "reason_backdated")
			if row.ReasonCode != reasonBackdated {
				row.Reason = row.ReasonCode
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

type datedPeriod struct {
	Period
	Start time.Time
	End   time.Time
}

func periodOn(ctx context.Context, tx pgx.Tx, company uuid.UUID, day time.Time) (datedPeriod, error) {
	rows, err := tx.Query(ctx, `SELECT id, fiscal_year, month, kind, status, start_date, end_date, state_version
		FROM erp.periods WHERE company_id = $1 AND start_date <= $2 AND end_date >= $2 FOR UPDATE`, company, ymd(day))
	if err != nil {
		return datedPeriod{}, err
	}
	defer rows.Close()
	var found []datedPeriod
	for rows.Next() {
		p, err := scanDated(rows)
		if err != nil {
			return datedPeriod{}, err
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return datedPeriod{}, err
	}
	if len(found) == 0 {
		return datedPeriod{}, pgx.ErrNoRows
	}
	if len(found) > 1 {
		return datedPeriod{}, fmt.Errorf("periods: more than one period covers the date")
	}
	return found[0], nil
}

func isPriorOpen(ctx context.Context, tx pgx.Tx, company uuid.UUID, period datedPeriod) (bool, error) {
	var today time.Time
	if err := tx.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date`).Scan(&today); err != nil {
		return false, err
	}
	var curID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM erp.periods
		WHERE company_id = $1 AND status = $2 AND start_date <= $3 AND end_date >= $3`,
		company, statusOpen, ymd(today)).Scan(&curID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if curID.String() == period.ID {
		return false, nil
	}
	var curStart time.Time
	if err := tx.QueryRow(ctx, `SELECT start_date FROM erp.periods WHERE id = $1`, curID).Scan(&curStart); err != nil {
		return false, err
	}
	return period.EndDate < ymd(curStart), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDated(rows rowScanner) (datedPeriod, error) {
	var p datedPeriod
	var id uuid.UUID
	var month sql.NullInt32
	err := rows.Scan(&id, &p.FiscalYear, &month, &p.Kind, &p.Status, &p.Start, &p.End, &p.StateVersion)
	if err != nil {
		return datedPeriod{}, err
	}
	p.ID = id.String()
	p.StartDate = ymd(p.Start)
	p.EndDate = ymd(p.End)
	if month.Valid {
		m := int(month.Int32)
		p.Month = &m
	}
	return p, nil
}
