package approvals

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Handler serves the approval HTTP routes from the OpenAPI contract.
type Handler struct {
	Svc  *Service
	Pool *pgxpool.Pool
}

// RequestPrincipal supplies a caller when the identity authenticator has not
// already put one on the context. Production wiring should set this from the
// identity middleware. It stays nil until a composer or a test assigns it.
var RequestPrincipal func(*http.Request) (rls.Principal, bool)

// clockOverride replaces the wall clock for the mounted service. Nil uses RealClock.
var clockOverride Clock

// UseClock selects the clock Mount will pass to the service. Call it before Mount.
func UseClock(c Clock) { clockOverride = c }

// Mount registers /approvals routes. The caller mounts this under /api/v1.
func Mount(r chi.Router, deps httpx.Deps) {
	clock := Clock(RealClock{})
	if clockOverride != nil {
		clock = clockOverride
	}
	svc := New(deps.Pool, deps.River, NewSQLDirectory(deps.Pool), StubStepUp{}, clock)
	Handler{Svc: svc, Pool: deps.Pool}.Routes(r)
}

// Routes registers inbox, get, approve, reject, and the configuration commands
// the engine needs in order to run a request through the published lifecycle.
func (h Handler) Routes(r chi.Router) {
	r.Route("/approvals", func(ar chi.Router) {
		ar.Use(languageMiddleware)
		ar.Use(requestPrincipal)
		if h.Pool != nil {
			ar.Use(idempotency.Middleware(h.Pool))
		}
		ar.Post("/requests", h.Submit)
		ar.Post("/actors", h.PutActor)
		ar.Post("/matrix", h.PutMatrixHTTP)
		ar.Post("/assignments", h.Assign)
		ar.Post("/fraud-config", h.PutFraud)
		ar.Post("/posting-tokens/consume", h.Consume)
		ar.Get("/inbox", h.Inbox)
		ar.Get("/{id}", h.Get)
		ar.Post("/{id}/approve", h.Approve)
		ar.Post("/{id}/reject", h.Reject)
		ar.Post("/{id}/delegate", h.Delegate)
	})
}

func requestPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := rls.FromContext(r.Context()); err != nil && RequestPrincipal != nil {
			if p, ok := RequestPrincipal(r); ok {
				r = r.WithContext(rls.WithPrincipal(r.Context(), p))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func languageMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithLang(r.Context(), r.Header.Get("Accept-Language"))))
	})
}

// Inbox lists cards for the authenticated approver.
func (h Handler) Inbox(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, t(r.Context(), "inbox.state")))
			return
		}
		limit = n
	}
	cards, err := h.Svc.Inbox(r.Context(), p, r.URL.Query().Get("state"), limit)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, cards)
}

// Get returns one request, including the snapshot and its hash.
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	detail, err := h.Svc.Get(r.Context(), p, chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, detail)
}

// Approve records an approval. A client field named state is ignored (D6).
func (h Handler) Approve(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Comment      *string `json:"comment"`
		StateVersion *int    `json:"state_version"`
		StepUpToken  *string `json:"step_up_token"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	version, err := versionFrom(r, body.StateVersion)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	in := DecisionInput{RequestID: chi.URLParam(r, "id"), StateVersion: version}
	if body.Comment != nil {
		in.Comment = *body.Comment
	}
	if body.StepUpToken != nil {
		in.StepUpToken = *body.StepUpToken
	}
	out, err := h.Svc.Approve(r.Context(), p, in)
	writeDecision(w, r, out, err)
}

// Reject records a rejection. Empty and whitespace reasons are refused (D5).
func (h Handler) Reject(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason       *string `json:"reason"`
		StateVersion *int    `json:"state_version"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	version, err := versionFrom(r, body.StateVersion)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	reason := ""
	if body.Reason != nil {
		reason = *body.Reason
	}
	// API layer. The workflow repeats the check and audits the refusal.
	if err := NonEmptyReason(r.Context(), reason); err != nil {
		_, svcErr := h.Svc.Reject(r.Context(), p, DecisionInput{RequestID: chi.URLParam(r, "id"), StateVersion: version, Reason: reason})
		if svcErr != nil {
			apierr.Write(w, r, svcErr)
			return
		}
		apierr.Write(w, r, err)
		return
	}
	out, err := h.Svc.Reject(r.Context(), p, DecisionInput{RequestID: chi.URLParam(r, "id"), StateVersion: version, Reason: reason})
	writeDecision(w, r, out, err)
}

func writeDecision(w http.ResponseWriter, r *http.Request, out *Outcome, err error) {
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	meta := httpx.Meta{Notice: out.Notice}
	if out.PostingToken != "" {
		meta.Extra = map[string]any{
			"posting_token":   out.PostingToken,
			"posting_expires": out.PostingExpires.UTC().Format(time.RFC3339Nano),
		}
	}
	httpx.JSONWithMeta(w, r, http.StatusOK, out.Decision, meta)
}

func principal(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, t(r.Context(), "auth.required")))
		return rls.Principal{}, false
	}
	return p, true
}

func decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return apierr.Wrap(apierr.ValidationError, t(r.Context(), "validation.json"), err)
	}
	return nil
}

func versionFrom(r *http.Request, body *int) (int, error) {
	if body == nil {
		return 0, apierr.New(apierr.ValidationError, t(r.Context(), "version.missing")).WithDetails(map[string]any{"field": "state_version"})
	}
	header, present, err := ifmatch.Parse(r, false)
	if err != nil {
		return 0, err
	}
	if present && header != int64(*body) {
		return 0, ifmatch.Check(header, int64(*body), map[string]any{"state_version": *body})
	}
	return *body, nil
}
