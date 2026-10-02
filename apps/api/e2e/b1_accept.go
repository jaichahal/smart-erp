package e2e

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func scenarioD1(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	below := b.sales("initiator", "100.00", "sales_invoice", nil)
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+below.id+"/approve", `{"state_version":1}`, "initiator")
	if status != http.StatusForbidden || code != "SOD_VIOLATION" {
		t.Fatalf("D1 below: %d %s %s", status, code, raw)
	}
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='sod.self_approval'`, below.id) != 1 {
		t.Fatal("D1 self-approval was not audited")
	}

	b.actor("boss", "Boss", "Sales", "approver")
	above := b.sales("initiator", "5000.00", "sales_invoice", nil)
	status, code, raw = b.call(http.MethodPost, "/api/v1/approvals/"+above.id+"/approve", `{"state_version":1}`, "initiator")
	if code != "SOD_VIOLATION" {
		t.Fatalf("D1 first stage: %d %s %s", status, code, raw)
	}
	b.ok(http.MethodPost, "/api/v1/approvals/"+above.id+"/approve", map[string]any{"state_version": 1}, "boss")
	b.actor("initiator", "Initiator", "Sales", "agent", "stakeholder")
	status, code, raw = b.call(http.MethodPost, "/api/v1/approvals/"+above.id+"/approve", `{"state_version":2}`, "initiator")
	if code != "SOD_VIOLATION" {
		t.Fatalf("D1 final stage: %d %s %s", status, code, raw)
	}
}

func scenarioD2(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/assignments",
		`{"doc_type":"sales_invoice","slot":"below","user_id":"accountant"}`, "accountant")
	if code != "SOD_VIOLATION" {
		t.Fatalf("D2: %d %s %s", status, code, raw)
	}
	var n int
	p := rls.Principal{UserID: "accountant", CompanyID: b.company, Roles: []string{"accountant"}}
	err := rls.Tx(t.Context(), b.s.db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM erp.approval_assignments WHERE user_id='accountant' AND company_id=$1`, b.company).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("D2 assignment stored: %d %v", n, err)
	}
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND event_type='approval.refused' AND reason='accountant_self_assign'`, b.company) != 1 {
		t.Fatal("D2 attempt was not audited")
	}
	b.ok(http.MethodPost, "/api/v1/approvals/assignments", map[string]any{
		"doc_type": "sales_invoice", "slot": "below", "user_id": "approver-a",
	}, "accountant")
}

func scenarioD3(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	low := b.sales("initiator", "100.00", "sales_invoice", nil)
	raw := b.ok(http.MethodPost, "/api/v1/approvals/"+low.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	env := decodeEnv(t, raw)
	if env.Data.State != "approved" || b.tokenOf(raw) == "" {
		t.Fatalf("D3 below: %s", raw)
	}
	high := b.sales("initiator", "5000.00", "sales_invoice", nil)
	raw = b.ok(http.MethodPost, "/api/v1/approvals/"+high.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	env = decodeEnv(t, raw)
	if env.Data.State != "pending" || b.tokenOf(raw) != "" {
		t.Fatalf("D3 first: %s", raw)
	}
	raw = b.ok(http.MethodPost, "/api/v1/approvals/"+high.id+"/approve", map[string]any{"state_version": env.Data.StateVersion}, "final")
	env = decodeEnv(t, raw)
	if env.Data.State != "approved" || b.tokenOf(raw) == "" {
		t.Fatalf("D3 final: %s", raw)
	}
}

func scenarioD4(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	b.actor("acct-init", "Accounts Initiator", "Accounts", "agent")
	out := b.sales("acct-init", "5000.00", "sales_invoice", nil)
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", `{"state_version":1}`, "accounts-peer")
	if code != "SOD_VIOLATION" {
		t.Fatalf("D4: %d %s %s", status, code, raw)
	}
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='sod.accounts_independence'`, out.id) != 1 {
		t.Fatal("D4 independence refusal was not audited")
	}
	if b.versions(out.id) != 1 {
		t.Fatal("D4 refused approval was stored")
	}
	raw = b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	if decodeEnv(t, raw).Data.State != "pending" {
		t.Fatalf("D4 independent approver: %s", raw)
	}
}

