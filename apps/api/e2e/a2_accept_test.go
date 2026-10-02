package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwk"

	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
)

func (s *stack) account(login, password string, company uuid.UUID, id string, roles []string) identity.Account {
	if id == "" {
		id = uuid.NewString()
	}
	if roles == nil {
		roles = []string{}
	}
	a := identity.Account{
		ID: id, LoginName: login, Name: login, CompanyID: company,
		Roles: roles, Personas: []string{"staff"}, StepUpMethods: []string{"totp"},
	}
	s.dir.put(a)
	s.broker.add(login, a.ID, password, "654321")
	return a
}

func (s *stack) accessToken(t *testing.T, a identity.Account, password string) string {
	t.Helper()
	_, pub := devicePublicKey(t)
	status, _, raw := s.call(t, http.MethodPost, "/api/v1/auth/device/enroll", string(mustJSON(t, map[string]any{
		"public_key": pub, "platform": "console", "app_version": "1.0.0", "device_name": "console",
	})), nil)
	if status != http.StatusCreated {
		t.Fatalf("enroll %d %s", status, raw)
	}
	deviceID := dataString(t, raw, "device_id")
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/auth/session", string(mustJSON(t, map[string]string{"login_name": a.LoginName})), nil)
	if status != http.StatusOK {
		t.Fatalf("session %d %s", status, raw)
	}
	sessionID := dataString(t, raw, "session_id")
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/auth/session/"+sessionID+"/check", string(mustJSON(t, map[string]string{"password": password})), nil)
	if status != http.StatusOK {
		t.Fatalf("check %d %s", status, raw)
	}
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/auth/token", string(mustJSON(t, map[string]string{
		"session_id": sessionID, "device_id": deviceID,
	})), nil)
	if status != http.StatusOK {
		t.Fatalf("token %d %s", status, raw)
	}
	token := dataString(t, raw, "access_token")
	if token == "" {
		t.Fatalf("empty token %s", raw)
	}
	return token
}

