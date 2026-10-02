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

// StockMovement changes on-hand. A result below zero is refused by the database.
type StockMovement struct {
	SKU       uuid.UUID
	Warehouse uuid.UUID
	Qty       string
	DocID     string
}

// PostStockMovement inserts one quantity movement. NEGATIVE_STOCK comes from the database trigger.
func (s *Service) PostStockMovement(ctx context.Context, p rls.Principal, in StockMovement) error {
	if in.SKU == uuid.Nil || in.Warehouse == uuid.Nil || in.Qty == "" || in.DocID == "" {
		return apierr.New(apierr.ValidationError, "stock movement is incomplete")
	}
	ctx = rls.WithPrincipal(ctx, p)
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.stock_qty_movements
			(company_id, sku_id, warehouse_id, qty_delta, source_doc_id)
			VALUES ($1,$2,$3,$4,$5)`, p.CompanyID, in.SKU, in.Warehouse, in.Qty, in.DocID)
		return err
	})
	return mapPostErr(err)
}

// OnHand is the sum of movements for the SKU in the warehouse.
func (s *Service) OnHand(ctx context.Context, p rls.Principal, sku, warehouse uuid.UUID) string {
	ctx = rls.WithPrincipal(ctx, p)
	var qty string
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(qty_delta), 0)::text FROM erp.stock_qty_movements
			WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3`, p.CompanyID, sku, warehouse).Scan(&qty)
	})
	if err != nil {
		return err.Error()
	}
	return qty
}

// CashPayment moves an amount between a cash account and another account.
type CashPayment struct {
	PostingDate      time.Time
	DocID            string
	CashAccountID    uuid.UUID
	ExpenseAccountID uuid.UUID
	Amount           string
}

// PostCashPayment credits cash. A negative cash balance is refused by the database.
func (s *Service) PostCashPayment(ctx context.Context, p rls.Principal, in CashPayment) error {
	return s.postCash(ctx, p, in, false)
}

// PostCashReceipt debits cash.
func (s *Service) PostCashReceipt(ctx context.Context, p rls.Principal, in CashPayment) error {
	return s.postCash(ctx, p, in, true)
}

func (s *Service) postCash(ctx context.Context, p rls.Principal, in CashPayment, receipt bool) error {
	if in.DocID == "" || in.Amount == "" || in.CashAccountID == uuid.Nil || in.ExpenseAccountID == uuid.Nil {
		return apierr.New(apierr.ValidationError, "cash posting is incomplete")
	}
	if err := s.periods.CheckPosting(ctx, p, periods.Posting{
		PostingDate: in.PostingDate, DocType: "cash_payment", DocID: in.DocID,
	}, "en"); err != nil {
		return err
	}
	ctx = rls.WithPrincipal(ctx, p)
	journalID := uuid.New()
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		periodID, err := openPeriodID(ctx, tx, p.CompanyID, in.PostingDate)
		if err != nil {
			return err
		}
		kind := "system"
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, kind, created_by, description)
			VALUES ($1,$2,'cash_payment',$3,$4,$5,$6,$7,$3)`,
			journalID, p.CompanyID, in.DocID, periodID, in.PostingDate.UTC().Format("2006-01-02"), kind, p.UserID); err != nil {
			return err
		}
		cashDebit, cashCredit := "0.00", in.Amount
		otherDebit, otherCredit := in.Amount, "0.00"
		if receipt {
			cashDebit, cashCredit = in.Amount, "0.00"
			otherDebit, otherCredit = "0.00", in.Amount
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
			(journal_id, company_id, line_no, account_id, debit, credit, currency)
			VALUES ($1,$2,1,$3,$4,$5,'AED'), ($1,$2,2,$6,$7,$8,'AED')`,
			journalID, p.CompanyID, in.CashAccountID, cashDebit, cashCredit, in.ExpenseAccountID, otherDebit, otherCredit); err != nil {
			return err
		}
		return nil
	})
	return mapPostErr(err)
}
