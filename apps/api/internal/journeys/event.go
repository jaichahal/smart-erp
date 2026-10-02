package journeys

import "time"

// StepCompletedArgs is the journey.step.completed outbox payload.
// River stores Kind separately; the JSON of this struct is the event in
// contracts/events/event.schema.json.
type StepCompletedArgs struct {
	EventID        string         `json:"event_id"`
	Type           string         `json:"type"`
	Severity       string         `json:"severity"`
	CompanyID      string         `json:"company_id"`
	OccurredAt     time.Time      `json:"occurred_at"`
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
	DocNumber string  `json:"doc_number"`
	Party     *string `json:"party"`
}

type eventMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Kind is the River job name and the event type.
func (StepCompletedArgs) Kind() string { return "journey.step.completed" }
