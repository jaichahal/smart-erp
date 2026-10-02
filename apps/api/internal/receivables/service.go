package receivables

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the receivables document rules. It does not allocate a bank statement line.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a service bound to the application pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) tx(ctx context.Context, p rls.Principal, fn func(pgx.Tx) error) error {
	ctx = rls.WithPrincipal(ctx, p)
	err := rls.Tx(ctx, s.pool, p, fn)
	return asAPI(err)
}

func asAPI(err error) error {
	if err == nil {
		return nil
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	return apierr.Wrap(apierr.Internal, "The request could not be completed.", err)
}

// CustomerInput is the collections profile used for dunning, interest, and credit review.
type CustomerInput struct {
	Name             string
	CreditLimit      int64
	DunningDays      []int
	InterestEnabled  bool
	AnnualInterestBP int
}

// Customer is the stored profile.
type Customer struct {
	ID string `json:"id"`
}

// CreateCustomer stores a customer the sales master has not supplied yet.
func (s *Service) CreateCustomer(ctx context.Context, p rls.Principal, in CustomerInput) (Customer, error) {
	if in.Name == "" {
		return Customer{}, apierr.New(apierr.ValidationError, "Customer name is required.")
	}
	id := uuid.New()
	days := make([]int32, 0, len(in.DunningDays))
	for _, d := range in.DunningDays {
		if d <= 0 {
			return Customer{}, apierr.New(apierr.ValidationError, "Dunning days must be positive.")
		}
		days = append(days, int32(d))
	}
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.ar_customers (company_id, id, name, credit_limit_fils, dunning_days, interest_enabled, annual_interest_bp)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			p.CompanyID, id, in.Name, in.CreditLimit, days, in.InterestEnabled, in.AnnualInterestBP)
		return err
	})
	return Customer{ID: id.String()}, err
}

// InvoiceInput is an open sales invoice. Sales owns the revenue document; this records the balance to collect.
type InvoiceInput struct {
	CustomerID  uuid.UUID
	Number      string
	InvoiceDate time.Time
	DueDate     time.Time
	Amount      int64
}

// InvoiceRef is the id of a stored invoice.
type InvoiceRef struct {
	ID string `json:"id"`
}

// CreateInvoice opens a receivable and debits the receivable role. It does not touch the bank.
func (s *Service) CreateInvoice(ctx context.Context, p rls.Principal, in InvoiceInput) (InvoiceRef, error) {
	if in.Number == "" || in.Amount <= 0 {
		return InvoiceRef{}, apierr.New(apierr.ValidationError, "Invoice number and amount are required.")
	}
	id := uuid.New()
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var exists int
		err := tx.QueryRow(ctx, `SELECT 1 FROM erp.ar_customers WHERE company_id = $1 AND id = $2`, p.CompanyID, in.CustomerID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Customer not found.")
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO erp.ar_invoices (company_id, id, customer_id, number, invoice_date, due_date, amount_fils, open_fils)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
			p.CompanyID, id, in.CustomerID, in.Number, in.InvoiceDate, in.DueDate, in.Amount)
		if err != nil {
			return err
		}
		if err := addParty(ctx, tx, p.CompanyID, in.CustomerID, in.InvoiceDate, "invoice", in.Number, in.Amount, 0); err != nil {
			return err
		}
		party := in.CustomerID
		_, err = insertBooks(ctx, tx, p.CompanyID, []bookLine{
			{role: "receivable", debit: in.Amount, docType: "sales_invoice", docID: id.String(), party: &party, postedOn: in.InvoiceDate},
			{role: "revenue", credit: in.Amount, docType: "sales_invoice", docID: id.String(), party: &party, postedOn: in.InvoiceDate},
		})
		return err
	})
	return InvoiceRef{ID: id.String()}, err
}

// Allocation is an amount of a receipt applied to one invoice.
type Allocation struct {
	InvoiceID uuid.UUID
	Amount    int64
}

// ReceiptInput is a collection. Method pdc posts to PDC in hand, not to the bank.
type ReceiptInput struct {
	CustomerID    uuid.UUID
	Method        string
	Amount        int64
	CollectorID   string
	PostedOn      time.Time
	BankAccountID *uuid.UUID
	CashAccountID *uuid.UUID
	ChequeNumber  string
	ChequeBank    string
	ChequeDate    *time.Time
	Allocations   []Allocation
}

// ReceiptResult is the collection after posting.
type ReceiptResult struct {
	ID              string    `json:"id"`
	Allocated       Money     `json:"allocated"`
	OnAccount       Money     `json:"on_account"`
	CustomerCredit  Money     `json:"customer_credit"`
	PDCInBalance    Money     `json:"pdc_in_balance"`
	BankBalance     Money     `json:"bank_balance,omitempty"`
	PDCID           string    `json:"pdc_id,omitempty"`
	PDCStateVersion string    `json:"pdc_state_version,omitempty"`
	StateVersion    string    `json:"state_version"`
	BankLineID      string    `json:"bank_line_id,omitempty"`
	Postings        []Posting `json:"postings,omitempty"`
}