func devicePublicKey(t *testing.T) (jwk.Key, map[string]any) {
	t.Helper()
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return key, m
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dataString(t *testing.T, raw []byte, field string) string {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	s, _ := env.Data[field].(string)
	return s
}

func authzHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func acceptA11(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	password := "secret"
	sales := s.account("a11-sales-"+uuid.NewString(), password, company, "", []string{"sales_agent"})
	acct := s.account("a11-acct-"+uuid.NewString(), password, company, "", []string{"accountant"})
	salesTok := s.accessToken(t, sales, password)
	acctTok := s.accessToken(t, acct, password)
	payloads := []any{
		map[string]any{
			"sku": "FG-1", "price": "12.00", "cost": "COST-SENTINEL", "margin": "MARGIN-SENTINEL",
			"pricing": map[string]any{"name": "Panel", "unit_cost": "COST-SENTINEL", "margin_amount": "MARGIN-SENTINEL"},
			"lines":   []any{map[string]any{"qty": "2", "total_cost": "COST-SENTINEL", "gross_margin": "MARGIN-SENTINEL"}},
		},
		[]any{map[string]any{"name": "Panel", "line_cost": "COST-SENTINEL", "margin_percent": "MARGIN-SENTINEL"}},
	}
	for _, payload := range payloads {
		body := string(mustJSON(t, payload))
		status, _, raw := s.call(t, http.MethodPost, "/api/v1/documents/preview", body, authzHeader(salesTok))
		if status != http.StatusOK {
			t.Fatalf("A11 sales preview %d %s", status, raw)
		}
		if bytes.Contains(raw, []byte("COST-SENTINEL")) || bytes.Contains(raw, []byte("MARGIN-SENTINEL")) ||
			bytes.Contains(raw, []byte("unit_cost")) || bytes.Contains(raw, []byte("gross_margin")) ||
			bytes.Contains(raw, []byte("line_cost")) || bytes.Contains(raw, []byte("margin_percent")) ||
			bytes.Contains(raw, []byte("margin_amount")) || bytes.Contains(raw, []byte("total_cost")) {
			t.Fatalf("A11 sales payload still exposes cost or margin: %s", raw)
		}
		status, _, raw = s.call(t, http.MethodPost, "/api/v1/documents/preview", body, authzHeader(acctTok))
		if status != http.StatusOK {
			t.Fatalf("A11 accountant preview %d %s", status, raw)
		}
		if !bytes.Contains(raw, []byte("COST-SENTINEL")) || !bytes.Contains(raw, []byte("MARGIN-SENTINEL")) {
			t.Fatalf("A11 accountant lost cost or margin: %s", raw)
		}
	}
}

func acceptA12(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	other := uuid.New()
	territory := uuid.New()
	elsewhere := uuid.New()
	password := "secret"
	manager := s.account("a12-mgr-"+uuid.NewString(), password, company, "", []string{"system_manager"})
	sales := s.account("a12-sales-"+uuid.NewString(), password, company, "", []string{"sales_agent"})
	accountant := s.account("a12-acct-"+uuid.NewString(), password, company, "", []string{"accountant"})
	mgrTok := s.accessToken(t, manager, password)
	status, _, raw := s.call(t, http.MethodPost, "/api/v1/users", string(mustJSON(t, map[string]any{
		"id": sales.ID, "name": "Sales Agent One", "territory_id": territory,
	})), authzHeader(mgrTok))
	if status != http.StatusCreated {
		t.Fatalf("A12 create sales user %d %s", status, raw)
	}
	for i := 0; i < 5; i++ {
		insertCustomer(t, s, company, territory, "someone-else", "home-"+uuid.NewString())
	}
	for i := 0; i < 3; i++ {
		insertCustomer(t, s, company, elsewhere, "someone-else", "away-"+uuid.NewString())
	}
	for i := 0; i < 2; i++ {
		insertCustomer(t, s, other, territory, "someone-else", "other-"+uuid.NewString())
	}
	salesTok := s.accessToken(t, sales, password)
	rows, total := pageCustomers(t, s, salesTok, 2)
	if total != 5 {
		t.Fatalf("A12 total = %d, want 5 (territory only)", total)
	}
	if rows != total {
		t.Fatalf("A12 rows across pages %d, total %d", rows, total)
	}
	acctTok := s.accessToken(t, accountant, password)
	rows, total = pageCustomers(t, s, acctTok, 3)
	if total != 8 || rows != total {
		t.Fatalf("A12 accountant rows %d total %d, want 8 and equal", rows, total)
	}
}

func insertCustomer(t *testing.T, s *stack, company, territory uuid.UUID, owner, name string) {
	t.Helper()
	_, err := s.db.Migrator.Exec(context.Background(), `
		INSERT INTO erp.customers (id, company_id, territory_id, owner_id, name)
		VALUES ($1, $2, $3, $4, $5)`, uuid.New(), company, territory, owner, name)
	if err != nil {
		t.Fatal(err)
	}
}

func pageCustomers(t *testing.T, s *stack, token string, limit int) (rows, total int) {
	t.Helper()
	cursor := ""
	pages := 0
	for {
		pages++
		if pages > 20 {
			t.Fatal("A12 pagination did not end")
		}
		path := "/api/v1/customers?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		status, _, raw := s.call(t, http.MethodGet, path, "", authzHeader(token))
		if status != http.StatusOK {
			t.Fatalf("A12 customers %d %s", status, raw)
		}
		var env struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Meta struct {
				Total      int     `json:"total"`
				NextCursor *string `json:"next_cursor"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if pages == 1 {
			total = env.Meta.Total
		} else if env.Meta.Total != total {
			t.Fatalf("A12 total changed from %d to %d", total, env.Meta.Total)
		}
		rows += len(env.Data)
		if env.Meta.NextCursor == nil || *env.Meta.NextCursor == "" {
			break
		}
		cursor = *env.Meta.NextCursor
	}
	return rows, total
}

func acceptA13(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	password := "secret"
	unknown := s.account("a13-unknown-"+uuid.NewString(), password, company, "", []string{"visiting_wizard"})
	none := s.account("a13-none-"+uuid.NewString(), password, company, "", []string{})
	mixed := s.account("a13-mixed-"+uuid.NewString(), password, company, "", []string{"sales_agent", "visiting_wizard"})
	boss := s.account("a13-boss-"+uuid.NewString(), password, company, "", []string{"system_manager"})

	unk := experienceOf(t, s, s.accessToken(t, unknown, password))
	if !unk.Least || len(unk.Personas) != 1 || unk.Personas[0] != "least_privileged" || len(unk.Tabs) != 0 || len(unk.Endpoints) != 0 {
		t.Fatalf("A13 unknown experience %+v", unk)
	}
	status, code, raw := s.call(t, http.MethodGet, "/api/v1/customers", "", authzHeader(s.accessToken(t, unknown, password)))
	if status != http.StatusForbidden || code != "PERMISSION_DENIED" {
		t.Fatalf("A13 unknown customers %d %s %s", status, code, raw)
	}
	status, code, raw = s.call(t, http.MethodPost, "/api/v1/sod-matrix", `{"kind":"role_pair","left_code":"auditor","right_code":"stakeholder"}`, map[string]string{
		"Authorization": "Bearer " + s.accessToken(t, unknown, password), "If-Match": "0",
	})
	if status != http.StatusForbidden || code != "PERMISSION_DENIED" {
		t.Fatalf("A13 unknown sod %d %s %s", status, code, raw)
	}

	empty := experienceOf(t, s, s.accessToken(t, none, password))
	if !empty.Least || len(empty.Tabs) != 0 || len(empty.Endpoints) != 0 {
		t.Fatalf("A13 missing role %+v", empty)
	}
	status, code, raw = s.call(t, http.MethodGet, "/api/v1/customers", "", authzHeader(s.accessToken(t, none, password)))
	if status != http.StatusForbidden || code != "PERMISSION_DENIED" {
		t.Fatalf("A13 missing customers %d %s %s", status, code, raw)
	}

	mix := experienceOf(t, s, s.accessToken(t, mixed, password))
	if mix.Least || len(mix.Tabs) != 0 || len(mix.Endpoints) != 0 {
		t.Fatalf("A13 mixed experience %+v", mix)
	}
	status, _, raw = s.call(t, http.MethodGet, "/api/v1/customers", "", authzHeader(s.accessToken(t, mixed, password)))
	if status != http.StatusOK {
		t.Fatalf("A13 recognised sales role was dropped: %d %s", status, raw)
	}

	manager := experienceOf(t, s, s.accessToken(t, boss, password))
	if manager.Least || len(manager.Tabs) == 0 || !has(manager.Endpoints, "GET /status") || !has(manager.Endpoints, "POST /sod-matrix") {
		t.Fatalf("A13 system manager lost privileged access: %+v", manager)
	}
}

type experienceBody struct {
	Personas  []string
	Tabs      []string
	Endpoints []string
	Least     bool
}

func experienceOf(t *testing.T, s *stack, token string) experienceBody {
	t.Helper()
	status, _, raw := s.call(t, http.MethodGet, "/api/v1/experience", "", authzHeader(token))
	if status != http.StatusOK {
		t.Fatalf("experience %d %s", status, raw)
	}
	var env struct {
		Data struct {
			Personas            []string `json:"personas"`
			PrivilegedTabs      []string `json:"privileged_tabs"`
			PrivilegedEndpoints []string `json:"privileged_endpoints"`
			LeastPrivileged     bool     `json:"least_privileged"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return experienceBody{Personas: env.Data.Personas, Tabs: env.Data.PrivilegedTabs, Endpoints: env.Data.PrivilegedEndpoints, Least: env.Data.LeastPrivileged}
}

func has(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

func acceptA14(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	password := "secret"
	manager := s.account("a14-mgr-"+uuid.NewString(), password, company, "", []string{"system_manager"})
	tok := s.accessToken(t, manager, password)
	userID := "user-" + uuid.NewString()
	status, _, raw := s.call(t, http.MethodPost, "/api/v1/users", string(mustJSON(t, map[string]string{"id": userID, "name": "Pair User"})), authzHeader(tok))
	if status != http.StatusCreated {
		t.Fatalf("A14 create %d %s", status, raw)
	}
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/users/"+userID+"/roles", `{"roles":["sales_agent"]}`, authzHeader(tok))
	if status != http.StatusOK {
		t.Fatalf("A14 first assign %d %s", status, raw)
	}
	status, code, raw := s.call(t, http.MethodPost, "/api/v1/users/"+userID+"/roles", `{"roles":["sales_agent","approver"]}`, authzHeader(tok))
	if status != http.StatusForbidden || code != "SOD_VIOLATION" {
		t.Fatalf("A14 incompatible pair %d %s %s", status, code, raw)
	}
	var refusals int
	if err := s.db.App.QueryRow(context.Background(), `SELECT count(*) FROM erp.audit_events WHERE reference_id = $1 AND event_type = 'sod.assignment_refused'`, userID).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if refusals != 1 {
		t.Fatalf("A14 refusal audits = %d, want 1", refusals)
	}
	got := getUser(t, s, tok, userID)
	if len(got.Roles) != 1 || got.Roles[0] != "sales_agent" || got.Name != "Pair User" {
		t.Fatalf("A14 roles after refusal %+v", got)
	}
	approvalID := "apr-" + uuid.NewString()
	if _, err := s.db.App.Exec(context.Background(), `INSERT INTO erp.authz_settings (key, value) VALUES ($1, $2)`, "sod.override."+approvalID, userID); err != nil {
		t.Fatal(err)
	}
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/users/"+userID+"/roles", string(mustJSON(t, map[string]any{
		"roles": []string{"approver", "sales_agent"}, "override_approval_id": approvalID,
	})), authzHeader(tok))
	if status != http.StatusOK {
		t.Fatalf("A14 override %d %s", status, raw)
	}
	got = getUser(t, s, tok, userID)
	if len(got.Roles) != 2 {
		t.Fatalf("A14 roles after override %+v", got)
	}
	var changes int
	if err := s.db.App.QueryRow(context.Background(), `SELECT count(*) FROM erp.audit_events WHERE reference_id = $1 AND event_type = 'role.changed'`, userID).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes < 2 {
		t.Fatalf("A14 role.changed audits = %d, want at least 2", changes)
	}
}

