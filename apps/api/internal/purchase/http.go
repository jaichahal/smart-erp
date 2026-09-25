package purchase

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Handler serves purchase routes.
type Handler struct {
	Svc *Service
	Log *slog.Logger
}

// Mount registers purchase routes on the API router.
func Mount(r chi.Router, deps httpx.Deps) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	appr := approvals.New(deps.Pool, deps.River, approvals.NewSQLDirectory(deps.Pool), approvals.StubStepUp{}, nil)
	Handler{Svc: New(deps.Pool, appr), Log: log}.Routes(r)
}

// Routes registers the purchase API.
func (h Handler) Routes(r chi.Router) {
	r.Route("/purchase", func(pr chi.Router) {
		pr.Use(h.logRequest)
		pr.Post("/vendors", h.refuseVendor)
		pr.Get("/dashboard", h.dashboard)
		pr.Get("/payables", h.payables)
		pr.Get("/requests/{id}", h.review)
		pr.Get("/documents/{id}", h.document)
	})
}

func (h Handler) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.logger().InfoContext(r.Context(), "purchase.request",
			"request_id", apierr.RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
		)
		next.ServeHTTP(w, r)
	})
}

func (h Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h Handler) refuseVendor(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	apierr.Write(w, r, h.Svc.RefuseDirectVendor(r.Context(), p))
}

func (h Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	var sku *uuid.UUID
	if raw := r.URL.Query().Get("sku"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "sku"}))
			return
		}
		sku = &id
	}
	out, err := h.Svc.Dashboard(r.Context(), p, sku)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h Handler) review(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, msg("validation")))
		return
	}
	out, err := h.Svc.Review(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h Handler) payables(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.Svc.ListPayables(r.Context(), p)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h Handler) document(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, msg("validation")))
		return
	}
	out, err := h.Svc.GetDocument(r.Context(), p, id)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func principal(r *http.Request) (rls.Principal, error) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		return rls.Principal{}, apierr.New(apierr.AuthRequired, "Authentication required")
	}
	return p, nil
}
