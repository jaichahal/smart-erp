package periods

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// event is an outbox payload. Its JSON object is the event schema, so the struct
// has no extra fields. period.closed is added by the contracts PR; exception.raised
// is already in the schema.
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

// InsertOpts is unused; Kind implements river.JobArgs.
var _ river.JobArgs = event{}

func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, p rls.Principal, eventType, docType, docID string, version int64, severity string, context map[string]any, lang string) error {
	if s.river == nil {
		return fail(lang, apierr.MissingConfig, "outbox_unconfigured")
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return err
	}
	id, err := newULID(at)
	if err != nil {
		return err
	}
	name := p.UserID
	if p.UserID == "" || p.UserID == "system" {
		name = tr("en", "actor_system")
	}
	ev := event{
		EventID: id, Type: eventType, Severity: oapi.Severity(severity), CompanyID: p.CompanyID.String(),
		OccurredAt: at.UTC(), Actor: oapi.Actor{Id: p.UserID, Name: name},
		Subject:        eventSubject{DocType: docType, DocID: docID},
		DeepLink:       "smarterp://document/" + docID,
		AllowedActions: []oapi.AllowedAction{oapi.Open},
		StateVersion:   version, Context: context,
	}
	if ev.Actor.Id == "" {
		ev.Actor.Id = "system"
	}
	if err := ev.validate(); err != nil {
		return err
	}
	if _, err := outbox.InsertTx(ctx, s.river, tx, ev, nil); err != nil {
		return fmt.Errorf("periods: outbox: %w", err)
	}
	return nil
}

func (e event) validate() error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var probe struct {
		EventID        string         `json:"event_id"`
		Type           string         `json:"type"`
		Severity       string         `json:"severity"`
		CompanyID      string         `json:"company_id"`
		DeepLink       string         `json:"deep_link"`
		AllowedActions []string       `json:"allowed_actions"`
		StateVersion   int64          `json:"state_version"`
		Subject        map[string]any `json:"subject"`
		Context        map[string]any `json:"context"`
		Amount         any            `json:"amount"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	if probe.EventID == "" || probe.CompanyID == "" || probe.Context == nil || probe.Subject == nil {
		return fmt.Errorf("periods: event is incomplete")
	}
	if probe.Type != "period.closed" && probe.Type != "exception.raised" {
		return fmt.Errorf("periods: unexpected event type %s", probe.Type)
	}
	if !oapi.Severity(probe.Severity).Valid() {
		return fmt.Errorf("periods: unexpected severity")
	}
	if len(probe.DeepLink) < len("smarterp://") || probe.DeepLink[:len("smarterp://")] != "smarterp://" {
		return fmt.Errorf("periods: deep link")
	}
	if probe.Amount != nil {
		return fmt.Errorf("periods: period events have no amount")
	}
	if probe.StateVersion < 0 {
		return fmt.Errorf("periods: state version")
	}
	return nil
}
