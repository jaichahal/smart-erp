package approvals

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
)

// Submit opens a request. A client field named state is ignored (D6).
func (h Handler) Submit(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		DocID                string         `json:"doc_id"`
		DocType              string         `json:"doc_type"`
		DocNumber            string         `json:"doc_number"`
		Party                string         `json:"party"`
		Amount               string         `json:"amount"`
		Currency             string         `json:"currency"`
		Snapshot             map[string]any `json:"snapshot"`
		UsualAmount          string         `json:"usual_amount"`
		OriginalAmount       string         `json:"original_amount"`
		OriginalApproverIDs  []string       `json:"original_approver_ids"`
		RejectionChain       int            `json:"rejection_chain"`
		CorrectionsThisMonth int            `json:"corrections_this_month"`
		OriginalDocID        string         `json:"original_doc_id"`
		State                string         `json:"state"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.Svc.Submit(r.Context(), p, SubmitInput{
		DocID: body.DocID, DocType: body.DocType, DocNumber: body.DocNumber, Party: body.Party,
		Amount: body.Amount, Currency: body.Currency, Snapshot: body.Snapshot,
		UsualAmount: body.UsualAmount, OriginalAmount: body.OriginalAmount,
		OriginalApproverIDs: body.OriginalApproverIDs, RejectionChain: body.RejectionChain,
		CorrectionsThisMonth: body.CorrectionsThisMonth, OriginalDocID: body.OriginalDocID,
		ClientState: body.State,
	})
	writeDecision(w, r, out, err)
}

// PutActor stores one person in the approvals directory.
func (h Handler) PutActor(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Department string   `json:"department"`
		Roles      []string `json:"roles"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := h.Svc.UpsertActor(r.Context(), p, Actor{ID: body.ID, Name: body.Name, Department: body.Department, Roles: body.Roles}); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"id": body.ID})
}

// PutMatrixHTTP stores the matrix row for one document type.
func (h Handler) PutMatrixHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		DocType             string   `json:"doc_type"`
		ThresholdAmount     string   `json:"threshold_amount"`
		Currency            string   `json:"currency"`
		BelowRoles          []string `json:"below_roles"`
		FirstRoles          []string `json:"first_roles"`
		FinalRole           string   `json:"final_role"`
		AboveMode           string   `json:"above_mode"`
		VoteN               int      `json:"vote_n"`
		RequiresStepUpAbove bool     `json:"requires_step_up_above"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	err := h.Svc.PutMatrix(r.Context(), p, Matrix{
		DocType: body.DocType, ThresholdAmount: body.ThresholdAmount, Currency: body.Currency,
		BelowRoles: body.BelowRoles, FirstRoles: body.FirstRoles, FinalRole: body.FinalRole,
		AboveMode: body.AboveMode, VoteN: body.VoteN, RequiresStepUpAbove: body.RequiresStepUpAbove,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"doc_type": body.DocType})
}

// Assign adds a person to an approver slot. An accountant adding themselves is refused (D2).
func (h Handler) Assign(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		DocType string `json:"doc_type"`
		Slot    string `json:"slot"`
		UserID  string `json:"user_id"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := h.Svc.AssignApprover(r.Context(), p, body.DocType, body.Slot, body.UserID); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"user_id": body.UserID})
}

// PutFraud stores the fraud-hint thresholds (D12).
func (h Handler) PutFraud(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		VariancePercent        string `json:"variance_percent"`
		RoundAbove             string `json:"round_above"`
		RoundStep              string `json:"round_step"`
		RepeatedRejectionMin   int    `json:"repeated_rejection_min"`
		CorrectionsPerMonthMin int    `json:"corrections_per_month_min"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	err := h.Svc.PutFraudConfig(r.Context(), p, FraudSettings{
		VariancePercent: body.VariancePercent, RoundAbove: body.RoundAbove, RoundStep: body.RoundStep,
		RepeatedRejectionMin: body.RepeatedRejectionMin, CorrectionsPerMonthMin: body.CorrectionsPerMonthMin,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"saved": true})
}

// Delegate records a first-stage, time-boxed delegation (D11).
func (h Handler) Delegate(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ToUserID     string `json:"to_user_id"`
		Until        string `json:"until"`
		StateVersion *int   `json:"state_version"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	until, err := time.Parse(time.RFC3339, body.Until)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, t(r.Context(), "delegate.until_past")).WithDetails(map[string]any{"field": "until"}))
		return
	}
	version, err := versionFrom(r, body.StateVersion)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.Svc.Delegate(r.Context(), p, chi.URLParam(r, "id"), body.ToUserID, until, version)
	writeDecision(w, r, out, err)
}

// Consume spends a posting token against the content being registered (D9, D10).
func (h Handler) Consume(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Token   string         `json:"token"`
		Content map[string]any `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if err := h.Svc.ConsumePostingToken(r.Context(), p, body.Token, body.Content); err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]bool{"posted": true})
}
