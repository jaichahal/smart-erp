package receivables

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

type invRow struct {
	customer uuid.UUID
	open     int64
	pdc      int64
	amount   int64
	bounced  bool
	due      time.Time
}

func lockInvoice(ctx context.Context, tx pgx.Tx, company, id uuid.UUID) (invRow, error) {
	var row invRow
	err := tx.QueryRow(ctx, `
		SELECT customer_id, open_fils, pdc_covered_fils, amount_fils, bounced, due_date
		FROM erp.ar_invoices WHERE company_id = $1 AND id = $2 FOR UPDATE`, company, id).Scan(
		&row.customer, &row.open, &row.pdc, &row.amount, &row.bounced, &row.due)
	if errors.Is(err, pgx.ErrNoRows) {
		return invRow{}, apierr.New(apierr.NotFound, "Invoice not found.")
	}
	return row, err
}

func setInvoice(ctx context.Context, tx pgx.Tx, company, id uuid.UUID, open, pdc int64, bounced bool, amount int64) error {
	if open < 0 || pdc < 0 || open+pdc > amount {
		return apierr.New(apierr.ValidationError, "Allocation cannot exceed the invoice balance.")
	}
	_, err := tx.Exec(ctx, `
		UPDATE erp.ar_invoices
		SET open_fils = $3, pdc_covered_fils = $4, bounced = $5, amount_fils = $6, state_version = state_version + 1
		WHERE company_id = $1 AND id = $2`, company, id, open, pdc, bounced, amount)
	return err
}

func applyInvoice(ctx context.Context, tx pgx.Tx, company, id uuid.UUID, amount int64, cover bool) error {
	inv, err := lockInvoice(ctx, tx, company, id)
	if err != nil {
		return err
	}
	if amount > inv.open {
		return apierr.New(apierr.ValidationError, "Allocation cannot exceed the invoice balance.")
	}
	pdc := inv.pdc
	if cover {
		pdc += amount
	}
	return setInvoice(ctx, tx, company, id, inv.open-amount, pdc, inv.bounced, inv.amount)
}

func requireKind(ctx context.Context, tx pgx.Tx, company uuid.UUID, id *uuid.UUID, kind string) error {
	if id == nil {
		return apierr.New(apierr.ValidationError, "An account is required.")
	}
	var got string
	err := tx.QueryRow(ctx, `SELECT kind FROM erp.bank_accounts WHERE company_id = $1 AND id = $2`, company, *id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.New(apierr.NotFound, "Account not found.")
	}
	if err != nil {
		return err
	}
	if got != kind {
		return apierr.New(apierr.ValidationError, "The account kind does not match the receipt.")
	}
	return nil
}

func addCredit(ctx context.Context, tx pgx.Tx, company, customer uuid.UUID, amount int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO erp.ar_customer_credits (company_id, customer_id, credit_fils) VALUES ($1, $2, $3)
		ON CONFLICT (company_id, customer_id) DO UPDATE SET credit_fils = erp.ar_customer_credits.credit_fils + EXCLUDED.credit_fils`,
		company, customer, amount)
	return err
}

func spendCredit(ctx context.Context, tx pgx.Tx, company, customer uuid.UUID, amount int64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE erp.ar_customer_credits SET credit_fils = credit_fils - $3
		WHERE company_id = $1 AND customer_id = $2 AND credit_fils >= $3`, company, customer, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.ValidationError, "Customer credit is not enough to allocate.")
	}
	return nil
}

func customerCredit(ctx context.Context, tx pgx.Tx, company, customer uuid.UUID) (int64, error) {
	var fils int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(credit_fils, 0) FROM erp.ar_customer_credits WHERE company_id = $1 AND customer_id = $2`, company, customer).Scan(&fils)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return fils, err
}

func addParty(ctx context.Context, tx pgx.Tx, company, customer uuid.UUID, day time.Time, kind, number string, debit, credit int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO erp.ar_party_lines (company_id, customer_id, occurred_on, kind, doc_number, debit_fils, credit_fils)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, company, customer, day, kind, number, debit, credit)
	return err
}

type pdcRow struct {
	direction  string
	status     string
	receipt    uuid.UUID
	customer   uuid.UUID
	amount     int64
	chequeDate time.Time
	bank       *uuid.UUID
	version    int64
}

func lockPDC(ctx context.Context, tx pgx.Tx, company, id uuid.UUID) (pdcRow, error) {
	var row pdcRow
	var receipt *uuid.UUID
	var customer *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT direction, status, receipt_id, customer_id, amount_fils, cheque_date, bank_account_id, state_version
		FROM erp.ar_pdcs WHERE company_id = $1 AND id = $2 FOR UPDATE`, company, id).Scan(
		&row.direction, &row.status, &receipt, &customer, &row.amount, &row.chequeDate, &row.bank, &row.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return pdcRow{}, apierr.New(apierr.NotFound, "Cheque not found.")
	}
	if receipt != nil {
		row.receipt = *receipt
	}
	if customer != nil {
		row.customer = *customer
	}
	return row, err
}

