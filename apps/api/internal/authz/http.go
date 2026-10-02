package authz

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type handler struct {
	svc *Service
}

// Mount registers authorisation routes: matrices, customers, experience, users,
// document redaction, and the access-review job.
func Mount(r chi.Router, deps httpx.Deps) {
	h := handler{svc: New(deps)}
	r.Route("/sod-matrix", func(r chi.Router) {
		r.Use(language, h.authenticate)
		r.Get("/", h.listSod)
		r.With(idempotency.Middleware(deps.Pool)).Post("/", h.createSod)
	})
	r.Route("/approval-matrix", func(r chi.Router) {
		r.Use(language, h.authenticate)
		r.Get("/", h.listMatrix)
		r.With(idempotency.Middleware(deps.Pool)).Post("/", h.createMatrix)
	})
	r.Group(func(r chi.Router) {
		r.Use(language, h.authenticate)
		r.Get("/customers", h.listCustomers)
		r.Get("/experience", h.experience)
		r.Get("/users/{id}", h.getUser)
		r.Get("/exceptions", h.listExceptions)
		write := r.With(idempotency.Middleware(deps.Pool))
		write.Post("/documents/preview", h.previewDocument)
		write.Post("/users", h.createUser)
		write.Post("/users/{id}/roles", h.assignRoles)
		write.Post("/users/{id}/disable", h.disableUser)
		write.Post("/access-reviews", h.runAccessReview)
		write.Post("/access-reviews/{id}/confirm", h.confirmAccessReview)
	})
}

func language(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withLang(r.Context(), negotiate(r.Header.Get("Accept-Language")))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h handler) listSod(w http.ResponseWriter, r *http.Request) {
	actor, limit, cursor, ok := h.beginList(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListSodRules(r.Context(), actor, cursor, limit)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writePage(w, r, page.Rules, page.Total, page.NextCursor)
}

func (h handler) createSod(w http.ResponseWriter, r *http.Request) {
	actor, expected, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	var body oapi.SodRuleWrite
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	rule, err := h.svc.SaveSodRule(r.Context(), actor, expected, body)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, rule)
}

func (h handler) listMatrix(w http.ResponseWriter, r *http.Request) {
	actor, limit, cursor, ok := h.beginList(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListApprovalMatrix(r.Context(), actor, cursor, limit)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writePage(w, r, page.Rules, page.Total, page.NextCursor)
}

func (h handler) createMatrix(w http.ResponseWriter, r *http.Request) {
	actor, expected, ok := h.beginWrite(w, r)
	if !ok {
		return
	}
	var body oapi.ApprovalMatrixRuleWrite
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	rule, err := h.svc.SaveApprovalMatrixRule(r.Context(), actor, expected, body)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, rule)
}

func (h handler) beginList(w http.ResponseWriter, r *http.Request) (rls.Principal, int, string, bool) {
	actor, err := rls.FromContext(r.Context())
	if err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.AuthRequired, "auth.required"))
		return rls.Principal{}, 0, "", false
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.field"))
			return rls.Principal{}, 0, "", false
		}
		limit = n
	}
	return actor, limit, r.URL.Query().Get("cursor"), true
}

func (h handler) beginWrite(w http.ResponseWriter, r *http.Request) (rls.Principal, int, bool) {
	actor, err := rls.FromContext(r.Context())
	if err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.AuthRequired, "auth.required"))
		return rls.Principal{}, 0, false
	}
	expected, _, err := ifmatch.Parse(r, true)
	if err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.if_match"))
		return rls.Principal{}, 0, false
	}
	return actor, int(expected), true
}

func (h handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		err = apierr.Wrap(apierr.Internal, h.svc.messages.text(langOf(r.Context()), "internal"), err)
	}
	apierr.Write(w, r, err)
}

type pageEnvelope struct {
	Data any      `json:"data"`
	Meta pageMeta `json:"meta"`
}

type pageMeta struct {
	AsOf       time.Time `json:"as_of"`
	RequestID  string    `json:"request_id"`
	NextCursor *string   `json:"next_cursor"`
	Total      int       `json:"total"`
}

func writePage(w http.ResponseWriter, r *http.Request, data any, total int, next *string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pageEnvelope{
		Data: data,
		Meta: pageMeta{
			AsOf:       time.Now().UTC(),
			RequestID:  apierr.RequestID(r.Context()),
			NextCursor: next,
			Total:      total,
		},
	})
}
