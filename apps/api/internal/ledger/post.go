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

// PostByRule posts a balanced journal using the current approved rule and stores that version id.
func (s *Service) PostByRule(ctx context.Context, p rls.Principal, in RulePosting) (uuid.UUID, error) {
	if in.DocID == "" || in.Net == "" {
		return uuid.Nil, apierr.New(apierr.ValidationError, "posting is incomplete")
	}
	if in.Tax == "" {
		in.Tax = "0.00"
	}
	rule, err := s.ResolvePostingRule(ctx, p, in.Match)
	if err != nil {
		return uuid.Nil, err
	}
	if in.Tax != "0.00" && in.Tax != "0" && rule.TaxAccountID == nil {
		return uuid.Nil, apierr.New(apierr.MissingConfig, "posting rule has no tax account")
	}
	if err := s.periods.CheckPosting(ctx, p, periods.Posting{
		PostingDate: in.PostingDate, DocType: in.Match.DocType, DocID: in.DocID,
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
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, kind, created_by, rule_version_id, description)
			VALUES ($1,$2,$3,$4,$5,$6,'invoice',$7,$8,$9)`,
			journalID, p.CompanyID, in.Match.DocType, in.DocID, periodID, in.PostingDate.Format("2006-01-02"),
			p.UserID, rule.ID, in.DocID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
			(journal_id, company_id, line_no, account_id, debit, credit, currency)
			VALUES ($1,$2,1,$3, ($4::numeric + $5::numeric), 0, 'AED')`,
			journalID, p.CompanyID, rule.DebitAccountID, in.Net, in.Tax); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
			(journal_id, company_id, line_no, account_id, debit, credit, currency)
			VALUES ($1,$2,2,$3,0,$4,'AED')`,
			journalID, p.CompanyID, rule.CreditAccountID, in.Net); err != nil {
			return err
		}
		if rule.TaxAccountID != nil && in.Tax != "0.00" && in.Tax != "0" {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
				(journal_id, company_id, line_no, account_id, debit, credit, currency)
				VALUES ($1,$2,3,$3,0,$4,'AED')`,
				journalID, p.CompanyID, *rule.TaxAccountID, in.Tax); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return journalID, nil
}

func openPeriodID(ctx context.Context, tx pgx.Tx, company uuid.UUID, day time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	var status string
	err := tx.QueryRow(ctx, `SELECT id, status FROM erp.periods
		WHERE company_id = $1 AND start_date <= $2 AND end_date >= $2 AND kind = 'month'`,
		company, day.UTC().Format("2006-01-02")).Scan(&id, &status)
	if err != nil {
		return uuid.Nil, err
	}
	if status != "open" {
		return uuid.Nil, apierr.New(apierr.PeriodClosed, "period is closed")
	}
	return id, nil
}
