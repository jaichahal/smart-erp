package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
)

// eventPayload is the outbox body. It matches contracts/events/event.schema.json.
// River kind is approval.event; the wire type lives in Type so one worker can
// dispatch approval.requested, approval.decided and approval.delegated.
type eventPayload struct {
	EventID        string         `json:"event_id"`
	Type           string         `json:"type"`
	Severity       string         `json:"severity"`
	CompanyID      string         `json:"company_id"`
	OccurredAt     string         `json:"occurred_at"`
	Actor          eventActor     `json:"actor"`
	Subject        eventSubject   `json:"subject"`
	Amount         *eventMoney    `json:"amount"`
	DeepLink       string         `json:"deep_link"`
	AllowedActions []string       `json:"allowed_actions"`
	StateVersion   int            `json:"state_version"`
	Context        map[string]any `json:"context"`
}

type eventActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type eventSubject struct {
	DocType   string  `json:"doc_type"`
	DocID     string  `json:"doc_id"`
	DocNumber *string `json:"doc_number"`
	Party     *string `json:"party"`
}

type eventMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Kind is the River job name for every approval event.
func (eventPayload) Kind() string { return "approval.event" }

var eventTypes = map[string]bool{
	"approval.requested": true,
	"approval.decided":   true,
	"approval.delegated": true,
	"approval.snoozed":   true,
}

var eventActions = map[string]bool{
	"approve": true, "reject": true, "acknowledge": true, "snooze": true, "delegate": true, "open": true,
}

var severities = map[string]bool{"LOW": true, "MEDIUM": true, "HIGH": true, "CRITICAL": true}

// ValidateEvent checks a payload against the approval-relevant rules of
// contracts/events/event.schema.json.
func ValidateEvent(raw []byte) error {
	var ev eventPayload
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("event: json: %w", err)
	}
	if !ulidRe.MatchString(ev.EventID) {
		return fmt.Errorf("event: event_id is not a ulid")
	}
	if !eventTypes[ev.Type] {
		return fmt.Errorf("event: unknown type %q", ev.Type)
	}
	if !severities[ev.Severity] {
		return fmt.Errorf("event: unknown severity %q", ev.Severity)
	}
	if ev.CompanyID == "" || ev.Actor.ID == "" {
		return fmt.Errorf("event: company_id and actor.id are required")
	}
	if _, err := time.Parse(time.RFC3339, ev.OccurredAt); err != nil {
		if _, err2 := time.Parse(time.RFC3339Nano, ev.OccurredAt); err2 != nil {
			return fmt.Errorf("event: occurred_at: %w", err)
		}
	}
	if !docTypeRe.MatchString(ev.Subject.DocType) || ev.Subject.DocID == "" {
		return fmt.Errorf("event: subject is invalid")
	}
	if ev.Amount == nil || !amountRe.MatchString(ev.Amount.Amount) || !currencyRe.MatchString(ev.Amount.Currency) {
		return fmt.Errorf("event: amount is invalid")
	}
	if len(ev.DeepLink) < len("smarterp://") || ev.DeepLink[:len("smarterp://")] != "smarterp://" {
		return fmt.Errorf("event: deep_link must start with smarterp://")
	}
	seen := map[string]bool{}
	if ev.AllowedActions == nil {
		return fmt.Errorf("event: allowed_actions is required")
	}
	for _, a := range ev.AllowedActions {
		if !eventActions[a] || seen[a] {
			return fmt.Errorf("event: allowed action %q", a)
		}
		seen[a] = true
	}
	if ev.StateVersion < 0 {
		return fmt.Errorf("event: state_version is negative")
	}
	if ev.Context == nil {
		return fmt.Errorf("event: context is required")
	}
	return nil
}

func raise(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, ev eventPayload) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := ValidateEvent(raw); err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("event: outbox client is nil")
	}
	_, err = outbox.InsertTx(ctx, client, tx, ev, nil)
	return err
}