// PostReceipt records a receipt. Unallocated amount becomes customer credit and does not reduce an invoice.
func (s *Service) PostReceipt(ctx context.Context, p rls.Principal, in ReceiptInput) (ReceiptResult, error) {
	if in.Amount <= 0 {
		return ReceiptResult{}, apierr.New(apierr.ValidationError, "Receipt amount is required.")
	}
	switch in.Method {
	case "cash", "current_cheque", "pdc", "transfer", "advance":
	default:
		return ReceiptResult{}, apierr.New(apierr.ValidationError, "Receipt method is not supported.")
	}
	if in.Method == "current_cheque" || in.Method == "pdc" {
		if in.ChequeNumber == "" || in.ChequeBank == "" || in.ChequeDate == nil {
			return ReceiptResult{}, apierr.New(apierr.ValidationError, "A cheque receipt requires number, bank, and date.")
		}
	}
	if in.Method == "advance" && len(in.Allocations) > 0 {
		return ReceiptResult{}, apierr.New(apierr.ValidationError, "An advance is applied to an invoice later.")
	}
	var allocTotal int64
	for _, a := range in.Allocations {
		if a.Amount <= 0 {
			return ReceiptResult{}, apierr.New(apierr.ValidationError, "Allocation amount is required.")
		}
		allocTotal += a.Amount
	}
	if allocTotal > in.Amount {
		return ReceiptResult{}, apierr.New(apierr.ValidationError, "Allocations cannot exceed the receipt.")
	}
	var out ReceiptResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		if in.Method == "cash" {
			if err := requireKind(ctx, tx, p.CompanyID, in.CashAccountID, "cash"); err != nil {
				return err
			}
		} else if err := requireKind(ctx, tx, p.CompanyID, in.BankAccountID, "bank"); err != nil {
			return err
		}
		for _, a := range in.Allocations {
			inv, err := lockInvoice(ctx, tx, p.CompanyID, a.InvoiceID)
			if err != nil {
				return err
			}
			if inv.customer != in.CustomerID {
				return apierr.New(apierr.ValidationError, "Invoice belongs to another customer.")
			}
			if a.Amount > inv.open {
				return apierr.New(apierr.ValidationError, "Allocation cannot exceed the invoice balance.")
			}
		}
		remainder := in.Amount - allocTotal
		receiptID := uuid.New()
		party := in.CustomerID
		lines := make([]bookLine, 0, 3)
		switch in.Method {
		case "cash":
			lines = append(lines, bookLine{role: "cash", account: in.CashAccountID, debit: in.Amount, docType: "receipt", docID: receiptID.String(), party: &party, postedOn: in.PostedOn})
		case "pdc":
			lines = append(lines, bookLine{role: "pdc_in", debit: in.Amount, docType: "pdc", docID: receiptID.String(), party: &party, postedOn: in.PostedOn})
		default:
			lines = append(lines, bookLine{role: "bank", account: in.BankAccountID, debit: in.Amount, docType: "receipt", docID: receiptID.String(), party: &party, postedOn: in.PostedOn})
		}
		if allocTotal > 0 {
			lines = append(lines, bookLine{role: "receivable", credit: allocTotal, docType: "receipt", docID: receiptID.String(), party: &party, postedOn: in.PostedOn})
		}
		if remainder > 0 {
			lines = append(lines, bookLine{role: "customer_advances", credit: remainder, docType: "receipt", docID: receiptID.String(), party: &party, postedOn: in.PostedOn})
		}
		ids, err := insertBooks(ctx, tx, p.CompanyID, lines)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO erp.ar_receipts (
				company_id, id, customer_id, collector_id, method, amount_fils, allocated_fils, posted_on,
				bank_account_id, cash_account_id, cheque_number, cheque_bank, cheque_date
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			p.CompanyID, receiptID, in.CustomerID, in.CollectorID, in.Method, in.Amount, allocTotal, in.PostedOn,
			in.BankAccountID, in.CashAccountID, in.ChequeNumber, in.ChequeBank, in.ChequeDate)
		if err != nil {
			return err
		}
		for _, a := range in.Allocations {
			if err := applyInvoice(ctx, tx, p.CompanyID, a.InvoiceID, a.Amount, in.Method == "pdc"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO erp.ar_allocations (company_id, id, receipt_id, invoice_id, amount_fils)
				VALUES ($1, $2, $3, $4, $5)`, p.CompanyID, uuid.New(), receiptID, a.InvoiceID, a.Amount); err != nil {
				return err
			}
		}
		if allocTotal > 0 {
			if err := addParty(ctx, tx, p.CompanyID, in.CustomerID, in.PostedOn, "receipt", receiptID.String(), 0, allocTotal); err != nil {
				return err
			}
		}
		if remainder > 0 {
			if err := addCredit(ctx, tx, p.CompanyID, in.CustomerID, remainder); err != nil {
				return err
			}
		}
		var pdcID uuid.UUID
		if in.Method == "pdc" {
			pdcID = uuid.New()
			_, err = tx.Exec(ctx, `
				INSERT INTO erp.ar_pdcs (
					company_id, id, direction, status, receipt_id, customer_id, amount_fils,
					cheque_number, cheque_bank, cheque_date, bank_account_id
				) VALUES ($1,$2,'received','in_hand',$3,$4,$5,$6,$7,$8,$9)`,
				p.CompanyID, pdcID, receiptID, in.CustomerID, in.Amount, in.ChequeNumber, in.ChequeBank, in.ChequeDate, in.BankAccountID)
			if err != nil {
				return err
			}
		}
		return s.fillReceipt(ctx, tx, p.CompanyID, receiptID, in, allocTotal, remainder, ids, lines, pdcID, &out)
	})
	return out, err
}

func (s *Service) fillReceipt(ctx context.Context, tx pgx.Tx, company, receiptID uuid.UUID, in ReceiptInput, allocTotal, remainder int64, ids []uuid.UUID, lines []bookLine, pdcID uuid.UUID, out *ReceiptResult) error {
	credit, err := customerCredit(ctx, tx, company, in.CustomerID)
	if err != nil {
		return err
	}
	pdcBal, err := roleNet(ctx, tx, company, "pdc_in")
	if err != nil {
		return err
	}
	out.ID = receiptID.String()
	out.Allocated = AED(allocTotal)
	out.OnAccount = AED(remainder)
	out.CustomerCredit = AED(credit)
	out.PDCInBalance = AED(pdcBal)
	out.StateVersion = "1"
	out.Postings = toPostings(lines)
	if in.BankAccountID != nil {
		bal, _, err := accountBalance(ctx, tx, company, *in.BankAccountID)
		if err != nil {
			return err
		}
		out.BankBalance = AED(bal)
	}
	for i, ln := range lines {
		if ln.role == "bank" {
			out.BankLineID = ids[i].String()
		}
	}
	if pdcID != uuid.Nil {
		out.PDCID = pdcID.String()
		out.PDCStateVersion = "1"
	}
	return nil
}

// ApplyInput moves customer credit onto an invoice. It does not post to the bank.
type ApplyInput struct {
	CustomerID uuid.UUID
	InvoiceID  uuid.UUID
	ReceiptID  uuid.UUID
	Amount     int64
}

// ApplyResult is the invoice after the credit is applied.
type ApplyResult struct {
	OpenAmount Money     `json:"open_amount"`
	Postings   []Posting `json:"postings"`
}

// ApplyAdvance applies a prior advance or on-account balance to an invoice.
func (s *Service) ApplyAdvance(ctx context.Context, p rls.Principal, in ApplyInput) (ApplyResult, error) {
	return s.allocate(ctx, p, in.ReceiptID, []Allocation{{InvoiceID: in.InvoiceID, Amount: in.Amount}}, &in.CustomerID)
}

// Allocate applies an existing receipt's on-account remainder. The bank balance is unchanged.
func (s *Service) Allocate(ctx context.Context, p rls.Principal, receiptID uuid.UUID, expected int64, allocs []Allocation) (ApplyResult, error) {
	var out ApplyResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var version int64
		err := tx.QueryRow(ctx, `SELECT state_version FROM erp.ar_receipts WHERE company_id = $1 AND id = $2 FOR UPDATE`, p.CompanyID, receiptID).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Receipt not found.")
		}
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, version, map[string]any{"state_version": fmt.Sprint(version)}); err != nil {
			return err
		}
		res, err := s.allocateTx(ctx, tx, p.CompanyID, receiptID, allocs, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.ar_receipts SET state_version = state_version + 1 WHERE company_id = $1 AND id = $2`, p.CompanyID, receiptID); err != nil {
			return err
		}
		out = res
		return nil
	})
	return out, err
}