func scenarioD5(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	for _, reason := range []string{"", "   ", "\n\t"} {
		err := approvals.NonEmptyReason(t.Context(), reason)
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Code != apierr.ValidationError {
			t.Fatalf("D5 UI layer %q: %v", reason, err)
		}
	}
	ar := approvals.WithLang(t.Context(), "ar")
	if err := approvals.NonEmptyReason(ar, " "); err == nil || err.Error() == approvals.NonEmptyReason(t.Context(), " ").Error() {
		t.Fatal("D5 Arabic message should differ from English")
	}
	out := b.sales("initiator", "100.00", "sales_invoice", nil)
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/reject", `{"reason":"   ","state_version":1}`, "approver-a")
	if status != http.StatusBadRequest || code != "VALIDATION_ERROR" {
		t.Fatalf("D5 API: %d %s %s", status, code, raw)
	}
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='empty_reason'`, out.id) != 1 || b.versions(out.id) != 1 {
		t.Fatal("D5 workflow refusal was not committed apart from the request")
	}
}

func scenarioD6(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	out := b.sales("initiator", "5000.00", "sales_invoice", nil)
	raw := b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{
		"state": "approved", "state_version": 1, "comment": "ok",
	}, "approver-a")
	if decodeEnv(t, raw).Data.State != "pending" {
		t.Fatalf("D6 client state approved the document early: %s", raw)
	}
}

func scenarioD7(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	out := b.sales("initiator", "100.00", "sales_invoice", nil)
	var wg sync.WaitGroup
	type result struct {
		status int
		raw    []byte
	}
	results := make(chan result, 2)
	gate := make(chan struct{})
	for _, user := range []string{"approver-a", "approver-b"} {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			<-gate
			status, _, raw := b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", `{"state_version":1}`, user)
			results <- result{status, raw}
		}(user)
	}
	close(gate)
	wg.Wait()
	close(results)
	wins, notices := 0, 0
	for res := range results {
		if res.status != http.StatusOK {
			t.Fatalf("D7 concurrent decision returned an error: %d %s", res.status, res.raw)
		}
		env := decodeEnv(t, res.raw)
		if env.Error.Code != "" {
			t.Fatalf("D7 error envelope: %s", res.raw)
		}
		if env.Data.State != "approved" {
			t.Fatalf("D7 state %s", env.Data.State)
		}
		if env.Meta.Notice == "ALREADY_DECIDED" {
			notices++
		} else if env.Meta.Notice == "" && b.tokenOf(res.raw) != "" {
			wins++
		}
	}
	if wins != 1 || notices != 1 {
		t.Fatalf("D7 wins %d notices %d", wins, notices)
	}
}

func scenarioD8(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	out := b.sales("initiator", "100.00", "sales_invoice", nil)
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.requested'`, out.id) != 1 {
		t.Fatal("D8 submit was not audited")
	}
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/reject", `{"reason":"","state_version":1}`, "approver-a")
	if code != "VALIDATION_ERROR" || b.versions(out.id) != 1 || b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='empty_reason'`, out.id) != 1 {
		t.Fatalf("D8 refusal did not survive: %d %s %s", status, code, raw)
	}
	decided := b.count(`SELECT count(*) FROM river_job WHERE kind='approval.event' AND args->>'type'='approval.decided' AND args->'context'->>'request_id'=$1`, out.id)
	if decided != 0 {
		t.Fatal("D8 refusal raised an outbox event")
	}
	b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.approved'`, out.id) != 1 {
		t.Fatal("D8 approval was not audited")
	}
	if b.count(`SELECT count(*) FROM river_job WHERE kind='approval.event' AND args->>'type'='approval.decided' AND args->'context'->>'request_id'=$1`, out.id) != 1 {
		t.Fatal("D8 decided event was not raised after commit")
	}
}

