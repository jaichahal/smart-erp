package receivables

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type moneyIn struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Mount registers receivables routes on the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps) {
	s := New(deps.Pool)
	idem := idempotency.Middleware(deps.Pool)
	r.Get("/receivables", s.handleList)
	r.With(idem).Post("/receivables/customers", s.handleCustomer)
	r.With(idem).Post("/receivables/invoices", s.handleInvoice)
	r.With(idem).Post("/receivables/credits", s.handleCredit)
	r.With(idem).Post("/receivables/dunning/run", s.handleDunning)
	r.Get("/receivables/invoices/{id}/dunning", s.handleDunningLog)
	r.With(idem).Post("/receivables/interest/drafts", s.handleDraft)
	r.With(idem).Post("/receivables/debit-notes/{id}/issue", s.handleIssue)
	r.Get("/receivables/credit-review", s.handleReview)
	r.With(idem).Post("/receivables/settings", s.handleSettings)
	r.Get("/receivables/concentration", s.handleConcentration)
	r.With(idem).Post("/receipts", s.handleReceipt)
	r.Get("/receipts/unallocated", s.handleUnallocated)
	r.With(idem).Post("/receipts/{id}/allocate", s.handleAllocate)
	r.With(idem).Post("/advances/apply", s.handleApply)
	r.With(idem).Post("/pdcs/issued", s.handleIssued)
	r.With(idem).Post("/pdcs/{id}/deposit", s.handleDeposit)
	r.With(idem).Post("/pdcs/{id}/bounce", s.handleBounce)
	r.With(idem).Post("/pdcs/{id}/present", s.handlePresent)
	r.Get("/collectors/{id}/dashboard", s.handleDashboard)
	r.Get("/customers/{id}/statement", s.handleStatement)
	r.Get("/customers/{id}/ledger", s.handleStatement)
	r.With(idem).Post("/customers/{id}/statement/send", s.handleSend)
}

func caller(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "Authentication is required."))
		return rls.Principal{}, false
	}
	return p, true
}

func (s *Service) handleCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Name             string  `json:"name"`
		CreditLimit      moneyIn `json:"credit_limit"`
		DunningDays      []int   `json:"dunning_days"`
		InterestEnabled  bool    `json:"interest_enabled"`
		AnnualInterestBP int     `json:"annual_interest_bp"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	limit, err := ParseAmount(body.CreditLimit.Amount, body.CreditLimit.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.CreateCustomer(r.Context(), p, CustomerInput{
		Name: body.Name, CreditLimit: limit, DunningDays: body.DunningDays,
		InterestEnabled: body.InterestEnabled, AnnualInterestBP: body.AnnualInterestBP,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID  string  `json:"customer_id"`
		Number      string  `json:"number"`
		InvoiceDate string  `json:"invoice_date"`
		DueDate     string  `json:"due_date"`
		Amount      moneyIn `json:"amount"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cust, err := parseUUID(body.CustomerID, "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	invDate, err := parseDate(body.InvoiceDate)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	due, err := parseDate(body.DueDate)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	amount, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.CreateInvoice(r.Context(), p, InvoiceInput{CustomerID: cust, Number: body.Number, InvoiceDate: invDate, DueDate: due, Amount: amount})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID    string  `json:"customer_id"`
		Method        string  `json:"method"`
		Amount        moneyIn `json:"amount"`
		CollectorID   string  `json:"collector_id"`
		PostedOn      string  `json:"posted_on"`
		BankAccountID string  `json:"bank_account_id"`
		CashAccountID string  `json:"cash_account_id"`
		Allocations   []struct {
			InvoiceID string  `json:"invoice_id"`
			Amount    moneyIn `json:"amount"`
		} `json:"allocations"`
		Cheque *struct {
			Number string `json:"number"`
			Bank   string `json:"bank"`
			Date   string `json:"date"`
		} `json:"cheque"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cust, err := parseUUID(body.CustomerID, "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	amount, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	posted, err := parseDate(body.PostedOn)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	in := ReceiptInput{CustomerID: cust, Method: body.Method, Amount: amount, CollectorID: body.CollectorID, PostedOn: posted}
	if body.BankAccountID != "" {
		id, err := parseUUID(body.BankAccountID, "Bank account")
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		in.BankAccountID = &id
	}
	if body.CashAccountID != "" {
		id, err := parseUUID(body.CashAccountID, "Cash account")
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		in.CashAccountID = &id
	}
	if body.Cheque != nil {
		in.ChequeNumber = body.Cheque.Number
		in.ChequeBank = body.Cheque.Bank
		day, err := parseDate(body.Cheque.Date)
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		in.ChequeDate = &day
	}
	for _, a := range body.Allocations {
		inv, err := parseUUID(a.InvoiceID, "Invoice")
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		amt, err := ParseAmount(a.Amount.Amount, a.Amount.Currency)
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		in.Allocations = append(in.Allocations, Allocation{InvoiceID: inv, Amount: amt})
	}
	out, err := s.PostReceipt(r.Context(), p, in)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleAllocate(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Receipt")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	allocs, err := readAllocs(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.Allocate(r.Context(), p, id, version, allocs)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func readAllocs(r *http.Request) ([]Allocation, error) {
	var body struct {
		Allocations []struct {
			InvoiceID string  `json:"invoice_id"`
			Amount    moneyIn `json:"amount"`
		} `json:"allocations"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return nil, err
	}
	out := make([]Allocation, 0, len(body.Allocations))
	for _, a := range body.Allocations {
		inv, err := parseUUID(a.InvoiceID, "Invoice")
		if err != nil {
			return nil, err
		}
		amt, err := ParseAmount(a.Amount.Amount, a.Amount.Currency)
		if err != nil {
			return nil, err
		}
		out = append(out, Allocation{InvoiceID: inv, Amount: amt})
	}
	return out, nil
}

