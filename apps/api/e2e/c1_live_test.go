package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/clocks"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	periods "github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

// c1API is the periods and clocks HTTP API on the real Postgres database.
// Identity owns bearer tokens; this harness sets the principal the same way
// the auth middleware does, and does not change identity.
type c1API struct {
	db  *testdb.DB
	srv *httptest.Server
}

type c1Allow struct{}

func (c1Allow) Approved(context.Context, uuid.UUID, uuid.UUID, string) error { return nil }

func newC1API(t *testing.T) *c1API {
	t.Helper()
	db := testdb.New(t)
	c1Migrate(t, db)
	river, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	deps := httpx.Deps{Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), StartedAt: time.Now()}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			raw := req.Header.Get("X-Company-Id")
			if raw == "" {
				next.ServeHTTP(w, req)
				return
			}
			company, err := uuid.Parse(raw)
			if err != nil {
				t.Errorf("company header: %v", err)
				next.ServeHTTP(w, req)
				return
			}
			roles := []string{}
			if got := req.Header.Get("X-Roles"); got != "" {
				roles = splitCSV(got)
			}
			p := rls.Principal{UserID: req.Header.Get("X-User-Id"), CompanyID: company, Roles: roles}
			next.ServeHTTP(w, req.WithContext(rls.WithPrincipal(req.Context(), p)))
		})
	})
	r.Route("/api/v1", func(v1 chi.Router) {
		periods.Mount(v1, deps, c1Allow{})
		clocks.Mount(v1, deps)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &c1API{db: db, srv: srv}
}

func splitCSV(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if part := s[start:i]; part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func (a *c1API) call(t *testing.T, method, path, body string, hdr map[string]string) (int, string, []byte) {
	t.Helper()
	status, code, raw, err := a.do(method, path, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	return status, code, raw
}

func (a *c1API) do(method, path, body string, hdr map[string]string) (int, string, []byte, error) {
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, a.srv.URL+path, rdr)
	if err != nil {
		return 0, "", nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && hdr["Idempotency-Key"] == "" {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, errorCode(raw), raw, nil
}

func (a *c1API) as(p rls.Principal) map[string]string {
	roles := ""
	for i, role := range p.Roles {
		if i > 0 {
			roles += ","
		}
		roles += role
	}
	return map[string]string{
		"X-User-Id": p.UserID, "X-Company-Id": p.CompanyID.String(), "X-Roles": roles,
	}
}

func c1Principal(roles ...string) rls.Principal {
	company := uuid.New()
	return rls.Principal{UserID: "user-" + company.String(), CompanyID: company, Roles: roles}
}

func (a *c1API) company(t *testing.T, p rls.Principal, name string) {
	t.Helper()
	status, code, raw := a.call(t, http.MethodPost, "/api/v1/companies", `{"legal_name":"`+name+`"}`, a.as(p))
	if status != http.StatusOK {
		t.Fatalf("company %d %s %s", status, code, raw)
	}
}

func c1Migrate(t *testing.T, db *testdb.DB) {
	t.Helper()
	dsn := c1WithDB(os.Getenv("ERP_MIGRATOR_DATABASE_URL"), db.Name)
	sqldb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(sqldb, "."); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func c1WithDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(db, "/")
	return u.String()
}

func (a *c1API) docNumber(t *testing.T, company uuid.UUID, docType, docID string) int64 {
	t.Helper()
	var n int64
	err := a.db.Admin.QueryRow(context.Background(), `SELECT number FROM erp.registered_documents
		WHERE company_id = $1 AND doc_type = $2 AND doc_id = $3`, company, docType, docID).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

func (a *c1API) numbers(t *testing.T, company uuid.UUID, voids bool) []int64 {
	t.Helper()
	q := `SELECT number FROM erp.number_allocations WHERE company_id = $1 ORDER BY number`
	if voids {
		q = `SELECT number FROM erp.number_voids WHERE company_id = $1 ORDER BY number`
	}
	rows, err := a.db.Admin.Query(context.Background(), q, company)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// C1: a number is allocated only inside a successful registration. A failed
// registration records a void and the next number continues.
func TestC1_AllocationThroughAPI(t *testing.T) {
	api := newC1API(t)
	p := c1Principal(periods.RoleAccountant)
	api.company(t, p, "base")
	hdr := api.as(p)
	okStatus, _, raw := api.call(t, http.MethodPost, "/api/v1/periods/registrations",
		`{"doc_type":"sales_invoice","doc_id":"doc-ok-1","fiscal_year":2026,"commit":true}`, hdr)
	if okStatus != http.StatusOK {
		t.Fatalf("success %d %s", okStatus, raw)
	}
	var ok struct {
		Data struct {
			Number int64 `json:"number"`
			Voided bool  `json:"voided"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		t.Fatal(err)
	}
	if ok.Data.Voided || ok.Data.Number != 1 {
		t.Fatalf("first allocation %+v", ok.Data)
	}
	if n := api.docNumber(t, p.CompanyID, "sales_invoice", "doc-ok-1"); n != 1 {
		t.Fatalf("committed document number = %d, want 1", n)
	}

	failStatus, failCode, failRaw := api.call(t, http.MethodPost, "/api/v1/periods/registrations",
		`{"doc_type":"sales_invoice","doc_id":"doc-fail","fiscal_year":2026,"commit":false}`, hdr)
	if failStatus == http.StatusOK || failCode == "" {
		t.Fatalf("failed registration %d %s %s", failStatus, failCode, failRaw)
	}
	if n := api.docNumber(t, p.CompanyID, "sales_invoice", "doc-fail"); n != 0 {
		t.Fatalf("failed registration kept document number %d", n)
	}
	if got := api.numbers(t, p.CompanyID, true); len(got) != 1 || got[0] != 2 {
		t.Fatalf("voids = %v, want [2]", got)
	}

	nextStatus, _, nextRaw := api.call(t, http.MethodPost, "/api/v1/periods/registrations",
		`{"doc_type":"sales_invoice","doc_id":"doc-ok-2","fiscal_year":2026,"commit":true}`, hdr)
	if nextStatus != http.StatusOK {
		t.Fatalf("next %d %s", nextStatus, nextRaw)
	}
	var next struct {
		Data struct {
			Number int64 `json:"number"`
			Voided bool  `json:"voided"`
		} `json:"data"`
	}
	if err := json.Unmarshal(nextRaw, &next); err != nil {
		t.Fatal(err)
	}
	if next.Data.Voided || next.Data.Number != 3 {
		t.Fatalf("continued = %+v", next.Data)
	}
	if n := api.docNumber(t, p.CompanyID, "sales_invoice", "doc-ok-2"); n != 3 {
		t.Fatalf("continued document number = %d, want 3", n)
	}
	if got := api.numbers(t, p.CompanyID, false); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("allocations = %v", got)
	}
}

// C2: two concurrent registrations of the same document type receive distinct
// consecutive numbers.
func TestC2_ConcurrentRegistrations(t *testing.T) {
	api := newC1API(t)
	p := c1Principal(periods.RoleAccountant)
	api.company(t, p, "concurrent")
	hdr := api.as(p)
	var wg sync.WaitGroup
	nums := make([]int64, 2)
	errs := make([]string, 2)
	start := make(chan struct{})
	for i, doc := range []string{"a", "b"} {
		wg.Add(1)
		go func(i int, doc string) {
			defer wg.Done()
			<-start
			status, code, raw, err := api.do(http.MethodPost, "/api/v1/periods/registrations",
				`{"doc_type":"sales_invoice","doc_id":"`+doc+`","fiscal_year":2026,"commit":true}`, hdr)
			if err != nil {
				errs[i] = err.Error()
				return
			}
			if status != http.StatusOK {
				errs[i] = code + " " + string(raw)
				return
			}
			var env struct {
				Data struct {
					Number int64 `json:"number"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				errs[i] = err.Error()
				return
			}
			nums[i] = env.Data.Number
		}(i, doc)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != "" {
			t.Fatalf("request %d: %s", i, err)
		}
	}
	if nums[0] == nums[1] || (nums[0] != 1 && nums[0] != 2) || (nums[1] != 1 && nums[1] != 2) {
		t.Fatalf("numbers = %v, want 1 and 2", nums)
	}
}

// C3: a posting into a hard-closed period is refused with PERIOD_CLOSED.
func TestC3_PostingIntoHardClosedPeriod(t *testing.T) {
	api := newC1API(t)
	accountant := c1Principal(periods.RoleAccountant)
	api.company(t, accountant, "close-co")
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	today := now.Format("2006-01-02")
	hdr := api.as(accountant)
	status, code, raw := api.call(t, http.MethodPost, "/api/v1/fiscal-years",
		`{"year":`+itoa(now.Year())+`,"start":"`+start+`"}`, hdr)
	if status != http.StatusOK {
		t.Fatalf("year %d %s %s", status, code, raw)
	}
	status, code, raw = api.call(t, http.MethodGet, "/api/v1/periods?on="+today, "", hdr)
	if status != http.StatusOK {
		t.Fatalf("period %d %s %s", status, code, raw)
	}
	var period struct {
		Data struct {
			ID           string `json:"id"`
			StateVersion int64  `json:"state_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &period); err != nil || period.Data.ID == "" {
		t.Fatalf("period body %s", raw)
	}
	stakeholder := accountant
	stakeholder.Roles = []string{periods.RoleStakeholder}
	status, code, raw = api.call(t, http.MethodPost, "/api/v1/periods/"+period.Data.ID+"/hard-close",
		`{"approval_id":"approval-1"}`, map[string]string{
			"X-User-Id": stakeholder.UserID, "X-Company-Id": stakeholder.CompanyID.String(),
			"X-Roles": periods.RoleStakeholder, "If-Match": itoa64(period.Data.StateVersion),
		})
	if status != http.StatusOK {
		t.Fatalf("hard close %d %s %s", status, code, raw)
	}
	status, code, raw = api.call(t, http.MethodPost, "/api/v1/periods/postings",
		`{"posting_date":"`+today+`","doc_type":"sales_invoice","doc_id":"inv-closed"}`, hdr)
	if code != "PERIOD_CLOSED" {
		t.Fatalf("posting %d code %s %s, want PERIOD_CLOSED", status, code, raw)
	}
}

// C4: posting into a prior open period without the back-dating permission is
// refused. With the permission, it appears on the exceptions report.
func TestC4_BackdatingPermission(t *testing.T) {
	api := newC1API(t)
	accountant := c1Principal(periods.RoleAccountant)
	api.company(t, accountant, "backdate-co")
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	prior := start.Format("2006-01-02")
	hdr := api.as(accountant)
	status, code, raw := api.call(t, http.MethodPost, "/api/v1/fiscal-years",
		`{"year":`+itoa(start.Year())+`,"start":"`+start.Format("2006-01-02")+`"}`, hdr)
	if status != http.StatusOK {
		t.Fatalf("year %d %s %s", status, code, raw)
	}
	status, code, raw = api.call(t, http.MethodPost, "/api/v1/periods/postings",
		`{"posting_date":"`+prior+`","doc_type":"sales_invoice","doc_id":"inv-backdated"}`, hdr)
	if code != "PERMISSION_DENIED" {
		t.Fatalf("without permission %d %s %s", status, code, raw)
	}
	month := now.Format("2006-01")
	status, _, raw = api.call(t, http.MethodGet, "/api/v1/exceptions?month="+month, "", hdr)
	if status != http.StatusOK {
		t.Fatalf("exceptions %d %s", status, raw)
	}
	if bytes.Contains(raw, []byte("inv-backdated")) {
		t.Fatalf("refused posting appeared on the report: %s", raw)
	}
	backdater := accountant
	backdater.Roles = []string{periods.RoleBackdate}
	status, code, raw = api.call(t, http.MethodPost, "/api/v1/periods/postings",
		`{"posting_date":"`+prior+`","doc_type":"sales_invoice","doc_id":"inv-backdated"}`, api.as(backdater))
	if status != http.StatusOK {
		t.Fatalf("with permission %d %s %s", status, code, raw)
	}
	status, _, raw = api.call(t, http.MethodGet, "/api/v1/exceptions?month="+month, "", api.as(backdater))
	if status != http.StatusOK || !bytes.Contains(raw, []byte("inv-backdated")) || !bytes.Contains(raw, []byte("backdated_posting")) {
		t.Fatalf("exceptions = %s", raw)
	}
}

// C5: a clock started at 16:00 on the day before the weekend with a
// 24-business-hour window is due at 16:00 on the second working day.
func TestC5_ClockDueSecondWorkingDay(t *testing.T) {
	api := newC1API(t)
	p := c1Principal(periods.RoleAccountant)
	hdr := api.as(p)
	status, code, raw := api.call(t, http.MethodPost, "/api/v1/holiday-calendar",
		`{"timezone":"Asia/Dubai","business_open":"08:00","business_close":"20:00","weekend":["friday","saturday"],"holidays":[]}`,
		merge(hdr, map[string]string{"If-Match": "0"}))
	if status != http.StatusOK {
		t.Fatalf("calendar %d %s %s", status, code, raw)
	}
	loc, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 24, 16, 0, 0, 0, loc)
	status, code, raw = api.call(t, http.MethodPost, "/api/v1/clocks",
		`{"doc_id":"dn-1","doc_type":"delivery_note","kind":"delivery_note","started_at":"`+start.Format(time.RFC3339)+`","window_seconds":86400}`, hdr)
	if status != http.StatusOK {
		t.Fatalf("clock %d %s %s", status, code, raw)
	}
	var env struct {
		Data struct {
			DueAt time.Time `json:"due_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 28, 16, 0, 0, 0, loc)
	if !env.Data.DueAt.Equal(want) {
		t.Fatalf("due = %s, want 16:00 on the second working day %s", env.Data.DueAt, want)
	}
	got := env.Data.DueAt.In(loc)
	if got.Hour() != 16 || got.Minute() != 0 {
		t.Fatalf("clock time = %s, want 16:00", got)
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func itoa64(n int64) string { return itoa(int(n)) }