func (s *Service) allocate(ctx context.Context, p rls.Principal, receiptID uuid.UUID, allocs []Allocation, customer *uuid.UUID) (ApplyResult, error) {
	var out ApplyResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		res, err := s.allocateTx(ctx, tx, p.CompanyID, receiptID, allocs, customer)
		out = res
		return err
	})
	return out, err
}

func (s *Service) allocateTx(ctx context.Context, tx pgx.Tx, company, receiptID uuid.UUID, allocs []Allocation, customer *uuid.UUID) (ApplyResult, error) {
	var amount, allocated int64
	var posted time.Time
	var cust uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT amount_fils, allocated_fils, posted_on, customer_id
		FROM erp.ar_receipts WHERE company_id = $1 AND id = $2 FOR UPDATE`, company, receiptID).Scan(&amount, &allocated, &posted, &cust)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, apierr.New(apierr.NotFound, "Receipt not found.")
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if customer != nil && *customer != cust {
		return ApplyResult{}, apierr.New(apierr.ValidationError, "Receipt belongs to another customer.")
	}
	var total int64
	for _, a := range allocs {
		if a.Amount <= 0 {
			return ApplyResult{}, apierr.New(apierr.ValidationError, "Allocation amount is required.")
		}
		total += a.Amount
	}
	if allocated+total > amount {
		return ApplyResult{}, apierr.New(apierr.ValidationError, "Allocations cannot exceed the receipt.")
	}
	var openLeft int64
	party := cust
	lines := []bookLine{
		{role: "customer_advances", debit: total, docType: "allocation", docID: receiptID.String(), party: &party, postedOn: posted},
		{role: "receivable", credit: total, docType: "allocation", docID: receiptID.String(), party: &party, postedOn: posted},
	}
	if _, err := insertBooks(ctx, tx, company, lines); err != nil {
		return ApplyResult{}, err
	}
	if err := spendCredit(ctx, tx, company, cust, total); err != nil {
		return ApplyResult{}, err
	}
	for _, a := range allocs {
		inv, err := lockInvoice(ctx, tx, company, a.InvoiceID)
		if err != nil {
			return ApplyResult{}, err
		}
		if inv.customer != cust {
			return ApplyResult{}, apierr.New(apierr.ValidationError, "Invoice belongs to another customer.")
		}
		if a.Amount > inv.open {
			return ApplyResult{}, apierr.New(apierr.ValidationError, "Allocation cannot exceed the invoice balance.")
		}
		if err := applyInvoice(ctx, tx, company, a.InvoiceID, a.Amount, false); err != nil {
			return ApplyResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.ar_allocations (company_id, id, receipt_id, invoice_id, amount_fils)
			VALUES ($1, $2, $3, $4, $5)`, company, uuid.New(), receiptID, a.InvoiceID, a.Amount); err != nil {
			return ApplyResult{}, err
		}
		openLeft = inv.open - a.Amount
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.ar_receipts SET allocated_fils = allocated_fils + $3 WHERE company_id = $1 AND id = $2`, company, receiptID, total); err != nil {
		return ApplyResult{}, err
	}
	if err := addParty(ctx, tx, company, cust, posted, "receipt", receiptID.String(), 0, total); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{OpenAmount: AED(openLeft), Postings: toPostings(lines)}, nil
}

// PDCMove is the cheque after deposit, bounce, issue, or presentation.
type PDCMove struct {
	ID              string `json:"id,omitempty"`
	BankBalance     Money  `json:"bank_balance"`
	PDCInBalance    Money  `json:"pdc_in_balance"`
	PDCOutLiability Money  `json:"pdc_out_liability"`
	StateVersion    string `json:"state_version"`
}

// Deposit moves a cheque from PDC in hand to the bank. It does not change the allocation.
func (s *Service) Deposit(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64) (PDCMove, error) {
	var out PDCMove
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		row, err := lockPDC(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, row.version, map[string]any{"state_version": fmt.Sprint(row.version)}); err != nil {
			return err
		}
		if row.status != "in_hand" || row.direction != "received" {
			return apierr.New(apierr.Conflict, "Only a cheque in hand can be deposited.")
		}
		if row.bank == nil {
			return apierr.New(apierr.ValidationError, "The cheque has no bank account.")
		}
		posted := row.chequeDate
		lines := []bookLine{
			{role: "bank", account: row.bank, debit: row.amount, docType: "pdc_deposit", docID: id.String(), postedOn: posted},
			{role: "pdc_in", credit: row.amount, docType: "pdc_deposit", docID: id.String(), postedOn: posted},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		allocs, err := loadAllocs(ctx, tx, p.CompanyID, row.receipt)
		if err != nil {
			return err
		}
		for _, a := range allocs {
			inv, err := lockInvoice(ctx, tx, p.CompanyID, a.invoice)
			if err != nil {
				return err
			}
			if a.amount > inv.pdc {
				return apierr.New(apierr.Conflict, "PDC cover on the invoice is short.")
			}
			if err := setInvoice(ctx, tx, p.CompanyID, a.invoice, inv.open, inv.pdc-a.amount, inv.bounced, inv.amount); err != nil {
				return err
			}
		}
		next := row.version + 1
		if _, err := tx.Exec(ctx, `UPDATE erp.ar_pdcs SET status = 'deposited', state_version = $3 WHERE company_id = $1 AND id = $2`, p.CompanyID, id, next); err != nil {
			return err
		}
		return fillPDC(ctx, tx, p.CompanyID, row.bank, next, &out)
	})
	return out, err
}

// Bounce reverses a cheque and reopens the invoices it covered. A deposited cheque leaves the bank.
func (s *Service) Bounce(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64, reason string) (PDCMove, error) {
	if reason == "" {
		return PDCMove{}, apierr.New(apierr.ValidationError, "A bounce requires a reason.")
	}
	var out PDCMove
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		row, err := lockPDC(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, row.version, map[string]any{"state_version": fmt.Sprint(row.version)}); err != nil {
			return err
		}
		if row.direction != "received" || (row.status != "in_hand" && row.status != "deposited") {
			return apierr.New(apierr.Conflict, "This cheque cannot be bounced.")
		}
		allocs, err := loadAllocs(ctx, tx, p.CompanyID, row.receipt)
		if err != nil {
			return err
		}
		var allocated int64
		for _, a := range allocs {
			allocated += a.amount
		}
		remainder := row.amount - allocated
		party := row.customer
		lines := make([]bookLine, 0, 3)
		if allocated > 0 {
			lines = append(lines, bookLine{role: "receivable", debit: allocated, docType: "pdc_bounce", docID: id.String(), party: &party, postedOn: row.chequeDate})
		}
		if remainder > 0 {
			lines = append(lines, bookLine{role: "customer_advances", debit: remainder, docType: "pdc_bounce", docID: id.String(), party: &party, postedOn: row.chequeDate})
		}
		if row.status == "deposited" {
			lines = append(lines, bookLine{role: "bank", account: row.bank, credit: row.amount, docType: "pdc_bounce", docID: id.String(), postedOn: row.chequeDate})
		} else {
			lines = append(lines, bookLine{role: "pdc_in", credit: row.amount, docType: "pdc_bounce", docID: id.String(), postedOn: row.chequeDate})
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		for _, a := range allocs {
			inv, err := lockInvoice(ctx, tx, p.CompanyID, a.invoice)
			if err != nil {
				return err
			}
			pdc := inv.pdc
			if row.status == "in_hand" {
				pdc -= a.amount
			}
			if pdc < 0 {
				return apierr.New(apierr.Conflict, "PDC cover on the invoice is short.")
			}
			if err := setInvoice(ctx, tx, p.CompanyID, a.invoice, inv.open+a.amount, pdc, true, inv.amount); err != nil {
				return err
			}
		}
		if remainder > 0 {
			if err := spendCredit(ctx, tx, p.CompanyID, row.customer, remainder); err != nil {
				return err
			}
		}
		next := row.version + 1
		if _, err := tx.Exec(ctx, `UPDATE erp.ar_pdcs SET status = 'bounced', state_version = $3 WHERE company_id = $1 AND id = $2`, p.CompanyID, id, next); err != nil {
			return err
		}
		return fillPDC(ctx, tx, p.CompanyID, row.bank, next, &out)
	})
	return out, err
}

// IssuedPDCInput is a cheque the company writes. It is a liability until the bank presents it.
type IssuedPDCInput struct {
	Amount        int64
	PostedOn      time.Time
	BankAccountID uuid.UUID
	ChequeNumber  string
	ChequeBank    string
	ChequeDate    time.Time
}

// IssuePDC credits PDC issued and does not move the bank.
func (s *Service) IssuePDC(ctx context.Context, p rls.Principal, in IssuedPDCInput) (PDCMove, error) {
	if in.Amount <= 0 || in.ChequeNumber == "" || in.ChequeBank == "" {
		return PDCMove{}, apierr.New(apierr.ValidationError, "An issued cheque requires amount, number, bank, and date.")
	}
	var out PDCMove
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		if err := requireKind(ctx, tx, p.CompanyID, &in.BankAccountID, "bank"); err != nil {
			return err
		}
		id := uuid.New()
		bank := in.BankAccountID
		lines := []bookLine{
			{role: "payable", debit: in.Amount, docType: "pdc_issued", docID: id.String(), postedOn: in.PostedOn},
			{role: "pdc_out", credit: in.Amount, docType: "pdc_issued", docID: id.String(), postedOn: in.PostedOn},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.ar_pdcs (
				company_id, id, direction, status, amount_fils, cheque_number, cheque_bank, cheque_date, bank_account_id
			) VALUES ($1,$2,'issued','issued',$3,$4,$5,$6,$7)`,
			p.CompanyID, id, in.Amount, in.ChequeNumber, in.ChequeBank, in.ChequeDate, in.BankAccountID)
		if err != nil {
			return err
		}
		out.ID = id.String()
		return fillPDC(ctx, tx, p.CompanyID, &bank, 1, &out)
	})
	return out, err
}

