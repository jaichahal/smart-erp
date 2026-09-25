package authz_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/authz"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func newService(t *testing.T, db *testdb.DB) *authz.Service {
	t.Helper()
	client, err := outbox.NewClient(db.App, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	return authz.New(httpx.Deps{Pool: db.App, River: client})
}

func manager(company uuid.UUID) rls.Principal {
	return rls.Principal{UserID: "manager-" + company.String(), CompanyID: company, Roles: []string{"system_manager"}}
}

func codeOf(t *testing.T, err error) apierr.Code {
	t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("expected api error, got %v", err)
	}
	return ae.Code
}

type grant struct{ id string }

func (g grant) Granted(_ context.Context, approvalID, _ string, _ []string) (bool, error) {
	return approvalID == g.id, nil
}

func TestA11_SalesAgentCannotReadCostOrMargin(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db)
	ctx := context.Background()
	company := uuid.New()
	sales := rls.Principal{UserID: "sales", CompanyID: company, Roles: []string{"sales_agent"}}
	accountant := rls.Principal{UserID: "accountant", CompanyID: company, Roles: []string{"accountant"}}

	doc := map[string]any{
		"sku":    "FG-1",
		"price":  "12.00",
		"cost":   "COST-SENTINEL",
		"margin": "MARGIN-SENTINEL",
		"pricing": map[string]any{
			"name":          "Panel",
			"unit_cost":     "COST-SENTINEL",
			"margin_amount": "MARGIN-SENTINEL",
		},
		"lines": []any{
			map[string]any{"qty": "2", "total_cost": "COST-SENTINEL", "gross_margin": "MARGIN-SENTINEL"},
		},
	}
	export := []any{
		map[string]any{"name": "Panel", "line_cost": "COST-SENTINEL", "margin_percent": "MARGIN-SENTINEL"},
	}

	for _, payload := range []any{doc, export} {
		redacted, err := svc.Redact(ctx, sales, payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(redacted)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if bytes.Contains(raw, []byte("COST-SENTINEL")) || bytes.Contains(raw, []byte("MARGIN-SENTINEL")) || bytes.Contains(raw, []byte("unit_cost")) || bytes.Contains(raw, []byte("gross_margin")) {
			t.Fatalf("sales agent payload still exposes cost or margin: %s", body)
		}
		kept, err := svc.Redact(ctx, accountant, payload)
		if err != nil {
			t.Fatal(err)
		}
		keptRaw, err := json.Marshal(kept)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(keptRaw, []byte("COST-SENTINEL")) || !bytes.Contains(keptRaw, []byte("MARGIN-SENTINEL")) {
			t.Fatalf("accountant lost cost or margin: %s", keptRaw)
		}
	}
}

func TestA12_TerritoryCountMatchesPages(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db)
	ctx := context.Background()
	company := uuid.New()
	otherCompany := uuid.New()
	territory := uuid.New()
	elsewhere := uuid.New()
	salesID := "sales-" + company.String()
	if err := svc.CreateUser(ctx, manager(company), authz.NewUser{
		ID: salesID, Name: "Sales Agent One", TerritoryID: &territory,
	}); err != nil {
		t.Fatal(err)
	}
	owner := "someone-else"
	for i := 0; i < 5; i++ {
		insertCustomer(t, db, company, territory, owner, "home-"+uuid.NewString())
	}
	for i := 0; i < 3; i++ {
		insertCustomer(t, db, company, elsewhere, owner, "away-"+uuid.NewString())
	}
	for i := 0; i < 2; i++ {
		insertCustomer(t, db, otherCompany, territory, owner, "other-"+uuid.NewString())
	}

	sales := rls.Principal{UserID: salesID, CompanyID: company, Roles: []string{"sales_agent"}}
	seen := pageAllCustomers(t, svc, sales, 2)
	if seen.total != 5 {
		t.Fatalf("total = %d, want 5 (territory only)", seen.total)
	}
	if seen.rows != seen.total {
		t.Fatalf("rows across pages %d, total %d", seen.rows, seen.total)
	}

	accountant := rls.Principal{UserID: "acct-" + company.String(), CompanyID: company, Roles: []string{"accountant"}}
	all := pageAllCustomers(t, svc, accountant, 3)
	if all.total != 8 || all.rows != all.total {
		t.Fatalf("accountant rows %d total %d, want 8 and equal", all.rows, all.total)
	}
}

