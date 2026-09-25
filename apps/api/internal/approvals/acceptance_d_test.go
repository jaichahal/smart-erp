package approvals_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

type world struct {
	t       *testing.T
	db      *testdb.DB
	svc     *approvals.Service
	clock   *approvals.FakeClock
	company uuid.UUID
	ctx     context.Context
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db := testdb.New(t)
	client, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	clock := approvals.NewFakeClock(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	w := &world{t: t, db: db, clock: clock, company: uuid.New(), ctx: context.Background(),
		svc: approvals.New(db.App, client, approvals.NewSQLDirectory(db.App), approvals.StubStepUp{}, clock)}
	w.actor("initiator", "Initiator", "Sales", "agent")
	w.actor("approver-a", "Approver A", "Management", "approver")
	w.actor("approver-b", "Approver B", "Management", "approver")
	w.actor("final", "Final Gate", "Stakeholder", "stakeholder")
	w.actor("accountant", "Accountant", "Accounts", "accountant")
	w.actor("accounts-peer", "Accounts Peer", "Accounts", "approver")
	w.matrix(approvals.Matrix{
		DocType: "sales_invoice", ThresholdAmount: "1000.00", Currency: "AED",
		BelowRoles: []string{"approver"}, FirstRoles: []string{"approver"}, FinalRole: "stakeholder",
		RequiresStepUpAbove: false,
	})
	return w
}

func (w *world) actor(id, name, dept string, roles ...string) {
	w.t.Helper()
	if err := w.svc.UpsertActor(w.ctx, w.as(id), approvals.Actor{ID: id, Name: name, Department: dept, Roles: roles}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) matrix(m approvals.Matrix) {
	w.t.Helper()
	if err := w.svc.PutMatrix(w.ctx, w.as("initiator"), m); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) as(id string) rls.Principal {
	return rls.Principal{UserID: id, CompanyID: w.company, Roles: []string{"approver"}}
}

func (w *world) submit(amount, docType string) *approvals.Outcome {
	w.t.Helper()
	return w.submitAs("initiator", amount, docType, map[string]any{"line": "1"})
}

func (w *world) submitAs(user, amount, docType string, snap map[string]any) *approvals.Outcome {
	w.t.Helper()
	out, err := w.svc.Submit(w.ctx, w.as(user), approvals.SubmitInput{
		DocID: "doc-" + amount, DocType: docType, DocNumber: "INV-1", Party: "Al Noor",
		Amount: amount, Currency: "AED", Snapshot: snap, ClientState: "approved",
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) code(err error) apierr.Code {
	w.t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		w.t.Fatalf("want api error, got %v", err)
	}
	return ae.Code
}

func (w *world) audits(requestID, eventType string) int {
	w.t.Helper()
	var n int
	err := w.db.App.QueryRow(w.ctx, `SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type=$2`, requestID, eventType).Scan(&n)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) refusal(requestID, reason string) int {
	w.t.Helper()
	var n int
	err := w.db.App.QueryRow(w.ctx, `SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason=$2`, requestID, reason).Scan(&n)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) versions(requestID string) int {
	w.t.Helper()
	var n int
	err := rls.Tx(w.ctx, w.db.App, w.as("initiator"), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM erp.approval_requests WHERE request_id=$1`, requestID).Scan(&n)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) router() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			ctx := rls.WithPrincipal(req.Context(), w.as(req.Header.Get("X-User")))
			next.ServeHTTP(rw, req.WithContext(ctx))
		})
	})
	approvals.Handler{Svc: w.svc, Pool: w.db.App}.Routes(r)
	return r
}

func (w *world) post(user, path, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	w.router().ServeHTTP(rec, req)
	return rec
}

// D1: the submitter cannot approve their own document at any stage.
func TestD1_SubmitterCannotApprove(t *testing.T) {
	w := newWorld(t)
	below := w.submit("100.00", "sales_invoice")
	_, err := w.svc.Approve(w.ctx, w.as("initiator"), approvals.DecisionInput{RequestID: below.Decision.RequestId, StateVersion: 1})
	if w.code(err) != apierr.SoDViolation {
		t.Fatalf("below: %v", err)
	}
	if w.refusal(below.Decision.RequestId, "sod.self_approval") != 1 {
		t.Fatal("self-approval was not audited")
	}

	w.actor("boss", "Boss", "Sales", "approver")
	above := w.submit("5000.00", "sales_invoice")
	if _, err := w.svc.Approve(w.ctx, w.as("initiator"), approvals.DecisionInput{RequestID: above.Decision.RequestId, StateVersion: 1}); w.code(err) != apierr.SoDViolation {
		t.Fatalf("first stage: %v", err)
	}
	if _, err := w.svc.Approve(w.ctx, w.as("boss"), approvals.DecisionInput{RequestID: above.Decision.RequestId, StateVersion: 1}); err != nil {
		t.Fatal(err)
	}
	w.actor("initiator", "Initiator", "Sales", "agent", "stakeholder")
	_, err = w.svc.Approve(w.ctx, w.as("initiator"), approvals.DecisionInput{RequestID: above.Decision.RequestId, StateVersion: 2})
	if w.code(err) != apierr.SoDViolation {
		t.Fatalf("final stage: %v", err)
	}
}

// D2: an accountant cannot add themselves to an approver list.
func TestD2_AccountantCannotAssignSelf(t *testing.T) {
	w := newWorld(t)
	err := w.svc.AssignApprover(w.ctx, w.as("accountant"), "sales_invoice", "below", "accountant")
	if w.code(err) != apierr.SoDViolation {
		t.Fatal(err)
	}
	var n int
	if err := rls.Tx(w.ctx, w.db.App, w.as("accountant"), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM erp.approval_assignments WHERE user_id='accountant'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("assignment stored: %d", n)
	}
	var audited int
	if err := w.db.App.QueryRow(w.ctx, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND event_type='approval.refused' AND reason='accountant_self_assign'`, w.company).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit %d %v", audited, err)
	}
	if err := w.svc.AssignApprover(w.ctx, w.as("accountant"), "sales_invoice", "below", "approver-a"); err != nil {
		t.Fatal(err)
	}
}

// D3: below threshold one approver registers; above, first approver then final gate.
func TestD3_ThresholdStages(t *testing.T) {
	w := newWorld(t)
	low := w.submit("100.00", "sales_invoice")
	out, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: low.Decision.RequestId, StateVersion: 1})
	if err != nil || out.Decision.State != "approved" || out.PostingToken == "" {
		t.Fatalf("below: %+v %v", out, err)
	}
	high := w.submit("5000.00", "sales_invoice")
	mid, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: high.Decision.RequestId, StateVersion: 1})
	if err != nil || mid.Decision.State != "pending" || mid.PostingToken != "" {
		t.Fatalf("first: %+v %v", mid, err)
	}
	final, err := w.svc.Approve(w.ctx, w.as("final"), approvals.DecisionInput{RequestID: high.Decision.RequestId, StateVersion: 2})
	if err != nil || final.Decision.State != "approved" || final.PostingToken == "" {
		t.Fatalf("final: %+v %v", final, err)
	}
}