// PresentPDC clears an issued cheque out of the liability and out of the bank.
func (s *Service) PresentPDC(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64) (PDCMove, error) {
	var out PDCMove
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		row, err := lockPDC(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, row.version, map[string]any{"state_version": fmt.Sprint(row.version)}); err != nil {
			return err
		}
		if row.status != "issued" {
			return apierr.New(apierr.Conflict, "Only an issued cheque can be presented.")
		}
		lines := []bookLine{
			{role: "pdc_out", debit: row.amount, docType: "pdc_presented", docID: id.String(), postedOn: row.chequeDate},
			{role: "bank", account: row.bank, credit: row.amount, docType: "pdc_presented", docID: id.String(), postedOn: row.chequeDate},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		next := row.version + 1
		if _, err := tx.Exec(ctx, `UPDATE erp.ar_pdcs SET status = 'presented', state_version = $3 WHERE company_id = $1 AND id = $2`, p.CompanyID, id, next); err != nil {
			return err
		}
		return fillPDC(ctx, tx, p.CompanyID, row.bank, next, &out)
	})
	return out, err
}

func fillPDC(ctx context.Context, tx pgx.Tx, company uuid.UUID, bank *uuid.UUID, version int64, out *PDCMove) error {
	pdcIn, err := roleNet(ctx, tx, company, "pdc_in")
	if err != nil {
		return err
	}
	pdcOut, err := roleNet(ctx, tx, company, "pdc_out")
	if err != nil {
		return err
	}
	out.PDCInBalance = AED(pdcIn)
	out.PDCOutLiability = AED(-pdcOut)
	out.StateVersion = fmt.Sprint(version)
	if bank != nil {
		bal, _, err := accountBalance(ctx, tx, company, *bank)
		if err != nil {
			return err
		}
		out.BankBalance = AED(bal)
	}
	return nil
}

