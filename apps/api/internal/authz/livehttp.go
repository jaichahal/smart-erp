package authz

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func (h handler) listCustomers(w http.ResponseWriter, r *http.Request) {
	actor, limit, cursor, ok := h.beginList(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListCustomers(r.Context(), actor, cursor, limit)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writePage(w, r, page.Rows, page.Total, page.NextCursor)
}

func (h handler) previewDocument(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var payload any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&payload); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	redacted, err := h.svc.Redact(r.Context(), actor, payload)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, redacted)
}

func (h handler) experience(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	exp, err := h.svc.ResolveExperience(r.Context(), actor)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, experienceView{
		Personas:            orEmpty(exp.Personas),
		PrivilegedTabs:      orEmpty(exp.PrivilegedTabs),
		PrivilegedEndpoints: orEmpty(exp.PrivilegedEndpoints),
		LeastPrivileged:     exp.LeastPrivileged(),
	})
}

type experienceView struct {
	Personas            []string `json:"personas"`
	PrivilegedTabs      []string `json:"privileged_tabs"`
	PrivilegedEndpoints []string `json:"privileged_endpoints"`
	LeastPrivileged     bool     `json:"least_privileged"`
}

func (h handler) createUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var body NewUser
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	if err := h.svc.CreateUser(r.Context(), actor, body); err != nil {
		h.writeErr(w, r, err)
		return
	}
	user, err := h.svc.ResolveUser(r.Context(), actor, body.ID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, userView(user))
}

func (h handler) getUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	user, err := h.svc.ResolveUser(r.Context(), actor, chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, userView(user))
}

type rolesWrite struct {
	Roles              []string `json:"roles"`
	OverrideApprovalID string   `json:"override_approval_id"`
}

func (h handler) assignRoles(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var body rolesWrite
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	userID := chi.URLParam(r, "id")
	if err := h.svc.AssignRoles(r.Context(), actor, userID, body.Roles, body.OverrideApprovalID); err != nil {
		h.writeErr(w, r, err)
		return
	}
	user, err := h.svc.ResolveUser(r.Context(), actor, userID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, userView(user))
}

func (h handler) disableUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	userID := chi.URLParam(r, "id")
	if err := h.svc.DisableUser(r.Context(), actor, userID); err != nil {
		h.writeErr(w, r, err)
		return
	}
	user, err := h.svc.ResolveUser(r.Context(), actor, userID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, userView(user))
}

type reviewWrite struct {
	AsOf time.Time `json:"as_of"`
}

func (h handler) runAccessReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var body reviewWrite
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	if body.AsOf.IsZero() {
		body.AsOf = time.Now().UTC()
	}
	review, err := h.svc.RunScheduledAccessReview(r.Context(), actor, body.AsOf)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]reviewItemView, 0, len(review.Items))
	for _, item := range review.Items {
		items = append(items, reviewItemView{
			UserID: item.UserID, Roles: orEmpty(item.Roles), LastLogin: item.LastLogin, Confirmed: item.Confirmed,
		})
	}
	httpx.JSON(w, r, http.StatusOK, reviewView{ID: review.ID, Period: review.Period, Items: items})
}

type confirmWrite struct {
	UserID string `json:"user_id"`
}

func (h handler) confirmAccessReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var body confirmWrite
	if err := httpx.DecodeJSON(r, &body); err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.body"))
		return
	}
	if err := h.svc.ConfirmAccessReview(r.Context(), actor, chi.URLParam(r, "id"), body.UserID); err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"user_id": body.UserID})
}

func (h handler) listExceptions(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	asOf := time.Now().UTC()
	if raw := r.URL.Query().Get("as_of"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			h.writeErr(w, r, h.svc.fail(r.Context(), apierr.ValidationError, "validation.field"))
			return
		}
		asOf = parsed
	}
	rows, err := h.svc.ListExceptions(r.Context(), actor, asOf)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := make([]exceptionView, 0, len(rows))
	for _, row := range rows {
		out = append(out, exceptionView{Kind: row.Kind, SubjectID: row.SubjectID})
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h handler) actor(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	actor, err := rls.FromContext(r.Context())
	if err != nil {
		h.writeErr(w, r, h.svc.fail(r.Context(), apierr.AuthRequired, "auth.required"))
		return rls.Principal{}, false
	}
	return actor, true
}

type directoryView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Disabled bool     `json:"disabled"`
	Roles    []string `json:"roles"`
}

func userView(user DirectoryUser) directoryView {
	return directoryView{ID: user.ID, Name: user.Name, Disabled: user.Disabled, Roles: orEmpty(user.Roles)}
}

type reviewView struct {
	ID     string           `json:"id"`
	Period string           `json:"period"`
	Items  []reviewItemView `json:"items"`
}

type reviewItemView struct {
	UserID    string     `json:"user_id"`
	Roles     []string   `json:"roles"`
	LastLogin *time.Time `json:"last_login"`
	Confirmed bool       `json:"confirmed"`
}

type exceptionView struct {
	Kind      string `json:"kind"`
	SubjectID string `json:"subject_id"`
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
