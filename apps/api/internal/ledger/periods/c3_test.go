package periods

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// C3: a posting into a hard-closed period is refused with PERIOD_CLOSED.
func TestC3(t *testing.T) {
	db, svc, accountant := newService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if err := svc.OpenFiscalYear(ctx, accountant, now.Year(), start, "en"); err != nil {
		t.Fatal(err)
	}
	today := now.Format("2006-01-02")
	period := periodCovering(t, db, accountant, today)
	stakeholder := accountant
	stakeholder.Roles = []string{RoleStakeholder}
	closed, err := svc.HardClose(ctx, stakeholder, mustID(t, period.ID), period.StateVersion, "approval-1", "en")
	if err != nil || closed.Status != statusHard {
		t.Fatalf("hard close = %+v, %v", closed, err)
	}
	err = svc.CheckPosting(ctx, accountant, Posting{PostingDate: now, DocType: "sales_invoice", DocID: "inv-closed"}, "en")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.PeriodClosed {
		t.Fatalf("posting = %v, want PERIOD_CLOSED", err)
	}
	raw := jobArgs(t, db, "period.closed", period.ID)
	if schemaHas(t, "period.closed") {
		if err := matchEventSchema(schemaDoc(t), raw); err != nil {
			t.Fatal(err)
		}
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe["type"] != "period.closed" || probe["notification"] != nil {
		t.Fatalf("event = %s", raw)
	}
}

func mustID(t *testing.T, id string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func schemaHas(t *testing.T, eventType string) bool {
	t.Helper()
	var sch map[string]any
	if err := json.Unmarshal(schemaDoc(t), &sch); err != nil {
		t.Fatal(err)
	}
	defs, _ := sch["$defs"].(map[string]any)
	typ, _ := defs["EventType"].(map[string]any)
	enum, _ := typ["enum"].([]any)
	for _, item := range enum {
		if item == eventType {
			return true
		}
	}
	return false
}
