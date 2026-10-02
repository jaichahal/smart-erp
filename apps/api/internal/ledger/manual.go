package ledger

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
)

// ManualLine is one side of a manual journal.
type ManualLine struct {
	AccountID uuid.UUID
	Debit     string
	Credit    string
	Currency  string
}

// ManualJournal is submitted only with a reason and an attachment (R4.10).
type ManualJournal struct {
	PostingDate   time.Time
	Description   string
	Reason        string
	AttachmentKey string
	Lines         []ManualLine
}

// ManualException is one exceptions-report row for a manual journal.
type ManualException struct {
	JournalID uuid.UUID
	UserID    string
	Reason    string
}

// SubmitManualJournal posts a manual journal and lists it on the exceptions report.
func (s *Service) SubmitManualJournal(ctx context.Context, p rls.Principal, in ManualJournal) (uuid.UUID, error) {
	if strings.TrimSpace(in.Reason) == "" {
		return uuid.Nil, apierr.New(apierr.ValidationError, "a manual journal requires a reason")
	}
	if strings.TrimSpace(in.AttachmentKey) == "" {
		return uuid.Nil, apierr.New(apierr.ValidationError, "a manual journal requires an attachment")
	}
	if len(in.Lines) < 2 {
		return uuid.Nil, apierr.New(apierr.ValidationError, "a manual journal requires lines")
	}
	if err := s.periods.CheckPosting(ctx, p, periods.Posting{
		PostingDate: in.PostingDate, DocType: "manual_journal", DocID: "manual",
	}, "en"); err != nil {
		return uuid.Nil, err
	}
	ctx = rls.WithPrincipal(ctx, p)
	journalID := uuid.New()
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		periodID, err := openPeriodID(ctx, tx, p.CompanyID, in.PostingDate)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, description, kind, reason, attachment_key, created_by)
			VALUES ($1,$2,'manual_journal',$3,$4,$5,$6,'manual',$7,$8,$9)`,
			journalID, p.CompanyID, journalID.String(), periodID, in.PostingDate.UTC().Format("2006-01-02"),
			in.Description, in.Reason, in.AttachmentKey, p.UserID); err != nil {
			return err
		}
		for i, line := range in.Lines {
			currency := line.Currency
			if currency == "" {
				currency = "AED"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
				(journal_id, company_id, line_no, account_id, debit, credit, currency)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				journalID, p.CompanyID, i+1, line.AccountID, line.Debit, line.Credit, currency); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.ledger_exceptions
			(company_id, kind, period_id, posting_date, doc_type, doc_id, actor_id, reason)
			VALUES ($1, 'manual_journal', $2, $3, 'manual_journal', $4, $5, $6)`,
			p.CompanyID, periodID, in.PostingDate.UTC().Format("2006-01-02"), journalID.String(), p.UserID, in.Reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return journalID, nil
}

// ManualJournalExceptions lists manual journals on the exceptions report for the month of day.
func (s *Service) ManualJournalExceptions(ctx context.Context, p rls.Principal, day time.Time) ([]ManualException, error) {
	start := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	ctx = rls.WithPrincipal(ctx, p)
	var out []ManualException
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT doc_id, actor_id, reason FROM erp.ledger_exceptions
			WHERE company_id = $1 AND kind = 'manual_journal' AND occurred_at >= $2 AND occurred_at < $3
			ORDER BY occurred_at, actor_id`, p.CompanyID, start, end)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var docID string
			var row ManualException
			if err := rows.Scan(&docID, &row.UserID, &row.Reason); err != nil {
				return err
			}
			row.JournalID, err = uuid.Parse(docID)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}