func scenarioD9(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	snap := map[string]any{"sku": "A"}
	out := b.sales("initiator", "100.00", "sales_invoice", snap)
	raw := b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	tok := b.tokenOf(raw)
	if tok == "" {
		t.Fatalf("D9 no token: %s", raw)
	}
	b.ok(http.MethodPost, "/api/v1/approvals/posting-tokens/consume", map[string]any{"token": tok, "content": snap}, "approver-a")
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/posting-tokens/consume", mustJSONString(t, map[string]any{"token": tok, "content": snap}), "approver-a")
	if code != "CONFLICT" || b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='token_used'`, out.id) != 1 {
		t.Fatalf("D9 second use: %d %s %s", status, code, raw)
	}
	again := b.sales("initiator", "150.00", "sales_invoice", snap)
	raw = b.ok(http.MethodPost, "/api/v1/approvals/"+again.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	tok = b.tokenOf(raw)
	approvalClock.Advance(6 * time.Minute)
	status, code, raw = b.call(http.MethodPost, "/api/v1/approvals/posting-tokens/consume", mustJSONString(t, map[string]any{"token": tok, "content": snap}), "approver-a")
	if code != "CONFLICT" || b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='token_expired'`, again.id) != 1 {
		t.Fatalf("D9 expired: %d %s %s", status, code, raw)
	}
}

func scenarioD10(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	snap := map[string]any{"sku": "A", "qty": "1"}
	out := b.sales("initiator", "100.00", "sales_invoice", snap)
	raw := b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	tok := b.tokenOf(raw)
	edited := map[string]any{"sku": "A", "qty": "9"}
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/posting-tokens/consume", mustJSONString(t, map[string]any{"token": tok, "content": edited}), "approver-a")
	if code != "CONFLICT" || b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='hash_mismatch'`, out.id) != 1 {
		t.Fatalf("D10 mismatch: %d %s %s", status, code, raw)
	}
	b.ok(http.MethodPost, "/api/v1/approvals/posting-tokens/consume", map[string]any{"token": tok, "content": snap}, "approver-a")
}

func scenarioD11(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	b.actor("delegate", "Delegate", "Warehouse", "clerk")
	out := b.sales("initiator", "5000.00", "sales_invoice", nil)
	until := approvalClock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	raw := b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/delegate", map[string]any{
		"to_user_id": "delegate", "until": until, "state_version": 1,
	}, "approver-a")
	env := decodeEnv(t, raw)
	if env.Data.State != "delegated" {
		t.Fatalf("D11 delegate: %s", raw)
	}
	if b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.delegated'`, out.id) != 1 {
		t.Fatal("D11 delegation was not logged")
	}
	inboxRaw := b.ok(http.MethodGet, "/api/v1/approvals/inbox?state=fyi", nil, "initiator")
	var inbox struct {
		Data []struct {
			RequestID string `json:"request_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(inboxRaw, &inbox); err != nil || len(inbox.Data) != 1 || inbox.Data[0].RequestID != out.id {
		t.Fatalf("D11 not visible to requester: %s", inboxRaw)
	}
	detailRaw := b.ok(http.MethodGet, "/api/v1/approvals/"+out.id, nil, "initiator")
	var detail struct {
		Data struct {
			Decisions []struct {
				Decision string `json:"decision"`
			} `json:"decisions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailRaw, &detail); err != nil || len(detail.Data.Decisions) != 1 || detail.Data.Decisions[0].Decision != "delegated" {
		t.Fatalf("D11 delegated decision missing: %s", detailRaw)
	}
	done := b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{"state_version": env.Data.StateVersion}, "delegate")
	if decodeEnv(t, done).Data.State != "pending" {
		t.Fatalf("D11 delegate approve: %s", done)
	}
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/delegate", mustJSONString(t, map[string]any{
		"to_user_id": "delegate", "until": until, "state_version": decodeEnv(t, done).Data.StateVersion,
	}), "final")
	if code != "PERMISSION_DENIED" || b.count(`SELECT count(*) FROM erp.audit_events WHERE reference_id=$1 AND event_type='approval.refused' AND reason='final_gate'`, out.id) != 1 {
		t.Fatalf("D11 final gate delegated: %d %s %s", status, code, raw)
	}

	second := b.sales("initiator", "6000.00", "sales_invoice", nil)
	short := approvalClock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	b.ok(http.MethodPost, "/api/v1/approvals/"+second.id+"/delegate", map[string]any{
		"to_user_id": "delegate", "until": short, "state_version": 1,
	}, "approver-a")
	approvalClock.Advance(2 * time.Minute)
	status, code, raw = b.call(http.MethodPost, "/api/v1/approvals/"+second.id+"/approve", `{"state_version":2}`, "delegate")
	if code != "PERMISSION_DENIED" {
		t.Fatalf("D11 expired delegation: %d %s %s", status, code, raw)
	}
}

