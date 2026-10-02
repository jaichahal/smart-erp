package periods

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers period close routes on the /api/v1 router.
// Wiring in cmd/api is one line; that file is outside this task.
func Mount(r chi.Router, deps httpx.Deps, approvals ApprovalGate) {
	s := New(deps.Pool, deps.River)
	s.Approvals = approvals
	idem := idempotency.Middleware(deps.Pool)
	// A hard-closed period is refused before the idempotency middleware, which
	// otherwise answers AUTH_REQUIRED when the caller has no principal. C3
	// requires PERIOD_CLOSED for that period.
	r.With(s.refuseHardClosed, idem).Post("/periods/{id}/soft-close", s.handleSoftClose)
	r.With(s.refuseHardClosed, idem).Post("/periods/{id}/hard-close", s.handleHardClose)
	r.With(idem).Post("/periods/{year}/audit-adjustment/open", s.handleAuditOpen)
	r.With(idem).Post("/companies", s.handleCompany)
	r.With(idem).Post("/fiscal-years", s.handleFiscalYear)
	r.With(idem).Post("/periods/registrations", s.handleRegister)
	r.With(idem).Post("/periods/postings", s.handlePosting)
	r.Get("/periods", s.handlePeriodOn)
	r.Get("/exceptions", s.handleExceptions)
	r.With(idem).Post("/periods/{id}/soft-close", s.handleSoftClose)
	r.With(idem).Post("/periods/{id}/hard-close", s.handleHardClose)
	r.With(idem).Post("/periods/{year}/audit-adjustment/open", s.handleAuditOpen)
}

func (s *Service) handleSoftClose(w http.ResponseWriter, r *http.Request) {
	p, id, version, lang, ok := periodRequest(w, r)
	if !ok {
		return
	}
	out, err := s.SoftClose(r.Context(), p, id, version, lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleHardClose(w http.ResponseWriter, r *http.Request) {
	p, id, version, lang, ok := periodRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.HardClose(r.Context(), p, id, version, body.ApprovalID, lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (s *Service) handleAuditOpen(w http.ResponseWriter, r *http.Request) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.AuthRequired, "auth_required"))
		return
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "year_invalid"))
		return
	}
	var body struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := s.OpenAuditAdjustment(r.Context(), p, year, version, body.ApprovalID, langOf(r))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func periodRequest(w http.ResponseWriter, r *http.Request) (rls.Principal, uuid.UUID, int64, string, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.AuthRequired, "auth_required"))
		return rls.Principal{}, uuid.Nil, 0, "", false
	}
	version, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return rls.Principal{}, uuid.Nil, 0, "", false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.ValidationError, "period_not_found"))
		return rls.Principal{}, uuid.Nil, 0, "", false
	}
	return p, id, version, langOf(r), true
}

func langOf(r *http.Request) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "ar") {
		return "ar"
	}
	return "en"
}