// ReceivableRow is one open invoice on the collections list.
type ReceivableRow struct {
	CustomerID  string `json:"customer_id"`
	Customer    string `json:"customer"`
	InvoiceDate string `json:"invoice_date"`
	DueDate     string `json:"due_date"`
	Number      string `json:"number"`
	OpenAmount  Money  `json:"open_amount"`
	PDCCovered  Money  `json:"pdc_covered"`
	Balance     Money  `json:"balance"`
	Bucket      string `json:"bucket"`
	Bounced     bool   `json:"bounced"`
}

// ReceivableList is the dashboard payload.
type ReceivableList struct {
	Rows []ReceivableRow `json:"rows"`
}

// ListFilter selects the dashboard.
type ListFilter struct {
	AsOf       time.Time
	Bucket     string
	Due        string
	PDCCovered bool
}

// List returns open amount and PDC-covered amount. Aging uses the due date.
func (s *Service) List(ctx context.Context, p rls.Principal, f ListFilter) (ReceivableList, error) {
	var rows []ReceivableRow
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `
			SELECT c.id, c.name, i.invoice_date, i.due_date, i.number, i.open_fils, i.pdc_covered_fils, i.bounced
			FROM erp.ar_invoices i
			JOIN erp.ar_customers c ON c.company_id = i.company_id AND c.id = i.customer_id
			WHERE i.company_id = $1
			ORDER BY i.due_date, i.number`, p.CompanyID)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var cust uuid.UUID
			var name, number string
			var invoiceDate, due time.Time
			var open, pdc int64
			var bounced bool
			if err := q.Scan(&cust, &name, &invoiceDate, &due, &number, &open, &pdc, &bounced); err != nil {
				return err
			}
			bucket := bucketOf(due, f.AsOf)
			if f.Bucket != "" && bucket != f.Bucket {
				continue
			}
			if f.PDCCovered && pdc == 0 {
				continue
			}
			if !dueMatches(due, f.AsOf, f.Due) {
				continue
			}
			rows = append(rows, ReceivableRow{
				CustomerID: cust.String(), Customer: name,
				InvoiceDate: ymd(invoiceDate), DueDate: ymd(due), Number: number,
				OpenAmount: AED(open), PDCCovered: AED(pdc), Balance: AED(open + pdc),
				Bucket: bucket, Bounced: bounced,
			})
		}
		return q.Err()
	})
	if rows == nil {
		rows = []ReceivableRow{}
	}
	return ReceivableList{Rows: rows}, err
}

