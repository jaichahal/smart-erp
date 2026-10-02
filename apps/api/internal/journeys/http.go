package journeys

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers the journey routes on the /api/v1 router.
// The caller must already have placed an rls.Principal on the context.
// Ports for identity, authorization, approvals, and the ledger come from
// WithProviders; missing ports fall closed.
func Mount(r chi.Router, deps httpx.Deps) {
	h := &handler{deps: deps}
	r.Route("/journeys", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/instances/{id}", h.get)
		r.With(idempotency.Middleware(deps.Pool)).Post("/instances/{id}/step", h.step)
		r.With(idempotency.Middleware(deps.Pool)).Post("/{slug}/instances", h.create)
	})
}

type handler struct {
	deps httpx.Deps
}

func (h *handler) engine() *Engine {
	return NewEngine(h.deps.Pool, h.deps.River)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	p, ok := principal(w, r, lang)
	if !ok {
		return
	}
	list, err := h.engine().List(r.Context(), p, r.URL.Query().Get("persona"), lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, list)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	p, ok := principal(w, r, lang)
	if !ok {
		return
	}
	created, err := h.engine().Start(r.Context(), p, chi.URLParam(r, "slug"), lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, created)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	p, ok := principal(w, r, lang)
	if !ok {
		return
	}
	id, ok := instanceID(w, r, lang)
	if !ok {
		return
	}
	view, err := h.engine().Get(r.Context(), p, id, lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, view)
}

func (h *handler) step(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	p, ok := principal(w, r, lang)
	if !ok {
		return
	}
	id, ok := instanceID(w, r, lang)
	if !ok {
		return
	}
	expected, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var body oapi.JourneyStepSubmission
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if body.Input == nil {
		body.Input = map[string]any{}
	}
	result, err := h.engine().Submit(r.Context(), p, id, expected, body.StepId, body.Input, lang)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, result)
}

func principal(w http.ResponseWriter, r *http.Request, lang string) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, text(lang, "journey.auth_required")))
		return rls.Principal{}, false
	}
	return p, true
}

func instanceID(w http.ResponseWriter, r *http.Request, lang string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.NotFound, text(lang, "journey.not_found")))
		return uuid.UUID{}, false
	}
	return id, true
}

func requestLang(r *http.Request) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "ar") {
		return "ar"
	}
	return "en"
}