type allocRow struct {
	invoice uuid.UUID
	amount  int64
}

func loadAllocs(ctx context.Context, tx pgx.Tx, company, receipt uuid.UUID) ([]allocRow, error) {
	q, err := tx.Query(ctx, `SELECT invoice_id, amount_fils FROM erp.ar_allocations WHERE company_id = $1 AND receipt_id = $2`, company, receipt)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []allocRow
	for q.Next() {
		var row allocRow
		if err := q.Scan(&row.invoice, &row.amount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, q.Err()
}

func partyLines(ctx context.Context, tx pgx.Tx, company, customer uuid.UUID, from, to time.Time) (Statement, error) {
	q, err := tx.Query(ctx, `
		SELECT occurred_on, kind, doc_number, debit_fils, credit_fils
		FROM erp.ar_party_lines
		WHERE company_id = $1 AND customer_id = $2 AND occurred_on >= $3 AND occurred_on <= $4
		ORDER BY occurred_on, CASE kind WHEN 'invoice' THEN 1 WHEN 'receipt' THEN 2 WHEN 'credit' THEN 3 ELSE 4 END, doc_number, id`,
		company, customer, from, to)
	if err != nil {
		return Statement{}, err
	}
	defer q.Close()
	var lines []StatementLine
	var running int64
	for q.Next() {
		var day time.Time
		var kind, number string
		var debit, credit int64
		if err := q.Scan(&day, &kind, &number, &debit, &credit); err != nil {
			return Statement{}, err
		}
		running += debit - credit
		lines = append(lines, StatementLine{
			Date: ymd(day), Kind: kind, Number: number,
			Debit: AED(debit), Credit: AED(credit), Running: AED(running),
		})
	}
	if err := q.Err(); err != nil {
		return Statement{}, err
	}
	if lines == nil {
		lines = []StatementLine{}
	}
	return Statement{Lines: lines, RunningBalance: AED(running)}, nil
}

func daysBetween(due, asOf time.Time) int {
	d0 := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
	a0 := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	return int(a0.Sub(d0).Hours() / 24)
}

func bucketOf(due, asOf time.Time) string {
	days := daysBetween(due, asOf)
	switch {
	case days <= 0:
		return "current"
	case days <= 30:
		return "1_30"
	case days <= 60:
		return "31_60"
	case days <= 90:
		return "61_90"
	default:
		return "90_plus"
	}
}

func dueMatches(due, asOf time.Time, filter string) bool {
	switch filter {
	case "":
		return true
	case "today":
		return ymd(due) == ymd(asOf)
	case "week":
		return !due.Before(asOf) && due.Before(asOf.AddDate(0, 0, 7))
	case "month":
		return due.Year() == asOf.Year() && due.Month() == asOf.Month()
	case "overdue":
		return due.Before(asOf)
	default:
		return true
	}
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

func parseDate(raw string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD.")
	}
	return t, nil
}

func statementPDF(customer, balance string) []byte {
	body := "Statement " + customer + " balance " + balance
	return []byte("%PDF-1.1\n1 0 obj<</Length " + strconv.Itoa(len(body)) + ">>stream\n" + body + "\nendstream\nendobj\ntrailer<<>>\n%%EOF\n")
}

func encodePDF(pdf []byte) string { return base64.StdEncoding.EncodeToString(pdf) }

func parseUUID(raw, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apierr.New(apierr.ValidationError, fmt.Sprintf("%s is not an id.", what))
	}
	return id, nil
}
