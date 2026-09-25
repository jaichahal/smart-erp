package stock

import "github.com/google/uuid"

// Movement types. Receipt types recompute the moving average. Issue types do not.
const (
	MoveReceipt    = "receipt"
	MoveDelivery   = "delivery"
	MoveConsume    = "production_consume"
	MoveProduce    = "production_produce"
	MoveAdjustment = "adjustment"
	MoveWriteOff   = "write_off"
	MoveJobOut     = "job_out"
	MoveJobIn      = "job_in"
	MoveOpening    = "opening"
)

// Reservation statuses. Pending does not reduce available. Active does.
const (
	StatusPending  = "pending"
	StatusActive   = "active"
	StatusReleased = "released"
	StatusConsumed = "consumed"
)

// Move is one ledger posting. QtyDelta is signed. UnitCost is required on
// receipt-type movements and ignored on issues, which take the moving average.
type Move struct {
	SKUID        uuid.UUID
	WarehouseID  uuid.UUID
	QtyDelta     string
	UnitCost     string
	MovementType string
	SourceDocID  string
}

// Line is an inserted stock ledger line.
type Line struct {
	ID           uuid.UUID
	SKUID        uuid.UUID
	WarehouseID  uuid.UUID
	QtyDelta     string
	UnitCost     string
	ValueDelta   string
	MovementType string
	SourceDocID  string
}

// ReserveRequest holds stock for one order line, or marks it pending when the
// order was taken offline and must not claim stock yet.
type ReserveRequest struct {
	OrderLineID string
	SKUID       uuid.UUID
	WarehouseID uuid.UUID
	Qty         string
	Pending     bool
}

// Reservation is one order-line hold.
type Reservation struct {
	ID             uuid.UUID
	OrderLineID    string
	SKUID          uuid.UUID
	WarehouseID    uuid.UUID
	Qty            string
	Status         string
	DeliveryNoteID string
}

// Query filters availability. Zero IDs are ignored.
type Query struct {
	SKUID       uuid.UUID
	WarehouseID uuid.UUID
	Class       string
	Text        string
}

// Position is on-hand, active reservations, and available for one SKU in one warehouse.
type Position struct {
	SKU         string
	SKUID       uuid.UUID
	WarehouseID uuid.UUID
	ItemClass   string
	UOM         string
	OnHand      string
	Reserved    string
	Available   string
	UnitCost    string
	Value       string
}
