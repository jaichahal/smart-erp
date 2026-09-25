package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// approvalClock is the clock the composed API uses while these cases run.
// Mount reads it once, so the pointer is installed before the first test.
var approvalClock = approvals.NewFakeClock(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))

func init() {
	approvals.UseClock(approvalClock)
	approvals.RequestPrincipal = func(r *http.Request) (rls.Principal, bool) {
		user := r.Header.Get("X-User")
		company, err := uuid.Parse(r.Header.Get("X-Company"))
		if user == "" || err != nil {
			return rls.Principal{}, false
		}
		var roles []string
		if raw := r.Header.Get("X-Roles"); raw != "" {
			roles = strings.Split(raw, ",")
		}
		return rls.Principal{UserID: user, CompanyID: company, Roles: roles}, true
	}
	for id, run := range map[string]func(*testing.T, *stack){
		"D1":  scenarioD1,
		"D2":  scenarioD2,
		"D3":  scenarioD3,
		"D4":  scenarioD4,
		"D5":  scenarioD5,
		"D6":  scenarioD6,
		"D7":  scenarioD7,
		"D8":  scenarioD8,
		"D9":  scenarioD9,
		"D10": scenarioD10,
		"D11": scenarioD11,
		"D12": scenarioD12,
		"D13": scenarioD13,
		"D14": scenarioD14,
	} {
		acceptanceScenarios[id] = run
	}
}

type b1 struct {
	t       *testing.T
	s       *stack
	company uuid.UUID
}

func newB1(t *testing.T, s *stack) *b1 {
	t.Helper()
	approvalClock.Set(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	return &b1{t: t, s: s, company: uuid.New()}
}

func (b *b1) hdr(user string) map[string]string {
	return map[string]string{"X-User": user, "X-Company": b.company.String(), "X-Roles": "approver"}
}

func (b *b1) call(method, path, body, user string) (int, string, []byte) {
	b.t.Helper()
	return b.s.call(b.t, method, path, body, b.hdr(user))
}

func (b *b1) ok(method, path string, body any, user string) []byte {
	b.t.Helper()
	rawBody := ""
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		rawBody = string(buf)
	}
	status, code, raw := b.call(method, path, rawBody, user)
	if status >= 400 {
		b.t.Fatalf("%s %s as %s -> %d %s %s", method, path, user, status, code, raw)
	}
	return raw
}

type decisionEnv struct {
	Data struct {
		RequestID    string `json:"request_id"`
		State        string `json:"state"`
		StateVersion int    `json:"state_version"`
	} `json:"data"`
	Meta struct {
		Notice string         `json:"notice"`
		Extra  map[string]any `json:"extra"`
	} `json:"meta"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeEnv(t *testing.T, raw []byte) decisionEnv {
	t.Helper()
	var env decisionEnv
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("json: %v body %s", err, raw)
	}
	return env
}

func (b *b1) actor(id, name, dept string, roles ...string) {
	b.t.Helper()
	b.ok(http.MethodPost, "/api/v1/approvals/actors", map[string]any{
		"id": id, "name": name, "department": dept, "roles": roles,
	}, id)
}

func (b *b1) matrix(doc, threshold string, stepUp bool) {
	b.t.Helper()
	b.ok(http.MethodPost, "/api/v1/approvals/matrix", map[string]any{
		"doc_type": doc, "threshold_amount": threshold, "currency": "AED",
		"below_roles": []string{"approver"}, "first_roles": []string{"approver"},
		"final_role": "stakeholder", "requires_step_up_above": stepUp,
	}, "initiator")
}

func (b *b1) seedSales() {
	b.t.Helper()
	b.actor("initiator", "Initiator", "Sales", "agent")
	b.actor("approver-a", "Approver A", "Management", "approver")
	b.actor("approver-b", "Approver B", "Management", "approver")
	b.actor("final", "Final Gate", "Stakeholder", "stakeholder")
	b.actor("accountant", "Accountant", "Accounts", "accountant")
	b.actor("accounts-peer", "Accounts Peer", "Accounts", "approver")
	b.matrix("sales_invoice", "1000.00", false)
}

type submitted struct {
	id      string
	version int
	raw     []byte
}

func (b *b1) submit(user string, body map[string]any) submitted {
	b.t.Helper()
	raw := b.ok(http.MethodPost, "/api/v1/approvals/requests", body, user)
	env := decodeEnv(b.t, raw)
	if env.Data.RequestID == "" || env.Data.StateVersion == 0 {
		b.t.Fatalf("submit: %s", raw)
	}
	return submitted{id: env.Data.RequestID, version: env.Data.StateVersion, raw: raw}
}

func (b *b1) sales(user, amount, doc string, snap map[string]any) submitted {
	b.t.Helper()
	if snap == nil {
		snap = map[string]any{"line": "1"}
	}
	return b.submit(user, map[string]any{
		"doc_id": "doc-" + amount + "-" + doc, "doc_type": doc, "doc_number": "INV-1",
		"party": "Al Noor", "amount": amount, "currency": "AED", "snapshot": snap, "state": "approved",
	})
}

func (b *b1) count(q string, args ...any) int {
	b.t.Helper()
	var n int
	if err := b.s.db.App.QueryRow(b.t.Context(), q, args...).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *b1) versions(requestID string) int {
	b.t.Helper()
	var n int
	p := rls.Principal{UserID: "initiator", CompanyID: b.company, Roles: []string{"approver"}}
	err := rls.Tx(b.t.Context(), b.s.db.App, p, func(tx pgx.Tx) error {
		return tx.QueryRow(b.t.Context(), `SELECT count(*) FROM erp.approval_requests WHERE request_id=$1`, requestID).Scan(&n)
	})
	if err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *b1) tokenOf(raw []byte) string {
	b.t.Helper()
	env := decodeEnv(b.t, raw)
	if env.Meta.Extra == nil {
		return ""
	}
	tok, _ := env.Meta.Extra["posting_token"].(string)
	return tok
}
