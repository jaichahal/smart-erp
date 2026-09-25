package ledger

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers chart and journal routes. The caller wires one line.
func Mount(r chi.Router, deps httpx.Deps) {
	s := New(deps.Pool)
	r.Get("/tax-codes", s.handleTaxCodes)
	r.Get("/dimensions", s.handleDimensions)
	r.Post("/journals", s.handleManualJournal)
}

func (s *Service) handleTaxCodes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	rows, err := s.TaxCodes(r.Context(), p)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, rows)
}

func (s *Service) handleDimensions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	rows, err := s.Dimensions(r.Context(), p)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, rows)
}

func (s *Service) handleManualJournal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		PostingDate   time.Time    `json:"posting_date"`
		Description   string       `json:"description"`
		Reason        string       `json:"reason"`
		AttachmentKey string       `json:"attachment_key"`
		Lines         []ManualLine `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := s.SubmitManualJournal(r.Context(), p, ManualJournal{
		PostingDate: body.PostingDate, Description: body.Description,
		Reason: body.Reason, AttachmentKey: body.AttachmentKey, Lines: body.Lines,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]uuid.UUID{"id": id})
}

func principal(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication is required"))
		return rls.Principal{}, false
	}
	return p, true
}
