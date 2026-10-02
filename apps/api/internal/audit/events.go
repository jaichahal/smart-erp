package audit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// ulidPattern is the contract's event_id pattern.
var ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// docTypePattern matches contracts/events subject.doc_type.
var docTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// eventTypes is the closed set the schema allows. A test reads the schema file
// and fails if this list drifts.
var eventTypes = map[string]bool{
	"chain.verified": true, "chain.broken": true,
	"backup.completed": true, "backup.failed": true,
}

// DomainEvent is the outbox payload. It must validate against event.schema.json.
type DomainEvent struct {
	EventID        string          `json:"event_id"`
	Type           string          `json:"type"`
	Severity       string          `json:"severity"`
	CompanyID      string          `json:"company_id"`
	OccurredAt     string          `json:"occurred_at"`
	Actor          eventActor      `json:"actor"`
	Subject        eventSubject    `json:"subject"`
	Amount         json.RawMessage `json:"amount"`
	DeepLink       string          `json:"deep_link"`
	AllowedActions []string        `json:"allowed_actions"`
	StateVersion   int             `json:"state_version"`
	Context        map[string]any  `json:"context"`
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

// DomainEventArgs is the River job. QuietHoursExempt is true for a Critical
// that preferences must not suppress (B11, I15).
type DomainEventArgs struct {
	Payload          json.RawMessage `json:"payload"`
	QuietHoursExempt bool            `json:"quiet_hours_exempt"`
}

// Kind is the River job name.
func (DomainEventArgs) Kind() string { return "audit.domain_event" }

// NewULID returns a Crockford base32 ULID for t.
func NewULID(t time.Time) (string, error) {
	var id [16]byte
	ms := uint64(t.UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	if _, err := rand.Read(id[6:]); err != nil {
		return "", err
	}
	s := encodeULID(id)
	if !ulidPattern.MatchString(s) {
		return "", fmt.Errorf("generated ulid %q does not match the contract", s)
	}
	return s, nil
}

func encodeULID(id [16]byte) string {
	const enc = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	dst := make([]byte, 26)
	dst[0] = enc[(id[0]&224)>>5]
	dst[1] = enc[id[0]&31]
	dst[2] = enc[(id[1]&248)>>3]
	dst[3] = enc[((id[1]&7)<<2)|((id[2]&192)>>6)]
	dst[4] = enc[(id[2]&62)>>1]
	dst[5] = enc[((id[2]&1)<<4)|((id[3]&240)>>4)]
	dst[6] = enc[((id[3]&15)<<1)|((id[4]&128)>>7)]
	dst[7] = enc[(id[4]&124)>>2]
	dst[8] = enc[((id[4]&3)<<3)|((id[5]&224)>>5)]
	dst[9] = enc[id[5]&31]
	dst[10] = enc[(id[6]&248)>>3]
	dst[11] = enc[((id[6]&7)<<2)|((id[7]&192)>>6)]
	dst[12] = enc[(id[7]&62)>>1]
	dst[13] = enc[((id[7]&1)<<4)|((id[8]&240)>>4)]
	dst[14] = enc[((id[8]&15)<<1)|((id[9]&128)>>7)]
	dst[15] = enc[(id[9]&124)>>2]
	dst[16] = enc[((id[9]&3)<<3)|((id[10]&224)>>5)]
	dst[17] = enc[id[10]&31]
	dst[18] = enc[(id[11]&248)>>3]
	dst[19] = enc[((id[11]&7)<<2)|((id[12]&192)>>6)]
	dst[20] = enc[(id[12]&62)>>1]
	dst[21] = enc[((id[12]&1)<<4)|((id[13]&240)>>4)]
	dst[22] = enc[((id[13]&15)<<1)|((id[14]&128)>>7)]
	dst[23] = enc[(id[14]&124)>>2]
	dst[24] = enc[((id[14]&3)<<3)|((id[15]&224)>>5)]
	dst[25] = enc[id[15]&31]
	return string(dst)
}

func (s *Service) publish(ctx context.Context, ev DomainEvent, quietExempt bool) error {
	if ev.Amount == nil {
		ev.Amount = json.RawMessage("null")
	}
	if ev.Context == nil {
		ev.Context = map[string]any{}
	}
	if ev.AllowedActions == nil {
		ev.AllowedActions = []string{}
	}
	if err := validateEvent(ev); err != nil {
		return err
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if s.River == nil {
		return fmt.Errorf("outbox client is required")
	}
	p := rls.Principal{UserID: "system", Roles: []string{"system"}}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := outbox.InsertTx(ctx, s.River, tx, DomainEventArgs{Payload: raw, QuietHoursExempt: quietExempt}, &river.InsertOpts{Queue: outbox.QueueAnchors})
		return err
	})
}

func validateEvent(ev DomainEvent) error {
	if !ulidPattern.MatchString(ev.EventID) {
		return fmt.Errorf("event_id is not a ulid")
	}
	if !eventTypes[ev.Type] {
		return fmt.Errorf("event type %q is not in the contract", ev.Type)
	}
	switch ev.Severity {
	case "LOW", "MEDIUM", "HIGH", "CRITICAL":
	default:
		return fmt.Errorf("severity %q is not in the contract", ev.Severity)
	}
	if ev.CompanyID == "" || ev.OccurredAt == "" || ev.Actor.ID == "" {
		return fmt.Errorf("event is missing company, time, or actor")
	}
	if _, err := time.Parse(time.RFC3339, ev.OccurredAt); err != nil {
		return fmt.Errorf("occurred_at: %w", err)
	}
	if !docTypePattern.MatchString(ev.Subject.DocType) || ev.Subject.DocID == "" {
		return fmt.Errorf("subject is not valid")
	}
	if len(ev.DeepLink) < len("smarterp://") || ev.DeepLink[:len("smarterp://")] != "smarterp://" {
		return fmt.Errorf("deep_link must start with smarterp://")
	}
	if ev.StateVersion < 0 {
		return fmt.Errorf("state_version is negative")
	}
	if string(ev.Amount) != "null" {
		var money struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		}
		if err := json.Unmarshal(ev.Amount, &money); err != nil || money.Currency == "" || money.Amount == "" {
			return fmt.Errorf("amount must be money or null")
		}
	}
	return nil
}

func newEvent(now time.Time, typ, severity, company, docType, docID, deepLink string, actions []string, ctx map[string]any) (DomainEvent, error) {
	id, err := NewULID(now)
	if err != nil {
		return DomainEvent{}, err
	}
	if actions == nil {
		actions = []string{"open"}
	}
	return DomainEvent{
		EventID:        id,
		Type:           typ,
		Severity:       severity,
		CompanyID:      company,
		OccurredAt:     now.UTC().Format(time.RFC3339),
		Actor:          eventActor{ID: IdentityAnchorWorker, Name: IdentityAnchorWorker},
		Subject:        eventSubject{DocType: docType, DocID: docID},
		Amount:         json.RawMessage("null"),
		DeepLink:       deepLink,
		AllowedActions: actions,
		Context:        ctx,
	}, nil
}
