package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/app"
	"github.com/jaichahal/smart-erp/apps/api/internal/audit"
	kitaudit "github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// b2Env is the composed dev stack the acceptance tests talk to.
type b2Env struct {
	db  *testdb.DB
	svc *audit.Service
	srv *httptest.Server
	p   rls.Principal
}

func newB2(t *testing.T, roles ...string) *b2Env {
	t.Helper()
	requireB2Env(t)
	db := testdb.New(t)
	if len(roles) == 0 {
		roles = []string{"auditor", "stakeholder"}
	}
	p := rls.Principal{UserID: "b2-" + uuid.NewString(), CompanyID: uuid.New(), Roles: roles}
	river, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	cfg := b2Config()
	handler, err := app.Handler(httpx.Deps{
		Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), StartedAt: time.Now(), Config: cfg,
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := rls.WithPrincipal(r.Context(), p)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	svc := audit.NewFromDeps(httpx.Deps{Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), Config: cfg})
	if svc.KMS == nil {
		t.Fatal("anchor signing key was not loaded")
	}
	return &b2Env{db: db, svc: svc, srv: srv, p: p}
}

func requireB2Env(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ERP_S3_ENDPOINT", "ERP_S3_ACCESS_KEY", "ERP_S3_SECRET_KEY", "ERP_S3_BUCKET_ANCHORS", "ERP_OFFSITE_S3_ENDPOINT", "ERP_OFFSITE_S3_BUCKET"} {
		if os.Getenv(k) == "" {
			t.Fatalf("%s is required so B2 runs against the compose MinIO buckets", k)
		}
	}
	if os.Getenv("ERP_ANCHOR_SIGNING_SEED") == "" {
		t.Setenv("ERP_ANCHOR_SIGNING_SEED", "b2-e2e-signing-seed")
	}
	if os.Getenv("ERP_ANCHOR_KEY_ID") == "" {
		t.Setenv("ERP_ANCHOR_KEY_ID", "local-sim")
	}
	if os.Getenv("ERP_STAKEHOLDER_EMAILS") == "" {
		t.Setenv("ERP_STAKEHOLDER_EMAILS", "stakeholder@example.com")
	}
	if os.Getenv("ERP_AUDITOR_EMAIL") == "" {
		t.Setenv("ERP_AUDITOR_EMAIL", "auditor@example.com")
	}
	if os.Getenv("ERP_ENV") == "" {
		t.Setenv("ERP_ENV", "dev")
	}
}

func b2Config() *config.Config {
	return &config.Config{
		Env:                envOr("ERP_ENV", "dev"),
		S3Endpoint:         os.Getenv("ERP_S3_ENDPOINT"),
		S3AccessKey:        os.Getenv("ERP_S3_ACCESS_KEY"),
		S3SecretKey:        os.Getenv("ERP_S3_SECRET_KEY"),
		S3BucketFiles:      os.Getenv("ERP_S3_BUCKET_FILES"),
		S3BucketBackups:    os.Getenv("ERP_S3_BUCKET_BACKUPS"),
		S3BucketAnchors:    os.Getenv("ERP_S3_BUCKET_ANCHORS"),
		OffsiteS3Endpoint:  os.Getenv("ERP_OFFSITE_S3_ENDPOINT"),
		OffsiteS3AccessKey: envOr("ERP_OFFSITE_S3_ACCESS_KEY", os.Getenv("ERP_S3_ACCESS_KEY")),
		OffsiteS3SecretKey: envOr("ERP_OFFSITE_S3_SECRET_KEY", os.Getenv("ERP_S3_SECRET_KEY")),
		OffsiteS3Bucket:    os.Getenv("ERP_OFFSITE_S3_BUCKET"),
		SMTPAddr:           os.Getenv("ERP_SMTP_ADDR"),
		Version:            "dev",
		Commit:             "b2",
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (e *b2Env) call(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func (e *b2Env) emit(t *testing.T) {
	t.Helper()
	err := rls.Tx(context.Background(), e.db.App, e.p, func(tx pgx.Tx) error {
		_, err := kitaudit.EmitAs(context.Background(), tx, e.p, kitaudit.Event{
			Type: "TEST", ReferenceType: "doc", ReferenceID: uuid.NewString(),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *b2Env) verifyHTTP(t *testing.T) oapi.ChainVerification {
	t.Helper()
	status, raw := e.call(t, http.MethodPost, "/api/v1/audit/verify", `{}`)
	if status != http.StatusOK {
		t.Fatalf("verify status %d %s", status, raw)
	}
	var env struct {
		Data oapi.ChainVerification `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func (e *b2Env) statusHTTP(t *testing.T) oapi.StatusResponse {
	t.Helper()
	status, raw := e.call(t, http.MethodGet, "/api/v1/status", "")
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, raw)
	}
	var env struct {
		Data oapi.StatusResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("status json: %v %s", err, raw)
	}
	return env.Data
}

func (e *b2Env) jobs(t *testing.T, typ string) []audit.DomainEventArgs {
	t.Helper()
	rows, err := e.db.App.Query(context.Background(), `SELECT args FROM river_job WHERE kind='audit.domain_event' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []audit.DomainEventArgs
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var args audit.DomainEventArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatal(err)
		}
		var ev audit.DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if typ == "" || ev.Type == typ {
			out = append(out, args)
		}
	}
	return out
}

func (e *b2Env) anchorEveryone(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := e.db.App.Query(ctx, `SELECT company_id FROM erp.chain_head`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := e.svc.Anchor(ctx, id); err != nil {
			t.Fatalf("anchor %s: %v", id, err)
		}
	}
}

func tamperHash(t *testing.T, db *testdb.DB, company uuid.UUID, restore bool) (id, canonical, prev, hash string) {
	t.Helper()
	ctx := context.Background()
	if err := db.App.QueryRow(ctx, `SELECT id::text, canonical, prev_hash, hash FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq DESC LIMIT 1`, company).Scan(&id, &canonical, &prev, &hash); err != nil {
		t.Fatal(err)
	}
	if !restore {
		return id, canonical, prev, hash
	}
	origC, origH := canonical, hash
	t.Cleanup(func() {
		rewriteRow(t, db, id, origC, origH)
	})
	return id, canonical, prev, hash
}

func rewriteRow(t *testing.T, db *testdb.DB, id, canonical, hash string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events DISABLE TRIGGER immutable_guard`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events ENABLE TRIGGER immutable_guard`)
	}()
	if _, err := db.Admin.Exec(ctx, `UPDATE erp.audit_events SET canonical=$2, hash=$3 WHERE id=$1::uuid`, id, canonical, hash); err != nil {
		t.Fatal(err)
	}
}