// IDList is a list of document ids.
type IDList struct {
	Receipts []idRow `json:"receipts"`
}

type idRow struct {
	ID string `json:"id"`
}

// Dashboard returns the collector's receipts for the day. The read sees the committed receipt immediately.
func (s *Service) Dashboard(ctx context.Context, p rls.Principal, collector string, day time.Time) (IDList, error) {
	return s.receiptIDs(ctx, p, `
		SELECT id FROM erp.ar_receipts WHERE company_id = $1 AND collector_id = $2 AND posted_on = $3 ORDER BY id`, collector, day)
}

// Unallocated lists receipts that still have an on-account remainder. Those invoices stay open.
func (s *Service) Unallocated(ctx context.Context, p rls.Principal) (IDList, error) {
	var list IDList
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `
			SELECT id FROM erp.ar_receipts WHERE company_id = $1 AND allocated_fils < amount_fils ORDER BY posted_on, id`, p.CompanyID)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var id uuid.UUID
			if err := q.Scan(&id); err != nil {
				return err
			}
			list.Receipts = append(list.Receipts, idRow{ID: id.String()})
		}
		return q.Err()
	})
	if list.Receipts == nil {
		list.Receipts = []idRow{}
	}
	return list, err
}

func (s *Service) receiptIDs(ctx context.Context, p rls.Principal, sql, collector string, day time.Time) (IDList, error) {
	var list IDList
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, sql, p.CompanyID, collector, day)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var id uuid.UUID
			if err := q.Scan(&id); err != nil {
				return err
			}
			list.Receipts = append(list.Receipts, idRow{ID: id.String()})
		}
		return q.Err()
	})
	if list.Receipts == nil {
		list.Receipts = []idRow{}
	}
	return list, err
}

// StatementLine is one movement on the customer statement and the party ledger.
type StatementLine struct {
	Date    string `json:"date"`
	Kind    string `json:"kind"`
	Number  string `json:"number"`
	Debit   Money  `json:"debit"`
	Credit  Money  `json:"credit"`
	Running Money  `json:"running"`
}

// Statement is the customer statement. The party ledger is the same lines.
type Statement struct {
	Lines          []StatementLine `json:"lines"`
	RunningBalance Money           `json:"running_balance"`
}

// Statement returns invoices, receipts, and credits with a running balance.
func (s *Service) Statement(ctx context.Context, p rls.Principal, customer uuid.UUID, from, to time.Time) (Statement, error) {
	var out Statement
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		lines, err := partyLines(ctx, tx, p.CompanyID, customer, from, to)
		out = lines
		return err
	})
	return out, err
}

// CreditInput reduces a receivable the way a sales credit note would.
type CreditInput struct {
	CustomerID uuid.UUID
	InvoiceID  uuid.UUID
	Number     string
	PostedOn   time.Time
	Amount     int64
}

// RegisterCredit records a credit against an invoice.
func (s *Service) RegisterCredit(ctx context.Context, p rls.Principal, in CreditInput) (ApplyResult, error) {
	if in.Amount <= 0 || in.Number == "" {
		return ApplyResult{}, apierr.New(apierr.ValidationError, "Credit number and amount are required.")
	}
	var out ApplyResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		inv, err := lockInvoice(ctx, tx, p.CompanyID, in.InvoiceID)
		if err != nil {
			return err
		}
		if inv.customer != in.CustomerID || in.Amount > inv.open {
			return apierr.New(apierr.ValidationError, "Credit cannot exceed the invoice balance.")
		}
		party := in.CustomerID
		lines := []bookLine{
			{role: "revenue", debit: in.Amount, docType: "credit_note", docID: in.Number, party: &party, postedOn: in.PostedOn},
			{role: "receivable", credit: in.Amount, docType: "credit_note", docID: in.Number, party: &party, postedOn: in.PostedOn},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		if err := setInvoice(ctx, tx, p.CompanyID, in.InvoiceID, inv.open-in.Amount, inv.pdc, inv.bounced, inv.amount); err != nil {
			return err
		}
		if err := addParty(ctx, tx, p.CompanyID, in.CustomerID, in.PostedOn, "credit", in.Number, 0, in.Amount); err != nil {
			return err
		}
		out = ApplyResult{OpenAmount: AED(inv.open - in.Amount), Postings: toPostings(lines)}
		return nil
	})
	return out, err
}

// DunningLog is the reminders already sent for an invoice.
type DunningLog struct {
	Logs []dunningRow `json:"logs"`
}

type dunningRow struct {
	Days int `json:"days"`
}

