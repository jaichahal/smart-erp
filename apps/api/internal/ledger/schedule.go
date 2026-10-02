package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
)

// PostAccrual posts the accrual rule and schedules reversal on the next period's first day.
func (s *Service) PostAccrual(ctx context.Context, p rls.Principal, in SchedulePosting) (uuid.UUID, error) {
	return s.postScheduled(ctx, p, "accrual", in)
}

// PostPrepayment posts the prepayment rule and schedules the same reversal.
func (s *Service) PostPrepayment(ctx context.Context, p rls.Principal, in SchedulePosting) (uuid.UUID, error) {
	return s.postScheduled(ctx, p, "prepayment", in)
}

func (s *Service) postScheduled(ctx context.Context, p rls.Principal, kind string, in SchedulePosting) (uuid.UUID, error) {
	if in.DocID == "" || in.Amount == "" || in.Amount == "0" || in.Amount == "0.00" {
		return uuid.Nil, apierr.New(apierr.ValidationError, "amount is required")
	}
	rule, err := s.ResolvePostingRule(ctx, p, RuleMatch{DocType: kind})
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.periods.CheckPosting(ctx, p, periods.Posting{
		PostingDate: in.PostingDate, DocType: kind, DocID: in.DocID,
	}, "en"); err != nil {
		return uuid.Nil, err
	}
	ctx = rls.WithPrincipal(ctx, p)
	journalID := uuid.New()
	err = rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		periodID, err := openPeriodID(ctx, tx, p.CompanyID, in.PostingDate)
		if err != nil {
			return err
		}
		var reverseOn time.Time
		if err := tx.QueryRow(ctx, `SELECT next.start_date
			FROM erp.periods cur
			JOIN erp.periods next
			  ON next.company_id = cur.company_id
			 AND next.kind = 'month'
			 AND next.start_date = cur.end_date + 1
			WHERE cur.id = $1`, periodID).Scan(&reverseOn); err != nil {
			return apierr.New(apierr.MissingConfig, "next period is not open")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, kind, created_by, rule_version_id, description)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			journalID, p.CompanyID, kind, in.DocID, periodID, in.PostingDate.UTC().Format("2006-01-02"),
			kind, p.UserID, rule.ID, in.DocID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
			(journal_id, company_id, line_no, account_id, debit, credit, currency)
			VALUES ($1,$2,1,$3,$4,0,'AED'), ($1,$2,2,$5,0,$4,'AED')`,
			journalID, p.CompanyID, rule.DebitAccountID, in.Amount, rule.CreditAccountID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.scheduled_reversals
			(company_id, kind, source_journal_id, reverse_on)
			VALUES ($1,$2,$3,$4)`,
			p.CompanyID, kind, journalID, reverseOn.UTC().Format("2006-01-02"))
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return journalID, nil
}

// ReverseDue posts reversals whose first day is on or before asOf. Each source posts once.
func (s *Service) ReverseDue(ctx context.Context, p rls.Principal, asOf time.Time) (int, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var n int
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, source_journal_id, reverse_on
			FROM erp.scheduled_reversals
			WHERE company_id = $1 AND reversal_journal_id IS NULL AND reverse_on <= $2
			ORDER BY reverse_on, id
			FOR UPDATE`, p.CompanyID, asOf.UTC().Format("2006-01-02"))
		if err != nil {
			return err
		}
		defer rows.Close()
		type due struct {
			id     uuid.UUID
			source uuid.UUID
			on     time.Time
		}
		var batch []due
		for rows.Next() {
			var row due
			if err := rows.Scan(&row.id, &row.source, &row.on); err != nil {
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, row := range batch {
			reversalID := uuid.New()
			periodID, err := openPeriodID(ctx, tx, p.CompanyID, row.on)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
				(id, company_id, doc_type, doc_id, period_id, posting_date, kind, created_by, description, reverses_journal_id)
				SELECT $1, company_id, doc_type || '_reversal', doc_id || '-rev', $2, $3, 'reversal', $4, description, id
				FROM erp.journals WHERE id = $5`,
				reversalID, periodID, row.on.UTC().Format("2006-01-02"), p.UserID, row.source); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
				(journal_id, company_id, line_no, account_id, debit, credit, currency, fx_rate, party_id, dimension_value_id)
				SELECT $1, company_id, line_no, account_id, credit, debit, currency, fx_rate, party_id, dimension_value_id
				FROM erp.journal_lines WHERE journal_id = $2`,
				reversalID, row.source); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE erp.scheduled_reversals SET reversal_journal_id = $2 WHERE id = $1`,
				row.id, reversalID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
