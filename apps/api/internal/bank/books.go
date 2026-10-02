package bank

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type bookLine struct {
	role     string
	account  *uuid.UUID
	debit    int64
	credit   int64
	docType  string
	docID    string
	postedOn time.Time
}

func insertBooks(ctx context.Context, tx pgx.Tx, company uuid.UUID, lines []bookLine) ([]uuid.UUID, error) {
	var debit, credit int64
	for _, ln := range lines {
		debit += ln.debit
		credit += ln.credit
	}
	if debit != credit {
		return nil, fmt.Errorf("bank: posting does not balance")
	}
	ids := make([]uuid.UUID, 0, len(lines))
	for _, ln := range lines {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO erp.book_lines (company_id, role, account_id, debit_fils, credit_fils, doc_type, doc_id, posted_on)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			company, ln.role, ln.account, ln.debit, ln.credit, ln.docType, ln.docID, ln.postedOn).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func toPostings(lines []bookLine) []Posting {
	out := make([]Posting, 0, len(lines))
	for _, ln := range lines {
		out = append(out, side(ln.role, ln.debit, ln.credit))
	}
	return out
}

func accountBalance(ctx context.Context, tx pgx.Tx, company, account uuid.UUID) (int64, int, error) {
	var balance int64
	var count int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit_fils - credit_fils), 0), COUNT(*)
		FROM erp.book_lines WHERE company_id = $1 AND account_id = $2`, company, account).Scan(&balance, &count)
	return balance, count, err
}
