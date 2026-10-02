package periods

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

const (
	allocLock int32 = 7401
	voidLock  int32 = 7402
)

type allocBody struct {
	ID          string    `json:"id"`
	CompanyID   string    `json:"company_id"`
	DocType     string    `json:"doc_type"`
	FiscalYear  int       `json:"fiscal_year"`
	Number      int64     `json:"number"`
	DocID       string    `json:"doc_id"`
	AllocatedAt time.Time `json:"allocated_at"`
	ChainSeq    int64     `json:"chain_seq"`
}

type voidBody struct {
	ID         string    `json:"id"`
	CompanyID  string    `json:"company_id"`
	DocType    string    `json:"doc_type"`
	FiscalYear int       `json:"fiscal_year"`
	Number     int64     `json:"number"`
	DocID      string    `json:"doc_id"`
	Reason     string    `json:"reason"`
	VoidedAt   time.Time `json:"voided_at"`
	ChainSeq   int64     `json:"chain_seq"`
}

// Register allocates the next number inside the same transaction as Apply (R4.5, C1, C2).
// A non-nil error from Apply rolls the business write back, records a void, and commits
// the sequence so the next registration continues.
func (s *Service) Register(ctx context.Context, p rls.Principal, in Registration, lang string) (Allocation, error) {
	if !docTypeRe.MatchString(in.DocType) || in.DocID == "" || in.FiscalYear < 2000 || in.FiscalYear > 2200 || in.Apply == nil {
		return Allocation{}, fail(lang, apierr.ValidationError, "registration_invalid")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var alloc Allocation
	var bizErr error
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		number, err := allocate(ctx, tx, p, in)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT reg_apply`); err != nil {
			return fmt.Errorf("periods: savepoint: %w", err)
		}
		bizErr = in.Apply(ctx, tx, number)
		if bizErr != nil {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT reg_apply`); err != nil {
				return fmt.Errorf("periods: rollback savepoint: %w", err)
			}
			if err := voidNumber(ctx, tx, p, in, number); err != nil {
				return err
			}
			alloc = Allocation{Number: number, DocType: in.DocType, FiscalYear: in.FiscalYear, Voided: true}
			return nil
		}
		if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT reg_apply`); err != nil {
			return fmt.Errorf("periods: release savepoint: %w", err)
		}
		alloc = Allocation{Number: number, DocType: in.DocType, FiscalYear: in.FiscalYear}
		return nil
	})
	if err != nil {
		return Allocation{}, err
	}
	if bizErr != nil {
		return alloc, bizErr
	}
	return alloc, nil
}

func allocate(ctx context.Context, tx pgx.Tx, p rls.Principal, in Registration) (int64, error) {
	if err := lockCompany(ctx, tx, allocLock, p.CompanyID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.number_sequences (company_id, doc_type, fiscal_year, next_number)
		VALUES ($1, $2, $3, 1) ON CONFLICT DO NOTHING`, p.CompanyID, in.DocType, in.FiscalYear); err != nil {
		return 0, fmt.Errorf("periods: sequence insert: %w", err)
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_number FROM erp.number_sequences
		WHERE company_id = $1 AND doc_type = $2 AND fiscal_year = $3 FOR UPDATE`,
		p.CompanyID, in.DocType, in.FiscalYear).Scan(&number); err != nil {
		return 0, fmt.Errorf("periods: sequence lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.number_sequences SET next_number = next_number + 1
		WHERE company_id = $1 AND doc_type = $2 AND fiscal_year = $3`,
		p.CompanyID, in.DocType, in.FiscalYear); err != nil {
		return 0, fmt.Errorf("periods: sequence update: %w", err)
	}
	at, seq, prev, err := nextChain(ctx, tx, `SELECT COALESCE(MAX(chain_seq), 0) FROM erp.number_allocations WHERE company_id = $1`,
		`SELECT hash FROM erp.number_allocations WHERE company_id = $1 AND chain_seq = $2`, p.CompanyID)
	if err != nil {
		return 0, err
	}
	body := allocBody{ID: uuid.NewString(), CompanyID: p.CompanyID.String(), DocType: in.DocType, FiscalYear: in.FiscalYear,
		Number: number, DocID: in.DocID, AllocatedAt: at, ChainSeq: seq}
	canonical, hash, err := canon.MarshalAndHash(body, prev)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.number_allocations
		(id, company_id, doc_type, fiscal_year, number, doc_id, allocated_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		body.ID, p.CompanyID, in.DocType, in.FiscalYear, number, in.DocID, at, string(canonical), seq, prev, hash); err != nil {
		return 0, fmt.Errorf("periods: allocation insert: %w", err)
	}
	return number, nil
}

func voidNumber(ctx context.Context, tx pgx.Tx, p rls.Principal, in Registration, number int64) error {
	if err := lockCompany(ctx, tx, voidLock, p.CompanyID); err != nil {
		return err
	}
	at, seq, prev, err := nextChain(ctx, tx, `SELECT COALESCE(MAX(chain_seq), 0) FROM erp.number_voids WHERE company_id = $1`,
		`SELECT hash FROM erp.number_voids WHERE company_id = $1 AND chain_seq = $2`, p.CompanyID)
	if err != nil {
		return err
	}
	body := voidBody{ID: uuid.NewString(), CompanyID: p.CompanyID.String(), DocType: in.DocType, FiscalYear: in.FiscalYear,
		Number: number, DocID: in.DocID, Reason: "registration_failed", VoidedAt: at, ChainSeq: seq}
	canonical, hash, err := canon.MarshalAndHash(body, prev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.number_voids
		(id, company_id, doc_type, fiscal_year, number, doc_id, reason, voided_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		body.ID, p.CompanyID, in.DocType, in.FiscalYear, number, in.DocID, body.Reason, at, string(canonical), seq, prev, hash)
	if err != nil {
		return fmt.Errorf("periods: void insert: %w", err)
	}
	return nil
}

func lockCompany(ctx context.Context, tx pgx.Tx, key int32, company uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2::text))`, key, company.String())
	if err != nil {
		return fmt.Errorf("periods: lock sequence: %w", err)
	}
	return nil
}

func nextChain(ctx context.Context, tx pgx.Tx, maxSQL, prevSQL string, company uuid.UUID) (time.Time, int64, string, error) {
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return time.Time{}, 0, "", err
	}
	var maxSeq int64
	if err := tx.QueryRow(ctx, maxSQL, company).Scan(&maxSeq); err != nil {
		return time.Time{}, 0, "", err
	}
	prev := ""
	if maxSeq > 0 {
		if err := tx.QueryRow(ctx, prevSQL, company, maxSeq).Scan(&prev); err != nil {
			return time.Time{}, 0, "", err
		}
	}
	return at, maxSeq + 1, prev, nil
}
