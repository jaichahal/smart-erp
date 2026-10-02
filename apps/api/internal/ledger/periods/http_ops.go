package periods

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// errRegistrationFailed is returned from the registration's business write so
// the number is voided and the write rolls back (C1).
var errRegistrationFailed = errors.New("registration failed")

func (s *Service) handleCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	var body struct {
		LegalName string `json:"legal_name"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := s.CreateCompany(r.Context(), p, body.LegalName, langOf(r)); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"id": p.CompanyID.String(), "legal_name": body.LegalName})
}

func (s *Service) handleFiscalYear(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	var body struct {
		Year  int    `json:"year"`
		Start string `json:"start"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	start, err := time.Parse("2006-01-02", body.Start)
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "year_invalid"))
		return
	}
	if err := s.OpenFiscalYear(r.Context(), p, body.Year, start, langOf(r)); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"year": body.Year, "start": body.Start})
}

func (s *Service) handlePeriodOn(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	day, err := time.Parse("2006-01-02", r.URL.Query().Get("on"))
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "year_invalid"))
		return
	}
	var period Period
	err = rls.Tx(r.Context(), s.pool, p, func(tx pgx.Tx) error {
		found, err := periodOn(r.Context(), tx, p.CompanyID, dateOnly(day))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(langOf(r), apierr.NotFound, "period_not_found")
		}
		if err != nil {
			return err
		}
		period = found.Period
		return nil
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, period)
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	var body struct {
		DocType    string `json:"doc_type"`
		DocID      string `json:"doc_id"`
		FiscalYear int    `json:"fiscal_year"`
		Commit     *bool  `json:"commit"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	commit := body.Commit == nil || *body.Commit
	alloc, err := s.Register(r.Context(), p, Registration{
		DocType: body.DocType, DocID: body.DocID, FiscalYear: body.FiscalYear,
		Apply: func(ctx context.Context, tx pgx.Tx, number int64) error {
			if _, execErr := tx.Exec(ctx, `INSERT INTO erp.registered_documents (company_id, doc_type, doc_id, number)
				VALUES ($1, $2, $3, $4)`, p.CompanyID, body.DocType, body.DocID, number); execErr != nil {
				return execErr
			}
			if !commit {
				return errRegistrationFailed
			}
			return nil
		},
	}, langOf(r))
	wire := map[string]any{
		"number": alloc.Number, "doc_type": alloc.DocType, "fiscal_year": alloc.FiscalYear, "voided": alloc.Voided,
	}
	if alloc.Voided {
		refused := fail(langOf(r), apierr.ValidationError, "registration_failed")
		var ae *apierr.Error
		if errors.As(refused, &ae) {
			refused = ae.WithDetails(wire)
		}
		apierr.Write(w, r, refused)
		return
	}
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, wire)
}

func (s *Service) handlePosting(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	var body struct {
		PostingDate string `json:"posting_date"`
		DocType     string `json:"doc_type"`
		DocID       string `json:"doc_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", body.PostingDate)
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "year_invalid"))
		return
	}
	if err := s.CheckPosting(r.Context(), p, Posting{PostingDate: day, DocType: body.DocType, DocID: body.DocID}, langOf(r)); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"doc_id": body.DocID, "posting_date": body.PostingDate})
}

func (s *Service) handleExceptions(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOnly(w, r)
	if !ok {
		return
	}
	month, err := time.Parse("2006-01", r.URL.Query().Get("month"))
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "year_invalid"))
		return
	}
	rows, err := s.ListExceptions(r.Context(), p, month, langOf(r))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]string{"doc_id": row.DocID, "kind": row.Kind, "reason": row.Reason})
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func principalOnly(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.AuthRequired, "auth_required"))
		return rls.Principal{}, false
	}
	return p, true
}