func (s *Service) handleApply(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID string  `json:"customer_id"`
		InvoiceID  string  `json:"invoice_id"`
		ReceiptID  string  `json:"receipt_id"`
		Amount     moneyIn `json:"amount"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cust, err := parseUUID(body.CustomerID, "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	inv, err := parseUUID(body.InvoiceID, "Invoice")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	rec, err := parseUUID(body.ReceiptID, "Receipt")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	amt, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.ApplyAdvance(r.Context(), p, ApplyInput{CustomerID: cust, InvoiceID: inv, ReceiptID: rec, Amount: amt})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleDeposit(w http.ResponseWriter, r *http.Request) {
	s.pdcAction(w, r, func(p rls.Principal, id uuid.UUID, version int64) (any, error) {
		return s.Deposit(r.Context(), p, id, version)
	})
}

func (s *Service) handlePresent(w http.ResponseWriter, r *http.Request) {
	s.pdcAction(w, r, func(p rls.Principal, id uuid.UUID, version int64) (any, error) {
		return s.PresentPDC(r.Context(), p, id, version)
	})
}

func (s *Service) pdcAction(w http.ResponseWriter, r *http.Request, fn func(rls.Principal, uuid.UUID, int64) (any, error)) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Cheque")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var discard struct{}
	if err := httpx.DecodeJSON(r, &discard); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := fn(p, id, version)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleBounce(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Cheque")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.Bounce(r.Context(), p, id, version, body.Reason)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleIssued(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Amount        moneyIn `json:"amount"`
		PostedOn      string  `json:"posted_on"`
		BankAccountID string  `json:"bank_account_id"`
		Cheque        struct {
			Number string `json:"number"`
			Bank   string `json:"bank"`
			Date   string `json:"date"`
		} `json:"cheque"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	amount, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	posted, err := parseDate(body.PostedOn)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	bankID, err := parseUUID(body.BankAccountID, "Bank account")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	chequeDate, err := parseDate(body.Cheque.Date)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.IssuePDC(r.Context(), p, IssuedPDCInput{
		Amount: amount, PostedOn: posted, BankAccountID: bankID,
		ChequeNumber: body.Cheque.Number, ChequeBank: body.Cheque.Bank, ChequeDate: chequeDate,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	asOf, err := queryDate(r, "as_of", time.Now().UTC())
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.List(r.Context(), p, ListFilter{
		AsOf: asOf, Bucket: r.URL.Query().Get("bucket"), Due: r.URL.Query().Get("due"),
		PDCCovered: r.URL.Query().Get("pdc_covered") == "1",
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleDashboard(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	day, err := queryDate(r, "date", time.Now().UTC())
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.Dashboard(r.Context(), p, chi.URLParam(r, "id"), day)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleUnallocated(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	out, err := s.Unallocated(r.Context(), p)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleStatement(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, from, to, err := customerSpan(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.Statement(r.Context(), p, id, from, to)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleSend(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var body struct {
		Channel string `json:"channel"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	from, err := parseDate(body.From)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	to, err := parseDate(body.To)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.SendStatement(r.Context(), p, id, body.Channel, from, to)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleCredit(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID string  `json:"customer_id"`
		InvoiceID  string  `json:"invoice_id"`
		Number     string  `json:"number"`
		PostedOn   string  `json:"posted_on"`
		Amount     moneyIn `json:"amount"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cust, err := parseUUID(body.CustomerID, "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	inv, err := parseUUID(body.InvoiceID, "Invoice")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	posted, err := parseDate(body.PostedOn)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	amt, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.RegisterCredit(r.Context(), p, CreditInput{CustomerID: cust, InvoiceID: inv, Number: body.Number, PostedOn: posted, Amount: amt})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleDunning(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AsOf string `json:"as_of"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	asOf, err := parseDate(body.AsOf)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := s.RunDunning(r.Context(), p, asOf); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"ran": true})
}

func (s *Service) handleDunningLog(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Invoice")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.DunningFor(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID string `json:"customer_id"`
		InvoiceID  string `json:"invoice_id"`
		AsOf       string `json:"as_of"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cust, err := parseUUID(body.CustomerID, "Customer")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	inv, err := parseUUID(body.InvoiceID, "Invoice")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	asOf, err := parseDate(body.AsOf)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.DraftInterest(r.Context(), p, cust, inv, asOf)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleIssue(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "Debit note")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var discard struct{}
	if err := httpx.DecodeJSON(r, &discard); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.IssueDebit(r.Context(), p, id, version)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleReview(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	asOf, err := queryDate(r, "as_of", time.Now().UTC())
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.CreditReview(r.Context(), p, asOf)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Share string `json:"concentration_share"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	share, err := ParseShare(body.Share)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := s.SetConcentration(r.Context(), p, share); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"concentration_share": body.Share})
}

func (s *Service) handleConcentration(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	out, err := s.Concentration(r.Context(), p)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func queryDate(r *http.Request, key string, fallback time.Time) (time.Time, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return time.Date(fallback.Year(), fallback.Month(), fallback.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return parseDate(raw)
}

func customerSpan(r *http.Request) (uuid.UUID, time.Time, time.Time, error) {
	id, err := parseUUID(chi.URLParam(r, "id"), "Customer")
	if err != nil {
		return uuid.Nil, time.Time{}, time.Time{}, err
	}
	from, err := parseDate(r.URL.Query().Get("from"))
	if err != nil {
		return uuid.Nil, time.Time{}, time.Time{}, err
	}
	to, err := parseDate(r.URL.Query().Get("to"))
	if err != nil {
		return uuid.Nil, time.Time{}, time.Time{}, err
	}
	return id, from, to, nil
}
