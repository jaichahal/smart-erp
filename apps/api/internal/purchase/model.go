package purchase

import (
	"time"

	"github.com/google/uuid"
)

const (
	kindOnboarding = "onboarding"
	kindSKU        = "sku_add"
	kindBlacklist  = "blacklist"
	kindPayment    = "payment_release"
	kindBank       = "bank_change"

	docOnboarding = "vendor_onboarding"
	docSKU        = "vendor_sku"
	docBlacklist  = "vendor_blacklist"
	docPayRelease = "vendor_blacklist_release"
	docBank       = "vendor_bank_change"

	classRaw      = "raw_material"
	classFinished = "finished_goods"
	classBoth     = "both"

	statusOpen     = "open"
	statusDraft    = "draft"
	statusApproved = "approved"
	statusPosted   = "posted"
	statusClosed   = "closed"
	statusBlocked  = "blocked"
	statusHeld     = "held"
	statusPaid     = "paid"

	// PostingGap is why a commercially accepted receipt or supplier invoice
	// has no ledger journal on this branch.
	PostingGap = "ledger interface absent: no inventory, received-not-billed, VAT input, or payable journal"
)

// Gate is an approval request the accountant prepared.
type Gate struct {
	ID           uuid.UUID `json:"id"`
	ApprovalID   string    `json:"approval_id"`
	StateVersion int       `json:"state_version"`
	Kind         string    `json:"kind"`
}

// Vendor is an approved vendor on the existing vendor master.
type Vendor struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	BankAccount     string    `json:"bank_account"`
	TradeLicenceKey string    `json:"trade_licence_key"`
	Blacklisted     bool      `json:"blacklisted"`
	PreparedBy      string    `json:"prepared_by"`
	ApprovalRequest string    `json:"approval_request_id"`
}

// LineInput is one purchase line.
type LineInput struct {
	SKUID     uuid.UUID `json:"sku_id"`
	Qty       string    `json:"qty"`
	UnitPrice string    `json:"unit_price"`
}

// DocInput is a requisition, quote, LPO, supplier invoice, receipt, or price agreement.
type DocInput struct {
	VendorID       uuid.UUID   `json:"vendor_id"`
	Currency       string      `json:"currency"`
	FXRate         string      `json:"fx_rate"`
	SourceID       *uuid.UUID  `json:"source_id"`
	SupplierNumber string      `json:"supplier_number"`
	CostCentre     string      `json:"cost_centre"`
	EffectiveFrom  string      `json:"effective_from"`
	EffectiveTo    string      `json:"effective_to"`
	Lines          []LineInput `json:"lines"`
}

// OnboardingInput is the file an accountant prepares.
type OnboardingInput struct {
	Name            string      `json:"name"`
	BankAccount     string      `json:"bank_account"`
	TradeLicenceKey string      `json:"trade_licence_key"`
	SKUIDs          []uuid.UUID `json:"sku_ids"`
}

// Document is a stored purchase document.
type Document struct {
	ID             uuid.UUID  `json:"id"`
	DocType        string     `json:"doc_type"`
	Number         string     `json:"number"`
	Status         string     `json:"status"`
	VendorID       uuid.UUID  `json:"vendor_id"`
	Amount         string     `json:"amount"`
	Currency       string     `json:"currency"`
	FXRate         string     `json:"fx_rate,omitempty"`
	BankAccount    string     `json:"bank_account"`
	SupplierNumber string     `json:"supplier_number,omitempty"`
	LedgerPosted   bool       `json:"ledger_posted"`
	PostingGap     string     `json:"posting_gap,omitempty"`
	Flagged        bool       `json:"flagged"`
	SourceID       *uuid.UUID `json:"source_id,omitempty"`
	Lines          []Line     `json:"lines"`
	EffectiveFrom  string     `json:"effective_from,omitempty"`
	EffectiveTo    string     `json:"effective_to,omitempty"`
}

// Line is a stored document line.
type Line struct {
	SKUID     uuid.UUID `json:"sku_id"`
	Qty       string    `json:"qty"`
	UnitPrice string    `json:"unit_price"`
}

// InvoiceBrief is one prior supplier invoice on a review file.
type InvoiceBrief struct {
	ID             uuid.UUID `json:"id"`
	Number         string    `json:"number"`
	Status         string    `json:"status"`
	SupplierNumber string    `json:"supplier_number"`
	Flagged        bool      `json:"flagged"`
}

// Review is what the reviewer sees before the final gate.
type Review struct {
	RequestID       uuid.UUID      `json:"request_id"`
	TradeLicenceKey string         `json:"trade_licence_key"`
	BankAccount     string         `json:"bank_account"`
	SKUIDs          []string       `json:"sku_ids"`
	PriorInvoices   []InvoiceBrief `json:"prior_invoices"`
}

// PriceSource is a quote or price agreement used in the rank.
type PriceSource struct {
	DocumentID    uuid.UUID `json:"document_id"`
	Number        string    `json:"number"`
	UnitPrice     string    `json:"unit_price"`
	Currency      string    `json:"currency"`
	FXRate        string    `json:"fx_rate,omitempty"`
	AED           string    `json:"aed,omitempty"`
	EffectiveFrom string    `json:"effective_from,omitempty"`
	EffectiveTo   string    `json:"effective_to,omitempty"`
	VendorID      uuid.UUID `json:"vendor_id"`
	DocType       string    `json:"doc_type"`
}

// BlockedPrice is a source that is not ranked, with the reason.
type BlockedPrice struct {
	VendorID uuid.UUID    `json:"vendor_id"`
	Reason   string       `json:"reason"`
	Source   *PriceSource `json:"source,omitempty"`
}

// SKUCount is active and past supplier invoices for one SKU.
type SKUCount struct {
	SKUID  uuid.UUID `json:"sku_id"`
	Active int       `json:"active"`
	Past   int       `json:"past"`
}

// VendorRow is one dashboard vendor, with invoice counts.
type VendorRow struct {
	VendorID       uuid.UUID  `json:"vendor_id"`
	Name           string     `json:"name"`
	Blacklisted    bool       `json:"blacklisted"`
	ActiveInvoices int        `json:"active_invoices"`
	PastInvoices   int        `json:"past_invoices"`
	SKUs           []SKUCount `json:"skus"`
}

// Dashboard is the vendor query the console and both phone apps read.
type Dashboard struct {
	Actions       []string       `json:"actions"`
	Vendors       []VendorRow    `json:"vendors"`
	Best          *PriceSource   `json:"best"`
	Blocked       []BlockedPrice `json:"blocked"`
	ShownUnranked []PriceSource  `json:"shown_unranked"`
	AsOf          time.Time      `json:"as_of"`
}
