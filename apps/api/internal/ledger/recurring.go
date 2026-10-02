package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
)

// ProposeRecurring stores a setup that does not post until approved.
func (s *Service) ProposeRecurring(ctx context.Context, p rls.Principal, in RecurringProposal) (RecurringSetup, error) {
	if in.Description == "" || in.IntervalMonths < 1 || len(in.Lines) < 2 || in.End.Before(in.Start) {
		return RecurringSetup{}, apierr.New(apierr.ValidationError, "recurring setup is incomplete")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var out RecurringSetup
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO erp.recurring_journal_setups
			(company_id, description, start_date, end_date, interval_months, next_run_on, status, requested_by)
			VALUES ($1,$2,$3,$4,$5,$3,'pending_approval',$6)
			RETURNING id, status`,
			p.CompanyID, in.Description, in.Start.UTC().Format("2006-01-02"), in.End.UTC().Format("2006-01-02"),
			in.IntervalMonths, p.UserID).Scan(&out.ID, &out.Status)
		if err != nil {
			return err
		}
		for i, line := range in.Lines {
			currency := line.Currency
			if currency == "" {
				currency = "AED"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.recurring_journal_lines
				(setup_id, company_id, line_no, account_id, debit, credit, currency)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				out.ID, p.CompanyID, i+1, line.AccountID, line.Debit, line.Credit, currency); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// ApproveRecurring stores the one approval the schedule needs. The proposer cannot approve it.
func (s *Service) ApproveRecurring(ctx context.Context, p rls.Principal, setupID uuid.UUID) error {
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var requested, status string
		err := tx.QueryRow(ctx, `SELECT requested_by, status FROM erp.recurring_journal_setups
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, setupID, p.CompanyID).Scan(&requested, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "recurring setup not found")
		}
		if err != nil {
			return err
		}
		if status != "pending_approval" {
			return apierr.New(apierr.Conflict, "recurring setup is not pending approval")
		}
		if requested == p.UserID {
			return apierr.New(apierr.PermissionDenied, "the proposer cannot approve this recurring setup")
		}
		_, err = tx.Exec(ctx, `UPDATE erp.recurring_journal_setups
			SET status = 'approved', approved_by = $2, approved_at = clock_timestamp(), state_version = state_version + 1
			WHERE id = $1`, setupID, p.UserID)
		return err
	})
}

// RunRecurring posts each approved setup whose next date is due and not after the end date.
func (s *Service) RunRecurring(ctx context.Context, p rls.Principal, asOf time.Time) (int, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var n int
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE erp.recurring_journal_setups
			SET status = 'stopped', state_version = state_version + 1
			WHERE company_id = $1 AND status = 'approved' AND next_run_on > end_date`, p.CompanyID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, next_run_on, description
			FROM erp.recurring_journal_setups
			WHERE company_id = $1 AND status = 'approved' AND next_run_on <= $2 AND next_run_on <= end_date
			ORDER BY next_run_on, id
			FOR UPDATE`, p.CompanyID, asOf.UTC().Format("2006-01-02"))
		if err != nil {
			return err
		}
		defer rows.Close()
		type due struct {
			id   uuid.UUID
			on   time.Time
			desc string
		}
		var batch []due
		for rows.Next() {
			var row due
			if err := rows.Scan(&row.id, &row.on, &row.desc); err != nil {
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, row := range batch {
			if err := s.periods.CheckPosting(ctx, p, periods.Posting{
				PostingDate: row.on, DocType: "recurring_journal", DocID: row.id.String(),
			}, "en"); err != nil {
				return err
			}
			journalID := uuid.New()
			periodID, err := openPeriodID(ctx, tx, p.CompanyID, row.on)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
				(id, company_id, doc_type, doc_id, period_id, posting_date, description, kind, created_by)
				VALUES ($1,$2,'recurring_journal',$3,$4,$5,$6,'recurring',$7)`,
				journalID, p.CompanyID, row.id.String()+":"+row.on.UTC().Format("2006-01-02"),
				periodID, row.on.UTC().Format("2006-01-02"), row.desc, p.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
				(journal_id, company_id, line_no, account_id, debit, credit, currency)
				SELECT $1, company_id, line_no, account_id, debit, credit, currency
				FROM erp.recurring_journal_lines WHERE setup_id = $2`,
				journalID, row.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.recurring_journal_runs (setup_id, company_id, run_on, journal_id)
				VALUES ($1,$2,$3,$4)`, row.id, p.CompanyID, row.on.UTC().Format("2006-01-02"), journalID); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE erp.recurring_journal_setups
				SET next_run_on = next_run_on + make_interval(months => interval_months),
				    status = CASE
				        WHEN next_run_on + make_interval(months => interval_months) > end_date THEN 'stopped'
				        ELSE status
				    END,
				    state_version = state_version + 1
				WHERE id = $1 AND status = 'approved'`, row.id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return apierr.New(apierr.Conflict, "recurring setup did not advance")
			}
			n++
		}
		return nil
	})
	return n, err
}