type userBody struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Disabled bool     `json:"disabled"`
	Roles    []string `json:"roles"`
}

func getUser(t *testing.T, s *stack, token, id string) userBody {
	t.Helper()
	status, _, raw := s.call(t, http.MethodGet, "/api/v1/users/"+id, "", authzHeader(token))
	if status != http.StatusOK {
		t.Fatalf("get user %d %s", status, raw)
	}
	var env struct {
		Data userBody `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func acceptA15(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	password := "secret"
	manager := s.account("a15-mgr-"+uuid.NewString(), password, company, "", []string{"system_manager"})
	person := s.account("a15-user-"+uuid.NewString(), password, company, "", []string{"sales_agent"})
	mgrTok := s.accessToken(t, manager, password)
	status, _, raw := s.call(t, http.MethodPost, "/api/v1/users", string(mustJSON(t, map[string]string{"id": person.ID, "name": "Still Here"})), authzHeader(mgrTok))
	if status != http.StatusCreated {
		t.Fatalf("A15 create %d %s", status, raw)
	}
	live := s.accessToken(t, person, password)
	status, _, raw = s.call(t, http.MethodGet, "/api/v1/experience", "", authzHeader(live))
	if status != http.StatusOK {
		t.Fatalf("A15 enabled authenticate %d %s", status, raw)
	}
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/users/"+person.ID+"/disable", `{}`, authzHeader(mgrTok))
	if status != http.StatusOK {
		t.Fatalf("A15 disable %d %s", status, raw)
	}
	got := getUser(t, s, mgrTok, person.ID)
	if !got.Disabled || got.Name != "Still Here" {
		t.Fatalf("A15 resolved %+v", got)
	}
	status, code, raw := s.call(t, http.MethodGet, "/api/v1/experience", "", authzHeader(live))
	if status != http.StatusUnauthorized || code != "AUTH_REQUIRED" {
		t.Fatalf("A15 disabled token %d %s %s", status, code, raw)
	}
	fresh := s.accessToken(t, person, password)
	status, code, raw = s.call(t, http.MethodGet, "/api/v1/experience", "", authzHeader(fresh))
	if status != http.StatusUnauthorized || code != "AUTH_REQUIRED" {
		t.Fatalf("A15 fresh token after disable %d %s %s", status, code, raw)
	}
	var can bool
	if err := s.db.App.QueryRow(context.Background(), `SELECT erp.user_can_authenticate($1)`, person.ID).Scan(&can); err != nil {
		t.Fatal(err)
	}
	if can {
		t.Fatal("A15 disabled user can still authenticate")
	}
	if _, err := s.db.App.Exec(context.Background(), `DELETE FROM erp.users WHERE id = $1`, person.ID); err == nil {
		t.Fatal("A15 application role deleted a user")
	}
	if _, err := s.db.Migrator.Exec(context.Background(), `DELETE FROM erp.users WHERE id = $1`, person.ID); err == nil {
		t.Fatal("A15 owner role deleted a user")
	}
	got = getUser(t, s, mgrTok, person.ID)
	if got.Name != "Still Here" {
		t.Fatalf("A15 user missing after delete attempts: %+v", got)
	}
}

func acceptA16(t *testing.T, s *stack) {
	t.Helper()
	company := uuid.New()
	password := "secret"
	manager := s.account("a16-mgr-"+uuid.NewString(), password, company, "", []string{"system_manager"})
	system := s.account("a16-sys-"+uuid.NewString(), password, company, "", []string{"system"})
	stakeholder := s.account("a16-sh-"+uuid.NewString(), password, company, "", []string{"stakeholder"})
	mgrTok := s.accessToken(t, manager, password)
	asOf := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	login := asOf.Add(-48 * time.Hour)
	confirmedID := "confirmed-" + uuid.NewString()
	overdueID := "overdue-" + uuid.NewString()
	for _, id := range []string{confirmedID, overdueID} {
		status, _, raw := s.call(t, http.MethodPost, "/api/v1/users", string(mustJSON(t, map[string]any{
			"id": id, "name": id, "last_login": login,
		})), authzHeader(mgrTok))
		if status != http.StatusCreated {
			t.Fatalf("A16 create %s %d %s", id, status, raw)
		}
		status, _, raw = s.call(t, http.MethodPost, "/api/v1/users/"+id+"/roles", `{"roles":["sales_agent"]}`, authzHeader(mgrTok))
		if status != http.StatusOK {
			t.Fatalf("A16 roles %s %d %s", id, status, raw)
		}
	}
	sysTok := s.accessToken(t, system, password)
	status, _, raw := s.call(t, http.MethodPost, "/api/v1/access-reviews", string(mustJSON(t, map[string]string{"as_of": asOf.Format(time.RFC3339)})), authzHeader(sysTok))
	if status != http.StatusOK {
		t.Fatalf("A16 review %d %s", status, raw)
	}
	var opened struct {
		Data struct {
			ID    string `json:"id"`
			Items []struct {
				UserID    string     `json:"user_id"`
				Roles     []string   `json:"roles"`
				LastLogin *time.Time `json:"last_login"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.Data.Items) != 2 {
		t.Fatalf("A16 review items = %d, want 2 (%s)", len(opened.Data.Items), raw)
	}
	for _, item := range opened.Data.Items {
		if len(item.Roles) != 1 || item.Roles[0] != "sales_agent" {
			t.Fatalf("A16 item roles = %v", item.Roles)
		}
		if item.LastLogin == nil || !item.LastLogin.Equal(login) {
			t.Fatalf("A16 last login = %v, want %v", item.LastLogin, login)
		}
	}
	shTok := s.accessToken(t, stakeholder, password)
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/access-reviews/"+opened.Data.ID+"/confirm", string(mustJSON(t, map[string]string{"user_id": confirmedID})), authzHeader(shTok))
	if status != http.StatusOK {
		t.Fatalf("A16 confirm %d %s", status, raw)
	}
	later := asOf.Add(15 * 24 * time.Hour)
	status, _, raw = s.call(t, http.MethodPost, "/api/v1/access-reviews", string(mustJSON(t, map[string]string{"as_of": later.Format(time.RFC3339)})), authzHeader(sysTok))
	if status != http.StatusOK {
		t.Fatalf("A16 later review %d %s", status, raw)
	}
	path := "/api/v1/exceptions?as_of=" + url.QueryEscape(later.Format(time.RFC3339))
	status, _, raw = s.call(t, http.MethodGet, path, "", authzHeader(sysTok))
	if status != http.StatusOK {
		t.Fatalf("A16 exceptions %d %s", status, raw)
	}
	var exceptions struct {
		Data []struct {
			Kind      string `json:"kind"`
			SubjectID string `json:"subject_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &exceptions); err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, ex := range exceptions.Data {
		if ex.Kind == "access_review_unconfirmed" {
			subjects = append(subjects, ex.SubjectID)
		}
	}
	if len(subjects) != 1 || subjects[0] != overdueID {
		t.Fatalf("A16 exceptions = %v, want [%s] body %s", subjects, overdueID, raw)
	}
}