// RunDunning sends reminders on the customer's configured days overdue and logs them on the invoice.
func (s *Service) RunDunning(ctx context.Context, p rls.Principal, asOf time.Time) error {
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `
			SELECT i.id, i.due_date, i.open_fils, i.pdc_covered_fils, c.dunning_days
			FROM erp.ar_invoices i
			JOIN erp.ar_customers c ON c.company_id = i.company_id AND c.id = i.customer_id
			WHERE i.company_id = $1`, p.CompanyID)
		if err != nil {
			return err
		}
		defer q.Close()
		type job struct {
			id   uuid.UUID
			days int
		}
		var jobs []job
		for q.Next() {
			var id uuid.UUID
			var due time.Time
			var open, pdc int64
			var days []int32
			if err := q.Scan(&id, &due, &open, &pdc, &days); err != nil {
				return err
			}
			if open+pdc <= 0 {
				continue
			}
			over := daysBetween(due, asOf)
			for _, d := range days {
				if int(d) == over {
					jobs = append(jobs, job{id: id, days: int(d)})
				}
			}
		}
		if err := q.Err(); err != nil {
			return err
		}
		for _, j := range jobs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO erp.ar_dunning (company_id, invoice_id, days, sent_on)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, p.CompanyID, j.id, j.days, asOf); err != nil {
				return err
			}
		}
		return nil
	})
}

// DunningFor returns the log rows on one invoice.
func (s *Service) DunningFor(ctx context.Context, p rls.Principal, invoice uuid.UUID) (DunningLog, error) {
	var out DunningLog
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `SELECT days FROM erp.ar_dunning WHERE company_id = $1 AND invoice_id = $2 ORDER BY days`, p.CompanyID, invoice)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var days int
			if err := q.Scan(&days); err != nil {
				return err
			}
			out.Logs = append(out.Logs, dunningRow{Days: days})
		}
		return q.Err()
	})
	if out.Logs == nil {
		out.Logs = []dunningRow{}
	}
	return out, err
}

// DraftNote is an overdue-interest debit note that is not on the books until the accountant issues it.
type DraftNote struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	BooksPosted  bool   `json:"books_posted"`
	Amount       Money  `json:"amount"`
	StateVersion string `json:"state_version"`
}

// DraftInterest computes overdue interest and stores a draft debit note.
func (s *Service) DraftInterest(ctx context.Context, p rls.Principal, customer, invoice uuid.UUID, asOf time.Time) (DraftNote, error) {
	var out DraftNote
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var enabled bool
		var bp int
		err := tx.QueryRow(ctx, `SELECT interest_enabled, annual_interest_bp FROM erp.ar_customers WHERE company_id = $1 AND id = $2`, p.CompanyID, customer).Scan(&enabled, &bp)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Customer not found.")
		}
		if err != nil {
			return err
		}
		if !enabled {
			return apierr.New(apierr.ValidationError, "This customer has no overdue interest.")
		}
		inv, err := lockInvoice(ctx, tx, p.CompanyID, invoice)
		if err != nil {
			return err
		}
		if inv.customer != customer {
			return apierr.New(apierr.ValidationError, "Invoice belongs to another customer.")
		}
		days := daysBetween(inv.due, asOf)
		amount := SimpleInterest(inv.open, int64(bp), int64(days))
		if amount <= 0 {
			return apierr.New(apierr.ValidationError, "There is no overdue interest to draft.")
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO erp.ar_debit_notes (company_id, id, customer_id, invoice_id, amount_fils, status)
			VALUES ($1, $2, $3, $4, $5, 'draft')`, p.CompanyID, id, customer, invoice, amount)
		if err != nil {
			return err
		}
		out = DraftNote{ID: id.String(), Status: "draft", BooksPosted: false, Amount: AED(amount), StateVersion: "1"}
		return nil
	})
	return out, err
}

// IssuedNote is a debit note the accountant chose to post.
type IssuedNote struct {
	Status   string    `json:"status"`
	Postings []Posting `json:"postings"`
}

// IssueDebit posts a draft overdue-interest debit note.
func (s *Service) IssueDebit(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64) (IssuedNote, error) {
	var out IssuedNote
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var status string
		var version, amount int64
		var customer, invoice uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT status, state_version, amount_fils, customer_id, invoice_id
			FROM erp.ar_debit_notes WHERE company_id = $1 AND id = $2 FOR UPDATE`, p.CompanyID, id).Scan(&status, &version, &amount, &customer, &invoice)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Debit note not found.")
		}
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, version, map[string]any{"state_version": fmt.Sprint(version)}); err != nil {
			return err
		}
		if status != "draft" {
			return apierr.New(apierr.Conflict, "The debit note is already issued.")
		}
		inv, err := lockInvoice(ctx, tx, p.CompanyID, invoice)
		if err != nil {
			return err
		}
		posted := time.Now().UTC()
		party := customer
		lines := []bookLine{
			{role: "receivable", debit: amount, docType: "debit_note", docID: id.String(), party: &party, postedOn: posted},
			{role: "interest_income", credit: amount, docType: "debit_note", docID: id.String(), party: &party, postedOn: posted},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		if err := setInvoice(ctx, tx, p.CompanyID, invoice, inv.open+amount, inv.pdc, inv.bounced, inv.amount+amount); err != nil {
			return err
		}
		if err := addParty(ctx, tx, p.CompanyID, customer, posted, "interest", id.String(), amount, 0); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.ar_debit_notes SET status = 'issued', state_version = state_version + 1 WHERE company_id = $1 AND id = $2`, p.CompanyID, id); err != nil {
			return err
		}
		out = IssuedNote{Status: "issued", Postings: toPostings(lines)}
		return nil
	})
	return out, err
}

// ReviewItem is a customer on the credit review queue.
type ReviewItem struct {
	CustomerID string `json:"customer_id"`
	Reason     string `json:"reason"`
}

// ReviewList is the credit review queue.
type ReviewList struct {
	Customers []ReviewItem `json:"customers"`
}

// CreditReview lists customers over 90 days overdue or over 80 percent of their limit.
func (s *Service) CreditReview(ctx context.Context, p rls.Principal, asOf time.Time) (ReviewList, error) {
	var out ReviewList
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `
			SELECT c.id, c.credit_limit_fils, COALESCE(SUM(i.open_fils + i.pdc_covered_fils), 0),
			       COALESCE(MAX(CASE WHEN i.due_date < $2 THEN ($2::date - i.due_date) ELSE 0 END), 0)
			FROM erp.ar_customers c
			LEFT JOIN erp.ar_invoices i ON i.company_id = c.company_id AND i.customer_id = c.id
			WHERE c.company_id = $1
			GROUP BY c.id, c.credit_limit_fils`, p.CompanyID, asOf)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var id uuid.UUID
			var limit, exposure int64
			var oldest int
			if err := q.Scan(&id, &limit, &exposure, &oldest); err != nil {
				return err
			}
			reason := ""
			if oldest > 90 {
				reason = "over_90"
			} else if limit > 0 && exposure*100 > limit*80 {
				reason = "over_limit"
			}
			if reason != "" {
				out.Customers = append(out.Customers, ReviewItem{CustomerID: id.String(), Reason: reason})
			}
		}
		return q.Err()
	})
	if out.Customers == nil {
		out.Customers = []ReviewItem{}
	}
	return out, err
}

// SetConcentration stores the share above which a customer is flagged.
func (s *Service) SetConcentration(ctx context.Context, p rls.Principal, shareBP int64) error {
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.ar_settings (company_id, concentration_bp) VALUES ($1, $2)
			ON CONFLICT (company_id) DO UPDATE SET concentration_bp = EXCLUDED.concentration_bp`, p.CompanyID, shareBP)
		return err
	})
}

