package clocks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

const (
	// KindDeliveryNote is the clock on a delivery note (default 24 business hours).
	KindDeliveryNote = "delivery_note"
	// KindSupplierInvoice is the clock on a missing supplier invoice.
	KindSupplierInvoice = "supplier_invoice"
	// KindSupplierDeliveryNote is the clock on a supplier delivery note.
	KindSupplierDeliveryNote = "supplier_delivery_note"
	// KindProductionDraft is the clock on an aged production draft.
	KindProductionDraft = "production_draft"
	// KindApprovalAge is the clock on an approval waiting past its window.
	KindApprovalAge = "approval_age"
)

// Clock is a business-hours deadline attached to a document.
type Clock struct {
	ID        uuid.UUID `json:"id"`
	DocID     string    `json:"doc_id"`
	DocType   string    `json:"doc_type"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	DueAt     time.Time `json:"due_at"`
	Window    time.Duration
}

// StartInput opens a clock at StartedAt for Window of business time.
type StartInput struct {
	DocID     string
	DocType   string
	Kind      string
	StartedAt time.Time
	Window    time.Duration
}

// Start stores a clock whose due instant is Calendar.Due(start, window).
func (s *Service) Start(ctx context.Context, p rls.Principal, in StartInput, lang string) (Clock, error) {
	if in.DocID == "" || in.DocType == "" || !knownKind(in.Kind) || in.Window < 0 {
		return Clock{}, fail(lang, apierr.ValidationError, "clock_invalid")
	}
	ctx = rls.WithPrincipal(ctx, p)
	var out Clock
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		cal, err := loadCalendar(ctx, tx, p.CompanyID, lang)
		if err != nil {
			return err
		}
		due, err := cal.Due(in.StartedAt, in.Window)
		if err != nil {
			return fail(lang, apierr.ValidationError, "clock_no_business_day")
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO erp.clocks
			(company_id, doc_id, doc_type, kind, started_at, due_at, window_seconds)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			p.CompanyID, in.DocID, in.DocType, in.Kind, in.StartedAt, due, int64(in.Window/time.Second)).Scan(&id)
		if err != nil {
			return fmt.Errorf("clocks: insert: %w", err)
		}
		out = Clock{ID: id, DocID: in.DocID, DocType: in.DocType, Kind: in.Kind, StartedAt: in.StartedAt, DueAt: due, Window: in.Window}
		return nil
	})
	return out, err
}

func knownKind(kind string) bool {
	switch kind {
	case KindDeliveryNote, KindSupplierInvoice, KindSupplierDeliveryNote, KindProductionDraft, KindApprovalAge:
		return true
	default:
		return false
	}
}

type event struct {
	EventID        string               `json:"event_id"`
	Type           string               `json:"type"`
	Severity       oapi.Severity        `json:"severity"`
	CompanyID      string               `json:"company_id"`
	OccurredAt     time.Time            `json:"occurred_at"`
	Actor          oapi.Actor           `json:"actor"`
	Subject        eventSubject         `json:"subject"`
	Amount         *oapi.Money          `json:"amount"`
	DeepLink       string               `json:"deep_link"`
	AllowedActions []oapi.AllowedAction `json:"allowed_actions"`
	StateVersion   int64                `json:"state_version"`
	Context        map[string]any       `json:"context"`
}

type eventSubject struct {
	DocType   string  `json:"doc_type"`
	DocID     string  `json:"doc_id"`
	DocNumber *string `json:"doc_number"`
	Party     *string `json:"party"`
}

// Kind is the River job name. It matches the event type.
func (e event) Kind() string { return e.Type }

var _ river.JobArgs = event{}

func (s *Service) enqueueExpired(ctx context.Context, tx pgx.Tx, row dueClock) error {
	if s.river == nil {
		return fail("en", apierr.MissingConfig, "outbox_unconfigured")
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return err
	}
	id, err := newULID(at)
	if err != nil {
		return err
	}
	ev := event{
		EventID: id, Type: "clock.expired", Severity: oapi.CRITICAL, CompanyID: row.CompanyID.String(),
		OccurredAt: at.UTC(), Actor: oapi.Actor{Id: "system", Name: tr("en", "actor_system")},
		Subject:        eventSubject{DocType: row.DocType, DocID: row.DocID},
		DeepLink:       "smarterp://document/" + row.DocID,
		AllowedActions: []oapi.AllowedAction{oapi.Acknowledge, oapi.Open},
		StateVersion:   row.StateVersion,
		Context: map[string]any{
			"clock_id": row.ID.String(), "kind": row.Kind,
			"started_at": row.StartedAt.UTC().Format(time.RFC3339), "due_at": row.DueAt.UTC().Format(time.RFC3339),
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	if probe["type"] != "clock.expired" || probe["notification"] != nil {
		return fmt.Errorf("clocks: refused event payload")
	}
	if _, err := outbox.InsertTx(ctx, s.river, tx, ev, nil); err != nil {
		return fmt.Errorf("clocks: outbox: %w", err)
	}
	return nil
}

type dueClock struct {
	ID           uuid.UUID
	CompanyID    uuid.UUID
	DocID        string
	DocType      string
	Kind         string
	StartedAt    time.Time
	DueAt        time.Time
	StateVersion int64
}

// ExpireDue is the scheduler hook. It emits clock.expired for every open clock
// whose due instant is at or before now, and records the escalation on the row.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	ctx = rls.WithPrincipal(ctx, rls.System)
	var n int
	err := rls.Tx(ctx, s.pool, rls.System, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, company_id, doc_id, doc_type, kind, started_at, due_at, state_version
			FROM erp.clocks
			WHERE due_at <= $1 AND closed_at IS NULL AND escalated_at IS NULL
			ORDER BY due_at
			FOR UPDATE`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		var due []dueClock
		for rows.Next() {
			var row dueClock
			if err := rows.Scan(&row.ID, &row.CompanyID, &row.DocID, &row.DocType, &row.Kind, &row.StartedAt, &row.DueAt, &row.StateVersion); err != nil {
				return err
			}
			due = append(due, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, row := range due {
			if err := s.enqueueExpired(ctx, tx, row); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE erp.clocks SET escalated_at = clock_timestamp(), state_version = state_version + 1
				WHERE id = $1 AND escalated_at IS NULL`, row.ID)
			if err != nil {
				return fmt.Errorf("clocks: escalate: %w", err)
			}
			if tag.RowsAffected() == 1 {
				n++
			}
		}
		return nil
	})
	return n, err
}

// PeriodicJob is what cmd/scheduler registers. The worker calls ExpireDue.
func PeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) {
			return sweepArgs{}, nil
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

type sweepArgs struct{}

// Kind is the River job name for the scheduler sweep.
func (sweepArgs) Kind() string { return "clocks.expire" }

// ExpireWorker runs the sweep. cmd/worker adds it; this package does not import cmd.
type ExpireWorker struct {
	river.WorkerDefaults[sweepArgs]
	Svc *Service
}

// Work expires due clocks.
func (w *ExpireWorker) Work(ctx context.Context, _ *river.Job[sweepArgs]) error {
	if w.Svc == nil {
		return fmt.Errorf("clocks: worker has no service")
	}
	_, err := w.Svc.ExpireDue(ctx, time.Now().UTC())
	return err
}
