package sales

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StockLine is a quantity the order wants held or the delivery wants released.
type StockLine struct {
	OrderLineID string
	SKU         string
	WarehouseID string
	Qty         string
}

// Consumed is the moving-average cost of a delivered quantity.
type Consumed struct {
	SKU      string
	Qty      string
	UnitCost string
	Value    string
}

// RestoreLine puts stock back at the cost it left at, not the current average.
type RestoreLine struct {
	SKU         string
	WarehouseID string
	Qty         string
	UnitCost    string
}

// Stock reserves, releases, and consumes inside the caller's transaction.
// The sales package does not import a stock module; this port is the boundary.
type Stock interface {
	Reserve(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string, lines []StockLine) error
	Release(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string) error
	Consume(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string, lines []StockLine) ([]Consumed, error)
	Restore(ctx context.Context, tx pgx.Tx, company uuid.UUID, lines []RestoreLine) error
}

// JournalLine is one side of a posting expressed as an account role.
type JournalLine struct {
	Role   string
	Debit  string
	Credit string
}

// Journal is a balanced posting. Orders never produce one.
type Journal struct {
	DocType   string
	DocID     string
	DocNumber string
	Lines     []JournalLine
}

// Ledger accepts postings from invoice registration, delivery, credit notes, and receipts.
type Ledger interface {
	Post(ctx context.Context, tx pgx.Tx, company uuid.UUID, journal Journal) error
}

// CreditDecision is the result of comparing the order plus open receivables with the limit.
type CreditDecision struct {
	OverLimit bool
	Open      string
	Limit     string
}

// CreditChecker runs inside the order transaction. Skipping it is a failed test.
type CreditChecker interface {
	Check(ctx context.Context, tx pgx.Tx, company uuid.UUID, customerID, orderTotal string) (CreditDecision, error)
}
