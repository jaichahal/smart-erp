package clocks

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers the holiday calendar on the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps) {
	s := New(deps.Pool, deps.River)
	r.Get("/holiday-calendar", s.handleGet)
	r.With(idempotency.Middleware(deps.Pool)).Post("/holiday-calendar", s.handleSave)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, fail(langOf(r), apierr.AuthRequired, "auth_required"))
		return
	}
	cal, err := s.GetCalendar(r.Context(), p, langOf(r))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, wire(cal))
}

func (s *Service) handleSave(w http.ResponseWriter, r *http.Request) {
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
	var body Calendar
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	cal, err := s.SaveCalendar(r.Context(), p, version, body, langOf(r))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, wire(cal))
}

func wire(cal Calendar) Calendar {
	cal.loc = nil
	cal.weekend = nil
	cal.holidays = nil
	return cal
}

func langOf(r *http.Request) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "ar") {
		return "ar"
	}
	return "en"
}