type walked struct{ rows, total int }

func pageAllCustomers(t *testing.T, svc *authz.Service, actor rls.Principal, limit int) walked {
	t.Helper()
	var cursor string
	var out walked
	pages := 0
	for {
		pages++
		if pages > 20 {
			t.Fatal("pagination did not end")
		}
		page, err := svc.ListCustomers(context.Background(), actor, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		if pages == 1 {
			out.total = page.Total
		} else if page.Total != out.total {
			t.Fatalf("total changed from %d to %d", out.total, page.Total)
		}
		out.rows += len(page.Rows)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	return out
}

func insertCustomer(t *testing.T, db *testdb.DB, company, territory uuid.UUID, owner, name string) {
	t.Helper()
	_, err := db.Migrator.Exec(context.Background(), `
		INSERT INTO erp.customers (id, company_id, territory_id, owner_id, name)
		VALUES ($1, $2, $3, $4, $5)`, uuid.New(), company, territory, owner, name)
	if err != nil {
		t.Fatal(err)
	}
}

func TestA13_UnknownRoleLeastPrivileged(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db)
	ctx := context.Background()
	company := uuid.New()

	unknown, err := svc.ResolveExperience(ctx, rls.Principal{UserID: "x", CompanyID: company, Roles: []string{"visiting_wizard"}})
	if err != nil {
		t.Fatal(err)
	}
	if !unknown.LeastPrivileged() {
		t.Fatalf("personas = %v, want least_privileged", unknown.Personas)
	}
	if len(unknown.PrivilegedTabs) != 0 || len(unknown.PrivilegedEndpoints) != 0 {
		t.Fatalf("unknown role gained privileged access: tabs %v endpoints %v", unknown.PrivilegedTabs, unknown.PrivilegedEndpoints)
	}
	if unknown.Allows("GET", "/status") || unknown.Allows("POST", "/sod-matrix") || unknown.Allows("GET", "/customers") {
		t.Fatal("unknown role was guessed into a privileged or sales endpoint")
	}

	none, err := svc.ResolveExperience(ctx, rls.Principal{UserID: "n", CompanyID: company})
	if err != nil {
		t.Fatal(err)
	}
	if !none.LeastPrivileged() || none.Allows("GET", "/customers") {
		t.Fatalf("missing role personas %v", none.Personas)
	}

	mixed, err := svc.ResolveExperience(ctx, rls.Principal{UserID: "m", CompanyID: company, Roles: []string{"sales_agent", "visiting_wizard"}})
	if err != nil {
		t.Fatal(err)
	}
	if mixed.LeastPrivileged() || !mixed.Allows("GET", "/customers") || mixed.Allows("GET", "/status") {
		t.Fatalf("recognised role was dropped or escalated: %+v", mixed)
	}

	boss, err := svc.ResolveExperience(ctx, manager(company))
	if err != nil {
		t.Fatal(err)
	}
	if !boss.Allows("GET", "/status") || !boss.Allows("POST", "/sod-matrix") || len(boss.PrivilegedTabs) == 0 {
		t.Fatalf("system manager lost privileged access: %+v", boss)
	}
}

func TestA14_IncompatibleRolesNeedOverrideAndAudit(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db).WithOverrideApprover(grant{id: "apr-14"})
	ctx := context.Background()
	company := uuid.New()
	actor := manager(company)
	userID := "user-" + company.String()
	if err := svc.CreateUser(ctx, actor, authz.NewUser{ID: userID, Name: "Pair User"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignRoles(ctx, actor, userID, []string{"sales_agent"}, ""); err != nil {
		t.Fatal(err)
	}
	err := svc.AssignRoles(ctx, actor, userID, []string{"sales_agent", "approver"}, "")
	if codeOf(t, err) != apierr.SoDViolation {
		t.Fatalf("code = %s, want SOD_VIOLATION", codeOf(t, err))
	}
	var refusals int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE reference_id = $1 AND event_type = 'sod.assignment_refused'`, userID).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if refusals != 1 {
		t.Fatalf("refusal audits = %d, want 1", refusals)
	}
	got, err := svc.ResolveUser(ctx, actor, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "sales_agent" {
		t.Fatalf("roles after refusal = %v", got.Roles)
	}
	if err := svc.AssignRoles(ctx, actor, userID, []string{"approver", "sales_agent"}, "apr-14"); err != nil {
		t.Fatal(err)
	}
	got, err = svc.ResolveUser(ctx, actor, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Roles) != 2 {
		t.Fatalf("roles after override = %v", got.Roles)
	}
	var changes int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE reference_id = $1 AND event_type = 'role.changed'`, userID).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes < 2 {
		t.Fatalf("role.changed audits = %d, want at least 2", changes)
	}
	var jobs int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'role.changed' AND args->>'user_id' = $1`, userID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs < 1 {
		t.Fatalf("role.changed jobs = %d", jobs)
	}
}

func TestA15_DisabledUserResolvableCannotAuthenticate(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db)
	ctx := context.Background()
	company := uuid.New()
	actor := manager(company)
	userID := "user-" + company.String()
	if err := svc.CreateUser(ctx, actor, authz.NewUser{ID: userID, Name: "Still Here"}); err != nil {
		t.Fatal(err)
	}
	ok, err := svc.CanAuthenticate(ctx, userID)
	if err != nil || !ok {
		t.Fatalf("enabled authenticate = %v %v", ok, err)
	}
	if err := svc.DisableUser(ctx, actor, userID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ResolveUser(ctx, actor, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Disabled || got.Name != "Still Here" {
		t.Fatalf("resolved %+v", got)
	}
	ok, err = svc.CanAuthenticate(ctx, userID)
	if err != nil || ok {
		t.Fatalf("disabled authenticate = %v %v", ok, err)
	}
	if _, err := db.App.Exec(ctx, `DELETE FROM erp.users WHERE id = $1`, userID); err == nil {
		t.Fatal("application role deleted a user")
	}
	if _, err := db.Migrator.Exec(ctx, `DELETE FROM erp.users WHERE id = $1`, userID); err == nil {
		t.Fatal("owner role deleted a user")
	}
	got, err = svc.ResolveUser(ctx, actor, userID)
	if err != nil || got.Name != "Still Here" {
		t.Fatalf("user missing after delete attempts: %+v %v", got, err)
	}
}

func TestA16_UnconfirmedAccessReviewOnExceptions(t *testing.T) {
	db := testdb.New(t)
	svc := newService(t, db)
	ctx := context.Background()
	company := uuid.New()
	actor := manager(company)
	asOf := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	login := asOf.Add(-48 * time.Hour)
	confirmedID := "confirmed-" + company.String()
	overdueID := "overdue-" + company.String()
	for _, id := range []string{confirmedID, overdueID} {
		if err := svc.CreateUser(ctx, actor, authz.NewUser{ID: id, Name: id, LastLogin: &login}); err != nil {
			t.Fatal(err)
		}
		if err := svc.AssignRoles(ctx, actor, id, []string{"sales_agent"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	worker := rls.System
	worker.CompanyID = company
	review, err := svc.RunScheduledAccessReview(ctx, worker, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Items) != 2 {
		t.Fatalf("review items = %d, want 2", len(review.Items))
	}
	for _, item := range review.Items {
		if len(item.Roles) != 1 || item.Roles[0] != "sales_agent" {
			t.Fatalf("item roles = %v", item.Roles)
		}
		if item.LastLogin == nil || !item.LastLogin.Equal(login) {
			t.Fatalf("last login = %v, want %v", item.LastLogin, login)
		}
	}
	stakeholder := rls.Principal{UserID: "stakeholder-" + company.String(), CompanyID: company, Roles: []string{"stakeholder"}}
	if err := svc.ConfirmAccessReview(ctx, stakeholder, review.ID, confirmedID); err != nil {
		t.Fatal(err)
	}
	later := asOf.Add(15 * 24 * time.Hour)
	if _, err := svc.RunScheduledAccessReview(ctx, worker, later); err != nil {
		t.Fatal(err)
	}
	exceptions, err := svc.ListExceptions(ctx, worker, later)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, ex := range exceptions {
		if ex.Kind == "access_review_unconfirmed" {
			subjects = append(subjects, ex.SubjectID)
		}
	}
	if len(subjects) != 1 || subjects[0] != overdueID {
		t.Fatalf("exceptions = %v, want [%s]", subjects, overdueID)
	}
}

func TestMatrixHTTP(t *testing.T) {
	db := testdb.New(t)
	company := uuid.New()
	boss := manager(company)
	sales := rls.Principal{UserID: "sales-http", CompanyID: company, Roles: []string{"sales_agent"}}

	srv := httptest.NewServer(router(db, boss))
	t.Cleanup(srv.Close)
	denied := httptest.NewServer(router(db, sales))
	t.Cleanup(denied.Close)

	res, err := postJSON(denied.URL+"/sod-matrix", map[string]string{
		"kind": "role_pair", "left_code": "auditor", "right_code": "stakeholder",
	}, "ar")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("sales status = %d", res.StatusCode)
	}
	var env struct {
		Error struct{ Code, Message string }
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if env.Error.Code != "PERMISSION_DENIED" || env.Error.Message == "" || env.Error.Message == "permission.denied" {
		t.Fatalf("error = %+v", env.Error)
	}

	created, err := postJSON(srv.URL+"/sod-matrix", map[string]string{
		"kind": "role_pair", "left_code": "auditor", "right_code": "stakeholder",
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if created.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create status = %d body %s", created.StatusCode, body)
	}
	var okBody struct {
		Data oapi.SodRule
	}
	if err := json.NewDecoder(created.Body).Decode(&okBody); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if okBody.Data.Status != oapi.SodRuleStatusPendingApproval || okBody.Data.StateVersion != 1 {
		t.Fatalf("created = %+v", okBody.Data)
	}

	matrix, err := postJSON(srv.URL+"/approval-matrix", map[string]any{
		"document_type":        "sales_invoice",
		"threshold":            map[string]string{"amount": "1000.00", "currency": "AED"},
		"below_threshold_role": "approver",
		"first_approver_role":  "accountant",
		"final_gate_role":      "stakeholder",
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if matrix.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(matrix.Body)
		t.Fatalf("matrix status = %d body %s", matrix.StatusCode, body)
	}
	matrix.Body.Close()

	stale, err := http.NewRequest(http.MethodPost, srv.URL+"/approval-matrix", bytes.NewReader([]byte(
		`{"document_type":"sales_invoice","threshold":{"amount":"2000.00","currency":"AED"},"below_threshold_role":"approver","first_approver_role":"accountant","final_gate_role":"stakeholder"}`)))
	if err != nil {
		t.Fatal(err)
	}
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("If-Match", "9")
	stale.Header.Set("Idempotency-Key", uuid.NewString())
	staleRes, err := srv.Client().Do(stale)
	if err != nil {
		t.Fatal(err)
	}
	defer staleRes.Body.Close()
	if staleRes.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(staleRes.Body)
		t.Fatalf("stale status = %d body %s", staleRes.StatusCode, body)
	}
}

func router(db *testdb.DB, actor rls.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), actor)))
		})
	})
	authz.Mount(r, httpx.Deps{Pool: db.App})
	return r
}

func postJSON(url string, body any, lang string) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", lang)
	req.Header.Set("If-Match", "0")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	return http.DefaultClient.Do(req)
}
