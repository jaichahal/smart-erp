package periods

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// C4: posting into a prior open period without the back-dating permission is refused.
// With the permission, the posting appears on the exceptions report.
func TestC4(t *testing.T) {
	db, svc, accountant := newService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	if err := svc.OpenFiscalYear(ctx, accountant, now.Year(), start, "en"); err != nil {
		t.Fatal(err)
	}
	prior := start
	docID := "inv-backdated"
	err := svc.CheckPosting(ctx, accountant, Posting{PostingDate: prior, DocType: "sales_invoice", DocID: docID}, "en")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.PermissionDenied {
		t.Fatalf("without permission = %v", err)
	}
	rows, err := svc.ListExceptions(ctx, accountant, now, "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("refused posting appeared on the report: %+v", rows)
	}

	backdater := accountant
	backdater.Roles = []string{RoleBackdate}
	if err := svc.CheckPosting(ctx, backdater, Posting{PostingDate: prior, DocType: "sales_invoice", DocID: docID}, "en"); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.ListExceptions(ctx, backdater, now, "en")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.DocID == docID && row.Kind == reasonBackdated {
			found = true
		}
	}
	if !found {
		t.Fatalf("exceptions = %+v", rows)
	}
	raw := jobArgs(t, db, "exception.raised", docID)
	if err := matchEventSchema(schemaDoc(t), raw); err != nil {
		t.Fatal(err)
	}
}
