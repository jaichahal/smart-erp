package bank

import (
	"net/http"
	"strconv"
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

// Mount registers bank, petty cash, and cash-position routes on the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps) {
	s := New(deps.Pool)
	idem := idempotency.Middleware(deps.Pool)
	r.With(idem).Post("/bank/accounts", s.handleOpen)
	r.Get("/bank/accounts/{id}", s.handleAccount)
	r.With(idem).Post("/bank/statements/import", s.handleImport)
	r.Get("/bank/reconciliation", s.handleRecon)
	r.With(idem).Post("/bank/reconciliation/match", s.handleMatch)
	r.With(idem).Post("/bank/reconciliation/unmatch", s.handleUnmatch)
	r.With(idem).Post("/bank/reconciliation/explain", s.handleExplain)
	r.With(idem).Post("/bank/reconciliation/rules", s.handleRule)
	r.With(idem).Post("/bank/reconciliation/auto", s.handleAuto)
	r.With(idem).Post("/bank/feed/consent", s.handleConsent)
	r.With(idem).Post("/bank/feed/run", s.handleFeedRun)
	r.With(idem).Post("/bank/feed/fail", s.handleFeedFail)
	r.Get("/bank/feed/status", s.handleFeed)
	r.With(idem).Post("/bank/contra", s.handleContra)
	r.With(idem).Post("/bank/payments", s.handlePayment)
	r.With(idem).Post("/bank/cheques", s.handleCheque)
	r.Get("/bank/cheques/gaps", s.handleGaps)
	r.With(idem).Post("/bank/cheques/{id}/stop", s.handleStop)
	r.With(idem).Post("/bank/cheques/{id}/clear", s.handleClear)
	r.With(idem).Post("/bank/facilities", s.handleFacility)
	r.With(idem).Post("/bank/facilities/{id}/draw", s.handleDraw)
	r.With(idem).Post("/bank/facilities/{id}/interest", s.handleInterest)
	r.Get("/bank/cash-position", s.handlePosition)
	r.With(idem).Post("/petty-cash-vouchers", s.handleVoucher)
}

func caller(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "Authentication is required."))
		return rls.Principal{}, false
	}
	return p, true
}

func (s *Service) handleOpen(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string  `json:"name"`
		Kind        string  `json:"kind"`
		Opening     moneyIn `json:"opening"`
		OpeningDate string  `json:"opening_date"`
		CustodianID string  `json:"custodian_id"`
		Float       moneyIn `json:"float"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	opening, err := ParseAmount(body.Opening.Amount, body.Opening.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	floatAmt, err := ParseAmount(body.Float.Amount, body.Float.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", body.OpeningDate)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	out, err := s.OpenAccount(r.Context(), p, AccountInput{
		Name: body.Name, Kind: body.Kind, Opening: opening, OpeningDate: day,
		CustodianID: body.CustodianID, Float: floatAmt,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	out, err := s.Account(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleImport(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Format    string `json:"format"`
		Body      string `json:"body"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	out, err := s.ImportStatement(r.Context(), p, id, body.Format, body.Body)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleRecon(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, day, err := accountQuery(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.Reconcile(r.Context(), p, id, day)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleMatch(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		LineID     string `json:"line_id"`
		BookLineID string `json:"book_line_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	lineID, err := uuid.Parse(body.LineID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Statement line is not an id."))
		return
	}
	bookID, err := uuid.Parse(body.BookLineID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Book line is not an id."))
		return
	}
	if err := s.Match(r.Context(), p, lineID, bookID); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"matched": true})
}

func (s *Service) handleUnmatch(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		LineID string `json:"line_id"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	lineID, err := uuid.Parse(body.LineID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Statement line is not an id."))
		return
	}
	if err := s.Unmatch(r.Context(), p, lineID, body.Reason); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"unmatched": true})
}

func (s *Service) handleExplain(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		LineID string `json:"line_id"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	lineID, err := uuid.Parse(body.LineID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Statement line is not an id."))
		return
	}
	if err := s.Explain(r.Context(), p, lineID, body.Reason); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"explained": true})
}

func (s *Service) handleRule(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Kind      string `json:"kind"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	if err := s.SaveRule(r.Context(), p, id, body.Kind); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"kind": body.Kind})
}

func (s *Service) handleAuto(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	if err := s.AutoMatch(r.Context(), p, id); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"proposed": true})
}

