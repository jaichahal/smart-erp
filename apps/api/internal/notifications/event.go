package notifications

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ErrAmountCurrency is returned when a payload has an amount and no currency (E4).
var ErrAmountCurrency = errors.New("amount without currency")

// ErrNotificationBlock is returned when a push body contains a notification block (E3, ADR-07).
var ErrNotificationBlock = errors.New("notification block is not allowed")

var (
	ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	moneyAmount = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	currencyRE  = regexp.MustCompile(`^[A-Z]{3}$`)
	deepLinkRE  = regexp.MustCompile(`^smarterp://`)
	docTypeRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

var eventTypes = map[string]struct{}{
	"approval.requested": {}, "approval.decided": {}, "approval.delegated": {}, "approval.snoozed": {},
	"document.registered": {}, "delivery.confirmed": {}, "clock.expired": {}, "receipt.posted": {},
	"pdc.bounced": {}, "stock.received": {}, "stock.count.approved": {}, "production.posted": {},
	"correction.posted": {}, "payment.released": {}, "chain.verified": {}, "chain.broken": {},
	"backup.completed": {}, "backup.failed": {}, "bank.feed.completed": {}, "bank.feed.failed": {},
	"forecast.below_floor": {}, "exception.raised": {}, "config.changed": {}, "break_glass.used": {},
}

var severities = map[string]struct{}{"LOW": {}, "MEDIUM": {}, "HIGH": {}, "CRITICAL": {}}

var allowedActions = map[string]struct{}{
	"approve": {}, "reject": {}, "acknowledge": {}, "snooze": {}, "delegate": {}, "open": {},
}

// Actor is the person who caused the event.
type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Subject is the document the event is about.
type Subject struct {
	DocType   string  `json:"doc_type"`
	DocID     string  `json:"doc_id"`
	DocNumber *string `json:"doc_number"`
	Party     *string `json:"party"`
}

// Money is a decimal amount and an ISO 4217 currency. Currency is required (E4).
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Event is the outbox, socket, and push payload from docs/spec/04.
type Event struct {
	EventID        string         `json:"event_id"`
	Type           string         `json:"type"`
	Severity       string         `json:"severity"`
	CompanyID      string         `json:"company_id"`
	OccurredAt     time.Time      `json:"occurred_at"`
	Actor          Actor          `json:"actor"`
	Subject        Subject        `json:"subject"`
	Amount         *Money         `json:"amount"`
	DeepLink       string         `json:"deep_link"`
	AllowedActions []string       `json:"allowed_actions"`
	StateVersion   int64          `json:"state_version"`
	Context        map[string]any `json:"context"`
}

// NewEventID returns a Crockford ULID.
func NewEventID(now time.Time) (string, error) {
	var id [16]byte
	ms := uint64(now.UTC().UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	if _, err := rand.Read(id[6:]); err != nil {
		return "", err
	}
	return encodeULID(id), nil
}

func encodeULID(id [16]byte) string {
	const enc = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var dst [26]byte
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
	return string(dst[:])
}

// ParseEvent decodes an event and refuses an amount that has no currency
// before any other use of the payload (E4).
func ParseEvent(raw []byte) (Event, error) {
	var probe struct {
		Amount json.RawMessage `json:"amount"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Event{}, fmt.Errorf("notifications: event json: %w", err)
	}
	if len(probe.Amount) > 0 && string(probe.Amount) != "null" {
		var money map[string]json.RawMessage
		if err := json.Unmarshal(probe.Amount, &money); err != nil {
			return Event{}, ErrAmountCurrency
		}
		cur, ok := money["currency"]
		if !ok || string(cur) == `""` || string(cur) == "null" {
			return Event{}, ErrAmountCurrency
		}
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return Event{}, fmt.Errorf("notifications: event json: %w", err)
	}
	if ev.Context == nil {
		ev.Context = map[string]any{}
	}
	return ev, nil
}

// Validate checks the event against the rules in contracts/events/event.schema.json.
func (e Event) Validate() error {
	if !ulidPattern.MatchString(e.EventID) {
		return fmt.Errorf("notifications: event_id: %w", errInvalid)
	}
	if _, ok := eventTypes[e.Type]; !ok {
		return fmt.Errorf("notifications: type: %w", errInvalid)
	}
	if _, ok := severities[e.Severity]; !ok {
		return fmt.Errorf("notifications: severity: %w", errInvalid)
	}
	if e.CompanyID == "" || e.Actor.ID == "" || e.Subject.DocID == "" {
		return errInvalid
	}
	if !docTypeRE.MatchString(e.Subject.DocType) {
		return fmt.Errorf("notifications: doc_type: %w", errInvalid)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("notifications: occurred_at: %w", errInvalid)
	}
	if !deepLinkRE.MatchString(e.DeepLink) {
		return fmt.Errorf("notifications: deep_link: %w", errInvalid)
	}
	if e.StateVersion < 0 {
		return fmt.Errorf("notifications: state_version: %w", errInvalid)
	}
	seen := map[string]struct{}{}
	for _, a := range e.AllowedActions {
		if _, ok := allowedActions[a]; !ok {
			return fmt.Errorf("notifications: allowed_action: %w", errInvalid)
		}
		if _, dup := seen[a]; dup {
			return fmt.Errorf("notifications: allowed_action duplicate: %w", errInvalid)
		}
		seen[a] = struct{}{}
	}
	if e.Context == nil {
		return fmt.Errorf("notifications: context: %w", errInvalid)
	}
	if e.Amount != nil {
		if !moneyAmount.MatchString(e.Amount.Amount) || !currencyRE.MatchString(e.Amount.Currency) {
			return ErrAmountCurrency
		}
	}
	return nil
}

var errInvalid = errors.New("invalid event")

// PushData flattens the event to string values with context JSON-encoded (ADR-07).
// The result never contains a notification key.
func (e Event) PushData() (map[string]string, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	ctx, err := json.Marshal(e.Context)
	if err != nil {
		return nil, err
	}
	actions, err := json.Marshal(e.AllowedActions)
	if err != nil {
		return nil, err
	}
	docNumber, party := "", ""
	if e.Subject.DocNumber != nil {
		docNumber = *e.Subject.DocNumber
	}
	if e.Subject.Party != nil {
		party = *e.Subject.Party
	}
	data := map[string]string{
		"event_id":        e.EventID,
		"type":            e.Type,
		"severity":        e.Severity,
		"company_id":      e.CompanyID,
		"occurred_at":     e.OccurredAt.UTC().Format(time.RFC3339),
		"actor_id":        e.Actor.ID,
		"actor_name":      e.Actor.Name,
		"doc_type":        e.Subject.DocType,
		"doc_id":          e.Subject.DocID,
		"doc_number":      docNumber,
		"party":           party,
		"deep_link":       e.DeepLink,
		"allowed_actions": string(actions),
		"state_version":   strconv.FormatInt(e.StateVersion, 10),
		"context":         string(ctx),
	}
	if e.Amount != nil {
		data["amount"] = e.Amount.Amount
		data["currency"] = e.Amount.Currency
	}
	if _, ok := data["notification"]; ok {
		return nil, ErrNotificationBlock
	}
	return data, nil
}

// RejectNotificationBlock fails closed when a push body contains a notification
// block. Schema additionalProperties would reject the same key on an event (E3).
func RejectNotificationBlock(body []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("notifications: push json: %w", err)
	}
	if _, ok := probe["notification"]; ok {
		return ErrNotificationBlock
	}
	if msg, ok := probe["message"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(msg, &inner); err == nil {
			if _, has := inner["notification"]; has {
				return ErrNotificationBlock
			}
		}
	}
	return nil
}

// Group classifies an inbox item. FYI events are what the weekly digest batches (E6).
func Group(actions []string, severity string) string {
	for _, a := range actions {
		if a == "approve" || a == "reject" {
			return "needs_me"
		}
	}
	if severity == "LOW" {
		return "fyi"
	}
	for _, a := range actions {
		if a == "acknowledge" {
			return "fyi"
		}
	}
	return "waiting"
}

func isFYI(actions []string, severity string) bool {
	return Group(actions, severity) == "fyi"
}