// FlaggedCustomer exceeds the configured receivable share.
type FlaggedCustomer struct {
	CustomerID string `json:"customer_id"`
	Share      string `json:"share"`
}

// ConcentrationList is the risk indicator.
type ConcentrationList struct {
	Flagged []FlaggedCustomer `json:"flagged"`
}

// Concentration flags customers whose exposure exceeds the configured share of the book.
func (s *Service) Concentration(ctx context.Context, p rls.Principal) (ConcentrationList, error) {
	var out ConcentrationList
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var share int64 = 10000
		err := tx.QueryRow(ctx, `SELECT concentration_bp FROM erp.ar_settings WHERE company_id = $1`, p.CompanyID).Scan(&share)
		if errors.Is(err, pgx.ErrNoRows) {
			share = 10000
		} else if err != nil {
			return err
		}
		q, err := tx.Query(ctx, `
			SELECT c.id, COALESCE(SUM(i.open_fils + i.pdc_covered_fils), 0)
			FROM erp.ar_customers c
			LEFT JOIN erp.ar_invoices i ON i.company_id = c.company_id AND i.customer_id = c.id
			WHERE c.company_id = $1
			GROUP BY c.id`, p.CompanyID)
		if err != nil {
			return err
		}
		defer q.Close()
		type row struct {
			id  uuid.UUID
			exp int64
		}
		var rows []row
		var total int64
		for q.Next() {
			var r row
			if err := q.Scan(&r.id, &r.exp); err != nil {
				return err
			}
			total += r.exp
			rows = append(rows, r)
		}
		if err := q.Err(); err != nil {
			return err
		}
		for _, r := range rows {
			if total > 0 && r.exp*10000 > total*share {
				out.Flagged = append(out.Flagged, FlaggedCustomer{CustomerID: r.id.String(), Share: Format(r.exp * 10000 / total)})
			}
		}
		return nil
	})
	if out.Flagged == nil {
		out.Flagged = []FlaggedCustomer{}
	}
	return out, err
}

// ShareResult is a statement sent with a PDF attached.
type ShareResult struct {
	Channel    string     `json:"channel"`
	Attachment attachment `json:"attachment"`
}

type attachment struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
}

// SendStatement stores an email or WhatsApp share with the PDF attached.
func (s *Service) SendStatement(ctx context.Context, p rls.Principal, customer uuid.UUID, channel string, from, to time.Time) (ShareResult, error) {
	if channel != "email" && channel != "whatsapp" {
		return ShareResult{}, apierr.New(apierr.ValidationError, "Share channel must be email or whatsapp.")
	}
	var out ShareResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		lines, err := partyLines(ctx, tx, p.CompanyID, customer, from, to)
		if err != nil {
			return err
		}
		pdf := statementPDF(customer.String(), lines.RunningBalance.Amount)
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.ar_shares (company_id, customer_id, channel, pdf) VALUES ($1, $2, $3, $4)`,
			p.CompanyID, customer, channel, pdf); err != nil {
			return err
		}
		out = ShareResult{
			Channel: channel,
			Attachment: attachment{
				Filename: "statement.pdf", ContentBase64: encodePDF(pdf),
			},
		}
		return nil
	})
	return out, err
}
