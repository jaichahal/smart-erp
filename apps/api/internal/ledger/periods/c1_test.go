package periods

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// C1: a number is issued only for a successful registration. A failed registration
// records a void and the next successful registration continues.
func TestC1(t *testing.T) {
	db, svc, p := newService(t)
	ctx := context.Background()
	ok, err := svc.Register(ctx, p, Registration{
		DocType: "sales_invoice", DocID: "doc-ok-1", FiscalYear: 2026,
		Apply: func(ctx context.Context, tx pgx.Tx, number int64) error {
			_, err := tx.Exec(ctx, `UPDATE erp.companies SET legal_name = $2 WHERE id = $1`, p.CompanyID, "issued")
			return err
		},
	}, "en")
	if err != nil || ok.Voided || ok.Number != 1 {
		t.Fatalf("first registration = %+v, %v", ok, err)
	}
	if companyName(t, db, p) != "issued" {
		t.Fatal("successful registration did not keep its write")
	}

	failed, err := svc.Register(ctx, p, Registration{
		DocType: "sales_invoice", DocID: "doc-fail", FiscalYear: 2026,
		Apply: func(ctx context.Context, tx pgx.Tx, _ int64) error {
			if _, execErr := tx.Exec(ctx, `UPDATE erp.companies SET legal_name = 'rolled-back' WHERE id = $1`, p.CompanyID); execErr != nil {
				return execErr
			}
			return errors.New("registration failed")
		},
	}, "en")
	if err == nil || !failed.Voided || failed.Number != 2 {
		t.Fatalf("failed registration = %+v, %v", failed, err)
	}
	if companyName(t, db, p) != "issued" {
		t.Fatalf("failed registration kept a write: %s", companyName(t, db, p))
	}

	next, err := svc.Register(ctx, p, Registration{
		DocType: "sales_invoice", DocID: "doc-ok-2", FiscalYear: 2026,
		Apply: func(context.Context, pgx.Tx, int64) error { return nil },
	}, "en")
	if err != nil || next.Voided || next.Number != 3 {
		t.Fatalf("continued registration = %+v, %v", next, err)
	}
	if got, want := listNumbers(t, db, p, true), []int64{2}; !sameInts(got, want) {
		t.Fatalf("voids = %v, want %v", got, want)
	}
	if got, want := listNumbers(t, db, p, false), []int64{1, 2, 3}; !sameInts(got, want) {
		t.Fatalf("allocations = %v, want %v", got, want)
	}
}

func sameInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestC1DoesNotIssueOnValidation(t *testing.T) {
	_, svc, p := newService(t)
	_, err := svc.Register(context.Background(), p, Registration{DocType: "BAD", DocID: "x", FiscalYear: 2026, Apply: func(context.Context, pgx.Tx, int64) error { return nil }}, "en")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.ValidationError {
		t.Fatalf("err = %v", err)
	}
}