func scenarioD12(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	b.ok(http.MethodPost, "/api/v1/approvals/fraud-config", map[string]any{
		"variance_percent": "10", "round_above": "10000", "round_step": "1000",
		"repeated_rejection_min": 3, "corrections_per_month_min": 3,
	}, "initiator")
	out := b.submit("initiator", map[string]any{
		"doc_id": "d", "doc_type": "sales_invoice", "doc_number": "INV-9", "amount": "20000.00", "currency": "AED",
		"snapshot": map[string]any{"n": "1"}, "usual_amount": "10000.00", "rejection_chain": 3, "corrections_this_month": 3,
	})
	detail := fraudHints(t, b.ok(http.MethodGet, "/api/v1/approvals/"+out.id, nil, "approver-a"))
	if len(detail) < 3 {
		t.Fatalf("D12 hints: %v", detail)
	}
	b.ok(http.MethodPost, "/api/v1/approvals/fraud-config", map[string]any{
		"variance_percent": "500", "round_above": "999999", "round_step": "1000",
		"repeated_rejection_min": 99, "corrections_per_month_min": 99,
	}, "initiator")
	detail = fraudHints(t, b.ok(http.MethodGet, "/api/v1/approvals/"+out.id, nil, "approver-a"))
	if len(detail) != 0 {
		t.Fatalf("D12 hints still present after raising thresholds: %v", detail)
	}
}

func scenarioD13(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	out := b.submit("initiator", map[string]any{
		"doc_id": "corr", "doc_type": "sales_invoice", "doc_number": "CN-1", "amount": "50.00", "currency": "AED",
		"snapshot": map[string]any{"kind": "correction"}, "original_approver_ids": []string{"approver-a"}, "original_doc_id": "orig",
	})
	mine := fraudHints(t, b.ok(http.MethodGet, "/api/v1/approvals/"+out.id, nil, "approver-a"))
	found := false
	for _, h := range mine {
		if h == "You approved the original document" {
			found = true
		}
	}
	if !found {
		t.Fatalf("D13 missing original-approver hint: %v", mine)
	}
	other := fraudHints(t, b.ok(http.MethodGet, "/api/v1/approvals/"+out.id, nil, "approver-b"))
	for _, h := range other {
		if h == "You approved the original document" {
			t.Fatal("D13 hint shown to someone who did not approve the original")
		}
	}
}

func scenarioD14(t *testing.T, s *stack) {
	b := newB1(t, s)
	b.seedSales()
	b.matrix("sales_invoice", "1000.00", true)
	for _, doc := range []string{"vendor_bank_change", "payment_run", "correction", "break_glass"} {
		b.matrix(doc, "1000.00", false)
	}
	low := b.sales("initiator", "100.00", "sales_invoice", nil)
	b.ok(http.MethodPost, "/api/v1/approvals/"+low.id+"/approve", map[string]any{"state_version": 1}, "approver-a")
	high := b.sales("initiator", "1000.00", "sales_invoice", nil)
	status, code, raw := b.call(http.MethodPost, "/api/v1/approvals/"+high.id+"/approve", `{"state_version":1}`, "approver-a")
	if code != "STEP_UP_REQUIRED" {
		t.Fatalf("D14 threshold: %d %s %s", status, code, raw)
	}
	b.ok(http.MethodPost, "/api/v1/approvals/"+high.id+"/approve", map[string]any{
		"state_version": 1, "step_up_token": "stepup.approval.approver-a",
	}, "approver-a")
	for _, tc := range []struct{ doc, action string }{
		{"vendor_bank_change", "bank_change"},
		{"payment_run", "payment_release"},
		{"correction", "correction"},
		{"break_glass", "break_glass"},
	} {
		out := b.sales("initiator", "10.00", tc.doc, nil)
		status, code, raw = b.call(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", `{"state_version":1}`, "approver-a")
		if code != "STEP_UP_REQUIRED" {
			t.Fatalf("D14 %s: %d %s %s", tc.doc, status, code, raw)
		}
		b.ok(http.MethodPost, "/api/v1/approvals/"+out.id+"/approve", map[string]any{
			"state_version": 1, "step_up_token": "stepup." + tc.action + ".approver-a",
		}, "approver-a")
	}
}

func fraudHints(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Data struct {
			FraudHints []string `json:"fraud_hints"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Data.FraudHints
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf)
}