// D4: an initiator in Accounts cannot be first-approved by anyone in Accounts.
func TestD4_AccountsIndependence(t *testing.T) {
	w := newWorld(t)
	w.actor("acct-init", "Accounts Initiator", "Accounts", "agent")
	out := w.submitAs("acct-init", "5000.00", "sales_invoice", map[string]any{"line": "1"})
	_, err := w.svc.Approve(w.ctx, w.as("accounts-peer"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
	if w.code(err) != apierr.SoDViolation {
		t.Fatal(err)
	}
	if w.refusal(out.Decision.RequestId, "sod.accounts_independence") != 1 {
		t.Fatal("independence refusal was not audited")
	}
	if w.versions(out.Decision.RequestId) != 1 {
		t.Fatal("refused approval was stored")
	}
	ok, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
	if err != nil || ok.Decision.State != "pending" {
		t.Fatalf("independent approver: %+v %v", ok, err)
	}
}

// D5: empty or whitespace reasons are refused at the API, UI, and workflow layers.
func TestD5_EmptyReason(t *testing.T) {
	w := newWorld(t)
	for _, reason := range []string{"", "   ", "\n\t"} {
		if err := approvals.NonEmptyReason(w.ctx, reason); w.code(err) != apierr.ValidationError {
			t.Fatalf("UI layer %q: %v", reason, err)
		}
	}
	ar := approvals.WithLang(w.ctx, "ar")
	if err := approvals.NonEmptyReason(ar, " "); err == nil || err.Error() == approvals.NonEmptyReason(w.ctx, " ").Error() {
		t.Fatal("Arabic message should differ from English")
	}
	out := w.submit("100.00", "sales_invoice")
	_, err := w.svc.Reject(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1, Reason: "   "})
	if w.code(err) != apierr.ValidationError {
		t.Fatalf("workflow: %v", err)
	}
	if w.refusal(out.Decision.RequestId, "empty_reason") != 1 || w.versions(out.Decision.RequestId) != 1 {
		t.Fatal("workflow refusal was not committed apart from the request")
	}
	rec := w.post("approver-a", "/approvals/"+out.Decision.RequestId+"/reject", `{"reason":"  ","state_version":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("API status %d body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct{ Code string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code != "VALIDATION_ERROR" {
		t.Fatalf("API body %s", rec.Body.String())
	}
}

// D6: a client payload containing state Approved does not change the evaluated state.
func TestD6_ClientStateIgnored(t *testing.T) {
	w := newWorld(t)
	out := w.submit("5000.00", "sales_invoice")
	client := map[string]any{"state": "approved", "amount": "0", "content_hash": "ab"}
	restored, err := w.svc.Restore(w.ctx, w.as("approver-a"), out.Decision.RequestId, client)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != "pending" || restored.Amount != "5000.00" {
		t.Fatalf("restored from client: %+v", restored)
	}
	if _, ok := client["state"]; ok {
		t.Fatal("client state was kept")
	}
	rec := w.post("approver-a", "/approvals/"+out.Decision.RequestId+"/approve", `{"state":"approved","state_version":1,"comment":"ok"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			State string `json:"state"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.State != "pending" {
		t.Fatalf("client state approved the document early: %s", body.Data.State)
	}
}

// D7: two concurrent approvals; one succeeds and the other gets ALREADY_DECIDED.
func TestD7_ConcurrentApprovals(t *testing.T) {
	w := newWorld(t)
	out := w.submit("100.00", "sales_invoice")
	start := make(chan struct{})
	var wg sync.WaitGroup
	type result struct {
		out *approvals.Outcome
		err error
	}
	results := make(chan result, 2)
	for _, user := range []string{"approver-a", "approver-b"} {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			<-start
			got, err := w.svc.Approve(w.ctx, w.as(user), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
			results <- result{got, err}
		}(user)
	}
	close(start)
	wg.Wait()
	close(results)
	var wins, notices int
	for res := range results {
		if res.err != nil {
			t.Fatalf("concurrent decision returned an error: %v", res.err)
		}
		if res.out.Decision.State != "approved" {
			t.Fatalf("state %s", res.out.Decision.State)
		}
		if res.out.Notice == "ALREADY_DECIDED" {
			notices++
		} else if res.out.Notice == "" && res.out.PostingToken != "" {
			wins++
		}
	}
	if wins != 1 || notices != 1 {
		t.Fatalf("wins %d notices %d", wins, notices)
	}

	other := w.submit("200.00", "sales_invoice")
	rec1 := make(chan *httptest.ResponseRecorder, 2)
	var wg2 sync.WaitGroup
	gate := make(chan struct{})
	for _, user := range []string{"approver-a", "approver-b"} {
		wg2.Add(1)
		go func(user string) {
			defer wg2.Done()
			<-gate
			rec1 <- w.post(user, "/approvals/"+other.Decision.RequestId+"/approve", `{"state_version":1}`)
		}(user)
	}
	close(gate)
	wg2.Wait()
	close(rec1)
	okN, noticeN := 0, 0
	for rec := range rec1 {
		if rec.Code != http.StatusOK {
			t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Meta struct{ Notice string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Meta.Notice == "ALREADY_DECIDED" {
			noticeN++
		} else {
			okN++
		}
	}
	if okN != 1 || noticeN != 1 {
		t.Fatalf("http wins %d notices %d", okN, noticeN)
	}
}

// D8: every transition is audited; a refusal survives the aborted transaction.
func TestD8_AuditBeforeRaise(t *testing.T) {
	w := newWorld(t)
	out := w.submit("100.00", "sales_invoice")
	if w.audits(out.Decision.RequestId, "approval.requested") != 1 {
		t.Fatal("submit was not audited")
	}
	_, err := w.svc.Reject(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1, Reason: ""})
	if err == nil || w.versions(out.Decision.RequestId) != 1 || w.refusal(out.Decision.RequestId, "empty_reason") != 1 {
		t.Fatalf("refusal did not survive rollback: %v versions %d", err, w.versions(out.Decision.RequestId))
	}
	var decided int
	if err := w.db.App.QueryRow(w.ctx, `SELECT count(*) FROM river_job WHERE kind='approval.event' AND args->>'type'='approval.decided' AND args->'context'->>'request_id'=$1`, out.Decision.RequestId).Scan(&decided); err != nil {
		t.Fatal(err)
	}
	if decided != 0 {
		t.Fatal("refusal raised an outbox event")
	}
	if _, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if w.audits(out.Decision.RequestId, "approval.approved") != 1 {
		t.Fatal("approval was not audited")
	}
	if err := w.db.App.QueryRow(w.ctx, `SELECT count(*) FROM river_job WHERE kind='approval.event' AND args->>'type'='approval.decided' AND args->'context'->>'request_id'=$1`, out.Decision.RequestId).Scan(&decided); err != nil || decided != 1 {
		t.Fatalf("decided events %d %v", decided, err)
	}
}

// D9: the posting token is single-use and expires.
func TestD9_PostingToken(t *testing.T) {
	w := newWorld(t)
	snap := map[string]any{"sku": "A"}
	out := w.submitAs("initiator", "100.00", "sales_invoice", snap)
	decided, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ConsumePostingToken(w.ctx, w.as("approver-a"), decided.PostingToken, snap); err != nil {
		t.Fatal(err)
	}
	err = w.svc.ConsumePostingToken(w.ctx, w.as("approver-a"), decided.PostingToken, snap)
	if w.code(err) != apierr.Conflict || w.refusal(out.Decision.RequestId, "token_used") != 1 {
		t.Fatalf("second use: %v", err)
	}
	again := w.submitAs("initiator", "150.00", "sales_invoice", snap)
	issued, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: again.Decision.RequestId, StateVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(6 * time.Minute)
	err = w.svc.ConsumePostingToken(w.ctx, w.as("approver-a"), issued.PostingToken, snap)
	if w.code(err) != apierr.Conflict || w.refusal(again.Decision.RequestId, "token_expired") != 1 {
		t.Fatalf("expired: %v", err)
	}
}

// D10: a draft edited after approval fails registration.
func TestD10_HashMismatch(t *testing.T) {
	w := newWorld(t)
	snap := map[string]any{"sku": "A", "qty": "1"}
	out := w.submitAs("initiator", "100.00", "sales_invoice", snap)
	decided, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	edited := map[string]any{"sku": "A", "qty": "9"}
	err = w.svc.ConsumePostingToken(w.ctx, w.as("approver-a"), decided.PostingToken, edited)
	if w.code(err) != apierr.Conflict || w.refusal(out.Decision.RequestId, "hash_mismatch") != 1 {
		t.Fatalf("mismatch: %v", err)
	}
	if err := w.svc.ConsumePostingToken(w.ctx, w.as("approver-a"), decided.PostingToken, snap); err != nil {
		t.Fatal(err)
	}
}

// D11: delegation is time-boxed, logged, visible to the requester, and impossible for the final gate.
func TestD11_Delegation(t *testing.T) {
	w := newWorld(t)
	w.actor("delegate", "Delegate", "Warehouse", "clerk")
	out := w.submit("5000.00", "sales_invoice")
	until := w.clock.Now().Add(time.Hour)
	delegated, err := w.svc.Delegate(w.ctx, w.as("approver-a"), out.Decision.RequestId, "delegate", until, 1)
	if err != nil || delegated.Decision.State != "delegated" {
		t.Fatalf("delegate: %+v %v", delegated, err)
	}
	if w.audits(out.Decision.RequestId, "approval.delegated") != 1 {
		t.Fatal("delegation was not logged")
	}
	cards, err := w.svc.Inbox(w.ctx, w.as("initiator"), "fyi", 50)
	if err != nil || len(cards) != 1 || cards[0].RequestId != out.Decision.RequestId {
		t.Fatalf("not visible to requester: %+v %v", cards, err)
	}
	detail, err := w.svc.Get(w.ctx, w.as("initiator"), out.Decision.RequestId)
	if err != nil || detail.Decisions == nil || len(*detail.Decisions) != 1 || (*detail.Decisions)[0].Decision != "delegated" {
		t.Fatal("delegated decision missing from detail")
	}
	done, err := w.svc.Approve(w.ctx, w.as("delegate"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: int(delegated.Decision.StateVersion)})
	if err != nil || done.Decision.State != "pending" {
		t.Fatalf("delegate approve: %+v %v", done, err)
	}
	_, err = w.svc.Delegate(w.ctx, w.as("final"), out.Decision.RequestId, "delegate", until, int(done.Decision.StateVersion))
	if w.code(err) != apierr.PermissionDenied || w.refusal(out.Decision.RequestId, "final_gate") != 1 {
		t.Fatalf("final gate delegated: %v", err)
	}

	second := w.submit("6000.00", "sales_invoice")
	if _, err := w.svc.Delegate(w.ctx, w.as("approver-a"), second.Decision.RequestId, "delegate", w.clock.Now().Add(time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(2 * time.Minute)
	_, err = w.svc.Approve(w.ctx, w.as("delegate"), approvals.DecisionInput{RequestID: second.Decision.RequestId, StateVersion: 2})
	if w.code(err) != apierr.PermissionDenied {
		t.Fatalf("expired delegation: %v", err)
	}
}

// D12: fraud hints appear when thresholds are met and are configurable.
func TestD12_FraudHintsConfigurable(t *testing.T) {
	w := newWorld(t)
	if err := w.svc.PutFraudConfig(w.ctx, w.as("initiator"), approvals.FraudSettings{
		VariancePercent: "10", RoundAbove: "10000", RoundStep: "1000", RepeatedRejectionMin: 3, CorrectionsPerMonthMin: 3,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := w.svc.Submit(w.ctx, w.as("initiator"), approvals.SubmitInput{
		DocID: "d", DocType: "sales_invoice", DocNumber: "INV-9", Amount: "20000.00", Currency: "AED",
		Snapshot: map[string]any{"n": "1"}, UsualAmount: "10000.00", RejectionChain: 3, CorrectionsThisMonth: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := w.svc.Get(w.ctx, w.as("approver-a"), out.Decision.RequestId)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.FraudHints) < 3 {
		t.Fatalf("hints: %v", detail.FraudHints)
	}
	if err := w.svc.PutFraudConfig(w.ctx, w.as("initiator"), approvals.FraudSettings{
		VariancePercent: "500", RoundAbove: "999999", RoundStep: "1000", RepeatedRejectionMin: 99, CorrectionsPerMonthMin: 99,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = w.svc.Get(w.ctx, w.as("approver-a"), out.Decision.RequestId)
	if err != nil || len(detail.FraudHints) != 0 {
		t.Fatalf("hints still present after raising thresholds: %v %v", detail, err)
	}
}

// D13: the card shows when the approver approved the original document.
func TestD13_OriginalApproverHint(t *testing.T) {
	w := newWorld(t)
	out, err := w.svc.Submit(w.ctx, w.as("initiator"), approvals.SubmitInput{
		DocID: "corr", DocType: "sales_invoice", DocNumber: "CN-1", Amount: "50.00", Currency: "AED",
		Snapshot: map[string]any{"kind": "correction"}, OriginalApproverIDs: []string{"approver-a"}, OriginalDocID: "orig",
	})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := w.svc.Get(w.ctx, w.as("approver-a"), out.Decision.RequestId)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range mine.FraudHints {
		if h == "You approved the original document" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing original-approver hint: %v", mine.FraudHints)
	}
	other, err := w.svc.Get(w.ctx, w.as("approver-b"), out.Decision.RequestId)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range other.FraudHints {
		if h == "You approved the original document" {
			t.Fatal("hint shown to someone who did not approve the original")
		}
	}
}

// D14: step-up is required at or above threshold and for bank changes, payment release, corrections, and break-glass.
func TestD14_StepUp(t *testing.T) {
	w := newWorld(t)
	w.matrix(approvals.Matrix{
		DocType: "sales_invoice", ThresholdAmount: "1000.00", Currency: "AED",
		BelowRoles: []string{"approver"}, FirstRoles: []string{"approver"}, FinalRole: "stakeholder",
		RequiresStepUpAbove: true,
	})
	for _, doc := range []string{"vendor_bank_change", "payment_run", "correction", "break_glass"} {
		w.matrix(approvals.Matrix{
			DocType: doc, ThresholdAmount: "1000.00", Currency: "AED",
			BelowRoles: []string{"approver"}, FirstRoles: []string{"approver"}, FinalRole: "stakeholder",
			RequiresStepUpAbove: false,
		})
	}
	low := w.submit("100.00", "sales_invoice")
	if _, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: low.Decision.RequestId, StateVersion: 1}); err != nil {
		t.Fatal(err)
	}
	high := w.submit("1000.00", "sales_invoice")
	_, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: high.Decision.RequestId, StateVersion: 1})
	if w.code(err) != apierr.StepUpRequired {
		t.Fatalf("threshold: %v", err)
	}
	token := "stepup.approval.approver-a"
	if _, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: high.Decision.RequestId, StateVersion: 1, StepUpToken: token}); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ doc, action string }{
		{"vendor_bank_change", "bank_change"},
		{"payment_run", "payment_release"},
		{"correction", "correction"},
		{"break_glass", "break_glass"},
	}
	for _, tc := range cases {
		out := w.submit("10.00", tc.doc)
		_, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1})
		if w.code(err) != apierr.StepUpRequired {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		tok := "stepup." + tc.action + ".approver-a"
		if _, err := w.svc.Approve(w.ctx, w.as("approver-a"), approvals.DecisionInput{RequestID: out.Decision.RequestId, StateVersion: 1, StepUpToken: tok}); err != nil {
			t.Fatalf("%s with token: %v", tc.doc, err)
		}
	}
}

func TestULIDAndMessages(t *testing.T) {
	id, err := approvals.NewULID(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	if err != nil || len(id) != 26 {
		t.Fatal(id, err)
	}
	en := approvals.NonEmptyReason(context.Background(), "")
	ar := approvals.NonEmptyReason(approvals.WithLang(context.Background(), "ar"), "")
	if en == nil || ar == nil || en.Error() == ar.Error() {
		t.Fatal(en, ar)
	}
}

func TestEventRoundTrip(t *testing.T) {
	w := newWorld(t)
	out := w.submit("100.00", "sales_invoice")
	rows, err := w.db.App.Query(w.ctx, `SELECT args FROM river_job WHERE kind='approval.event' AND args->'context'->>'request_id'=$1`, out.Decision.RequestId)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := approvals.ValidateEvent(raw); err != nil {
			t.Fatalf("event: %v\n%s", err, raw)
		}
		n++
	}
	if n == 0 || out.Decision.RequestId == "" {
		t.Fatal("no event")
	}
	_ = io.EOF
}
