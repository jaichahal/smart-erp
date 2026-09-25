package ledger

import (
	"time"

	"github.com/google/uuid"
)

// RuleMatch is the lookup key for a posting rule.
type RuleMatch struct {
	DocType          string
	TaxCode          string
	ItemClass        string
	DimensionValueID *uuid.UUID
	PartyGroup       string
}

// RuleProposal is a pending change. Account ids are chosen from the chart;
// this package does not embed account codes.
type RuleProposal struct {
	Match             RuleMatch
	DebitAccountID    uuid.UUID
	CreditAccountID   uuid.UUID
	TaxAccountID      *uuid.UUID
	DiscountAccountID *uuid.UUID
	RoundingAccountID *uuid.UUID
	Priority          int
	Reason            string
}

// RuleVersion is one approved or pending snapshot of a rule.
type RuleVersion struct {
	ID                uuid.UUID
	DebitAccountID    uuid.UUID
	CreditAccountID   uuid.UUID
	TaxAccountID      *uuid.UUID
	DiscountAccountID *uuid.UUID
	RoundingAccountID *uuid.UUID
	Status            string
}

// RulePosting is a document posted through the current approved rule.
type RulePosting struct {
	Match       RuleMatch
	PostingDate time.Time
	DocID       string
	Net         string
	Tax         string
}

// SchedulePosting is an accrual or prepayment that reverses next period.
type SchedulePosting struct {
	PostingDate time.Time
	DocID       string
	Amount      string
}

// RecurringProposal is one setup. It posts only after a different user approves it.
type RecurringProposal struct {
	Description    string
	Start          time.Time
	End            time.Time
	IntervalMonths int
	Lines          []ManualLine
}

// RecurringSetup is the stored setup.
type RecurringSetup struct {
	ID     uuid.UUID
	Status string
}