func (s *Service) handleConsent(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		ExpiresOn string `json:"expires_on"`
		Provider  string `json:"provider"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	exp, err := time.Parse("2006-01-02", body.ExpiresOn)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	if err := s.GiveConsent(r.Context(), p, id, exp, body.Provider); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"expires_on": body.ExpiresOn})
}

func (s *Service) handleFeedRun(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Format    string `json:"format"`
		Body      string `json:"body"`
		AsOf      string `json:"as_of"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	asOf, err := time.Parse("2006-01-02", body.AsOf)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	out, err := s.RunFeed(r.Context(), p, id, body.Format, body.Body, asOf)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleFeedFail(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Message   string `json:"message"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	if err := s.FailFeed(r.Context(), p, id, body.Message); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"alert": true})
}

func (s *Service) handleFeed(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.URL.Query().Get("account"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	out, err := s.Feed(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleContra(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind          string  `json:"kind"`
		CashAccountID string  `json:"cash_account_id"`
		BankAccountID string  `json:"bank_account_id"`
		Amount        moneyIn `json:"amount"`
		PostedOn      string  `json:"posted_on"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cashID, err := uuid.Parse(body.CashAccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Cash account is not an id."))
		return
	}
	bankID, err := uuid.Parse(body.BankAccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Bank account is not an id."))
		return
	}
	amount, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", body.PostedOn)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	if err := s.PostContra(r.Context(), p, ContraInput{Kind: body.Kind, CashAccountID: cashID, BankAccountID: bankID, Amount: amount, PostedOn: day}); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"kind": body.Kind})
}

func (s *Service) handlePayment(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string  `json:"account_id"`
		Amount    moneyIn `json:"amount"`
		PostedOn  string  `json:"posted_on"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	amount, err := ParseAmount(body.Amount.Amount, body.Amount.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", body.PostedOn)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	if err := s.PostPayment(r.Context(), p, PaymentInput{AccountID: id, Amount: amount, PostedOn: day}); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"posted": true})
}

func (s *Service) handleVoucher(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string   `json:"account_id"`
		Expense   moneyIn  `json:"expense"`
		VAT       *moneyIn `json:"vat"`
		PostedOn  string   `json:"posted_on"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	expense, err := ParseAmount(body.Expense.Amount, body.Expense.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var vat int64
	if body.VAT != nil {
		vat, err = ParseAmount(body.VAT.Amount, body.VAT.Currency)
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
	}
	day, err := time.Parse("2006-01-02", body.PostedOn)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	out, err := s.PostVoucher(r.Context(), p, VoucherInput{AccountID: id, Expense: expense, VAT: vat, PostedOn: day})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleCheque(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Number    string `json:"number"`
		State     string `json:"state"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(body.AccountID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	number, err := strconv.ParseInt(body.Number, 10, 64)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Cheque number is required."))
		return
	}
	out, err := s.RegisterCheque(r.Context(), p, ChequeInput{AccountID: id, Number: number, State: body.State})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, "stopped")
}

func (s *Service) handleClear(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, "cleared")
}

func (s *Service) transition(w http.ResponseWriter, r *http.Request, next string) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Cheque is not an id."))
		return
	}
	var discard struct{}
	if err := httpx.DecodeJSON(r, &discard); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.TransitionCheque(r.Context(), p, id, version, next)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleGaps(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.URL.Query().Get("account"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Account is not an id."))
		return
	}
	out, err := s.ChequeGaps(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Name         string  `json:"name"`
		Limit        moneyIn `json:"limit"`
		AnnualRateBP int     `json:"annual_rate_bp"`
		Maturity     string  `json:"maturity"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	limit, err := ParseAmount(body.Limit.Amount, body.Limit.Currency)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	mature, err := time.Parse("2006-01-02", body.Maturity)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	out, err := s.OpenFacility(r.Context(), p, FacilityInput{Name: body.Name, Limit: limit, AnnualRateBP: body.AnnualRateBP, Maturity: mature})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleDraw(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Facility is not an id."))
		return
	}
	var body struct {
		Amount moneyIn `json:"amount"`
		AsOf   string  `json:"as_of"`
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
	out, err := s.Draw(r.Context(), p, id, version, amount)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleInterest(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Facility is not an id."))
		return
	}
	var body struct {
		AsOf string `json:"as_of"`
		Days int    `json:"days"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.PostInterest(r.Context(), p, id, body.Days)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handlePosition(w http.ResponseWriter, r *http.Request) {
	p, ok := caller(w, r)
	if !ok {
		return
	}
	raw := r.URL.Query().Get("date")
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD."))
		return
	}
	out, err := s.Position(r.Context(), p, day)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func accountQuery(r *http.Request) (uuid.UUID, time.Time, error) {
	id, err := uuid.Parse(r.URL.Query().Get("account"))
	if err != nil {
		return uuid.Nil, time.Time{}, apierr.New(apierr.ValidationError, "Account is not an id.")
	}
	day, err := time.Parse("2006-01-02", r.URL.Query().Get("as_of"))
	if err != nil {
		return uuid.Nil, time.Time{}, apierr.New(apierr.ValidationError, "Date must be YYYY-MM-DD.")
	}
	return id, day, nil
}
