package sales

import "time"

// Status values, hold names, and the roles allowed to clear a hold.
const (
	StatusReserved           = "reserved"
	StatusHeld               = "held"
	StatusPendingReservation = "pending_reservation"
	StatusCancelled          = "cancelled"
	StatusDraft              = "draft"
	StatusSubmitted          = "submitted"
	StatusApproved           = "approved"
	StatusRegistered         = "registered"

	HoldCredit     = "credit"
	HoldPriceFloor = "price_floor"

	RoleCreditController = "credit_controller"
	RolePriceApprover    = "price_approver"

	NoticePendingReservation = "pending reservation"
)

// OrderInput is a new sales order. An offline order is marked pending reservation.
type OrderInput struct {
	CustomerID     string
	WarehouseID    string
	Currency       string
	Offline        bool
	IdempotencyKey string
	AsOf           time.Time
	Lines          []LineInput
}

// LineInput is one SKU quantity and the price the caller offered.
type LineInput struct {
	SKU       string `json:"sku"`
	Qty       string `json:"qty"`
	UnitPrice string `json:"unit_price"`
}

// Order is the stored sales order. Holds are named breaches. Nothing here is a ledger posting.
type Order struct {
	ID           string      `json:"id"`
	CustomerID   string      `json:"customer_id"`
	Status       string      `json:"status"`
	Holds        []string    `json:"holds"`
	AgentNotice  string      `json:"agent_notice"`
	Total        string      `json:"total"`
	Currency     string      `json:"currency"`
	StateVersion int64       `json:"state_version"`
	Lines        []OrderLine `json:"lines"`
}

// OrderLine is one priced row.
type OrderLine struct {
	ID          string `json:"id"`
	SKU         string `json:"sku"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unit_price"`
	TaxCode     string `json:"tax_code"`
	AgreementID string `json:"agreement_id"`
}

// FlowDoc is one document on the order timeline.
type FlowDoc struct {
	DocType string `json:"doc_type"`
	DocID   string `json:"doc_id"`
	Number  string `json:"number"`
	Status  string `json:"status"`
}

// Invoice is a tax invoice draft or a registered document.
type Invoice struct {
	ID                string        `json:"id"`
	OrderID           string        `json:"order_id"`
	CustomerID        string        `json:"customer_id"`
	Status            string        `json:"status"`
	Number            string        `json:"number"`
	InvoiceDate       string        `json:"invoice_date"`
	SupplyDate        string        `json:"supply_date"`
	DueDate           string        `json:"due_date"`
	Currency          string        `json:"currency"`
	Gross             string        `json:"gross"`
	Discount          string        `json:"discount"`
	Tax               string        `json:"tax"`
	Rounding          string        `json:"rounding"`
	Total             string        `json:"total"`
	Lines             []InvoiceLine `json:"lines"`
	PrintEnabled      bool          `json:"print_enabled"`
	GatePassEnabled   bool          `json:"gate_pass_enabled"`
	CustomerVersionID string        `json:"customer_version_id"`
	PriceVersionID    string        `json:"price_version_id"`
	TaxCodeVersionID  string        `json:"tax_code_version_id"`
	ApprovedHash      string        `json:"approved_hash"`
	ContentHash       string        `json:"content_hash"`
	PDFHash           string        `json:"pdf_hash"`
	Warnings          []string      `json:"warnings"`
	StateVersion      int64         `json:"state_version"`
}

// InvoiceLine is one tax-invoice row copied from the order.
type InvoiceLine struct {
	SKU         string `json:"sku"`
	Description string `json:"description"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unit_price"`
	TaxCode     string `json:"tax_code"`
	TaxRate     string `json:"tax_rate"`
	TaxAmount   string `json:"tax_amount"`
	LineTotal   string `json:"line_total"`
}

// InvoicePatch replaces offered prices before registration.
type InvoicePatch struct {
	Lines []LineInput
}

// Print is the stored PDF. Reprint returns these same bytes.
type Print struct {
	Body []byte
	Hash string
}

// GateInput delivers some or all of a registered invoice.
type GateInput struct {
	InvoiceID string
	At        time.Time
	Lines     []LineInput
}

// Delivery is the note created by a gate pass.
type Delivery struct {
	ID          string         `json:"id"`
	InvoiceID   string         `json:"invoice_id"`
	OrderID     string         `json:"order_id"`
	GatePassID  string         `json:"gate_pass_id"`
	Status      string         `json:"status"`
	Lines       []DeliveryLine `json:"lines"`
	Backorder   []LineInput    `json:"backorder"`
	ClockDue    time.Time      `json:"clock_due"`
	ClockClosed bool           `json:"clock_closed"`
	ProofHash   string         `json:"proof_hash"`
	ConfirmedAt time.Time      `json:"confirmed_at"`
}

// DeliveryLine carries the moving-average unit cost at delivery.
type DeliveryLine struct {
	SKU      string `json:"sku"`
	Qty      string `json:"qty"`
	UnitCost string `json:"unit_cost"`
}

// Proof closes the delivery-note clock.
type Proof struct {
	Signature string
	PhotoSHA  string
	At        time.Time
	Lat       string
	Lng       string
}

// CreditInput references an original invoice and does not edit it.
type CreditInput struct {
	InvoiceID    string
	RestoreStock bool
	Reason       string
	Lines        []LineInput
}

// CreditNote is a new document.
type CreditNote struct {
	ID                string `json:"id"`
	OriginalInvoiceID string `json:"original_invoice_id"`
	Number            string `json:"number"`
	Total             string `json:"total"`
}

// CashInput is an invoice and a receipt for a walk-in customer.
type CashInput struct {
	CustomerID  string
	WarehouseID string
	AsOf        time.Time
	Lines       []LineInput
}

// CashSale is both documents from one transaction.
type CashSale struct {
	InvoiceID     string `json:"invoice_id"`
	ReceiptID     string `json:"receipt_id"`
	InvoiceNumber string `json:"invoice_number"`
	Total         string `json:"total"`
}

// ReceiptInput collects against one invoice.
type ReceiptInput struct {
	InvoiceID string
	Amount    string
	PaidOn    time.Time
}

// Receipt is a collection, with an early-payment discount when the window applies.
type Receipt struct {
	ID       string `json:"id"`
	Amount   string `json:"amount"`
	Discount string `json:"discount"`
}
