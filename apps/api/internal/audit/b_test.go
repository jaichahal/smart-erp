package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	kitaudit "github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func ensureSchema(t *testing.T, db *testdb.DB) {
	t.Helper()
	ctx := context.Background()
	var rel *string
	if err := db.Migrator.QueryRow(ctx, `SELECT to_regclass('erp.anchors')::text`).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	if rel != nil && *rel != "" {
		return
	}
	raw, err := migrations.FS.ReadFile("00004_anchors_and_backups.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	up := text
	if i := strings.Index(text, "-- +goose Up"); i >= 0 {
		up = text[i:]
	}
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	for _, stmt := range splitSQL(up) {
		if _, err := db.Migrator.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema: %v\n%s", err, stmt)
		}
	}
}

func splitSQL(s string) []string {
	var b strings.Builder
	var out []string
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(trim, ";") {
			stmt := strings.TrimSpace(b.String())
			stmt = strings.TrimSuffix(stmt, ";")
			out = append(out, strings.TrimSpace(stmt))
			b.Reset()
		}
	}
	return out
}

func newSvc(t *testing.T, db *testdb.DB, on, off ObjectStore) *Service {
	t.Helper()
	ensureSchema(t, db)
	client, err := outbox.NewClient(db.App, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	seed[0] = 7
	kms, err := NewLocalKMS("local-sim", seed)
	if err != nil {
		t.Fatal(err)
	}
	box := &captureMail{}
	return &Service{
		Pool: db.App, River: client, OnPrem: on, Offsite: off, KMS: kms, Site: "dev",
		Mail: box, StakeholderEmails: []string{"stakeholder@example.com"}, AuditorEmail: "auditor@example.com",
	}
}

func principal(roles ...string) rls.Principal {
	if len(roles) == 0 {
		roles = []string{"accountant"}
	}
	return rls.Principal{UserID: "user@example.com", CompanyID: uuid.New(), Roles: roles}
}

func emit(t *testing.T, db *testdb.DB, p rls.Principal) {
	t.Helper()
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		_, err := kitaudit.EmitAs(context.Background(), tx, p, kitaudit.Event{Type: "TEST", ReferenceType: "doc", ReferenceID: uuid.NewString()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupCompany(t *testing.T, db *testdb.DB, company uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER TABLE erp.audit_events DISABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.anchors DISABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.chain_head_log DISABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.verification_runs DISABLE TRIGGER immutable_guard`,
	} {
		if _, err := db.Admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`DELETE FROM erp.audit_events WHERE company_id=$1`,
		`DELETE FROM erp.anchors WHERE company_id=$1`,
		`DELETE FROM erp.chain_head_log WHERE company_id=$1`,
		`DELETE FROM erp.verification_runs WHERE company_id=$1`,
	} {
		if _, err := db.Admin.Exec(ctx, stmt, company); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE erp.audit_events ENABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.anchors ENABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.chain_head_log ENABLE TRIGGER immutable_guard`,
		`ALTER TABLE erp.verification_runs ENABLE TRIGGER immutable_guard`,
	} {
		if _, err := db.Admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func eventsOfType(t *testing.T, db *testdb.DB, typ string) []DomainEventArgs {
	t.Helper()
	rows, err := db.App.Query(context.Background(), `SELECT args FROM river_job WHERE kind='audit.domain_event' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []DomainEventArgs
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var args DomainEventArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatal(err)
		}
		var ev DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if typ == "" || ev.Type == typ {
			out = append(out, args)
		}
	}
	return out
}

type failStore struct{}

func (failStore) PutCompliance(context.Context, string, []byte, time.Time) error {
	return errors.New("offsite down")
}
func (failStore) Get(context.Context, string) ([]byte, error) { return nil, errors.New("offsite down") }
func (failStore) Delete(context.Context, string) error        { return errors.New("offsite down") }
func (failStore) Exists(context.Context, string) (bool, error) {
	return false, errors.New("offsite down")
}

func assertLocked(t *testing.T, store ObjectStore, key string) {
	t.Helper()
	ctx := context.Background()
	body := []byte("anchor-body")
	retain := time.Now().Add(24 * time.Hour)
	if err := store.PutCompliance(ctx, key, body, retain); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCompliance(ctx, key, []byte("overwrite"), retain); !errors.Is(err, ErrImmutableObject) {
		t.Fatalf("overwrite must fail, got %v", err)
	}
	if err := store.Delete(ctx, key); !errors.Is(err, ErrImmutableObject) {
		t.Fatalf("delete must fail, got %v", err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("locked object changed: %q %v", got, err)
	}
}

// B9: the hourly anchor is written to a compliance-mode bucket and the writer cannot delete or overwrite it.
func TestB9_ComplianceAnchorCannotBeDeletedOrOverwritten(t *testing.T) {
	assertLocked(t, newMemStore(), "anchors/b9/1.json")
	dir := t.TempDir()
	assertLocked(t, NewDirStore(dir), "anchors/b9/1.json")
	partialDir := dir + "/anchors/b9"
	if err := os.MkdirAll(partialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := partialDir + "/interrupted.json.partial"
	if err := os.WriteFile(partial, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/anchors/b9/interrupted.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a partial write must not become the object")
	}
	if os.Getenv("ERP_OFFSITE_S3_ENDPOINT") == "" {
		return
	}
	live := NewS3Client(os.Getenv("ERP_OFFSITE_S3_ENDPOINT"), os.Getenv("ERP_S3_REGION"), os.Getenv("ERP_OFFSITE_S3_ACCESS_KEY"), os.Getenv("ERP_OFFSITE_S3_SECRET_KEY"), os.Getenv("ERP_OFFSITE_S3_BUCKET"))
	key := "b2-tests/" + uuid.NewString() + ".json"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	version, err := live.PutLockedVersion(ctx, key, []byte("anchor-body"), time.Now().Add(24*time.Hour))
	if err != nil || version == "" {
		t.Fatalf("put version: %q %v", version, err)
	}
	if err := live.PutCompliance(ctx, key, []byte("overwrite"), time.Now().Add(24*time.Hour)); !errors.Is(err, ErrImmutableObject) {
		t.Fatalf("offsite overwrite must fail, got %v", err)
	}
	if err := live.DeleteVersion(ctx, key, version); !errors.Is(err, ErrImmutableObject) {
		t.Fatalf("deleting the locked version must fail, got %v", err)
	}
	got, err := live.GetVersion(ctx, key, version)
	if err != nil || string(got) != "anchor-body" {
		t.Fatalf("locked version must still be readable: %q %v", got, err)
	}
}

// B10: the anchor signature verifies with the KMS public key, and the database operator cannot Sign.
func TestB10_SignatureVerifiesAndOperatorCannotSign(t *testing.T) {
	seed := SeedFromString("b10-test-seed")
	kms, err := NewLocalKMS("local-sim", seed)
	if err != nil {
		t.Fatal(err)
	}
	core := anchorCore{CompanyID: uuid.NewString(), ChainSeq: 1, HeadHash: strings.Repeat("ab", 32), RowCount: 1, At: time.Now().UTC().Truncate(time.Second)}
	canonical, err := canon.Marshal(core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kms.Sign(IdentityDBOperator, canonical); !errors.Is(err, ErrSignDenied) {
		t.Fatalf("database operator Sign must be denied, got %v", err)
	}
	sig, err := kms.Sign(IdentityAnchorWorker, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !kms.Verify(canonical, sig) {
		t.Fatal("signature does not verify with the KMS public key")
	}
	canonical[0] ^= 0xff
	if kms.Verify(canonical, sig) {
		t.Fatal("a changed anchor must not verify")
	}
}

// B11: a recomputed head that differs from the anchored head is a break and a Critical quiet hours do not suppress.
func TestB11_AnchorMismatchIsCritical(t *testing.T) {
	db := testdb.New(t)
	on, off := newMemStore(), newMemStore()
	svc := newSvc(t, db, on, off)
	p := principal("auditor")
	for range 3 {
		emit(t, db, p)
	}
	ctx := context.Background()
	if err := svc.Anchor(ctx, p.CompanyID); err != nil {
		t.Fatal(err)
	}
	var id string
	var canonical, prev string
	if err := db.App.QueryRow(ctx, `SELECT id::text, canonical, prev_hash FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq DESC LIMIT 1`, p.CompanyID).Scan(&id, &canonical, &prev); err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(canonical, `"reason":""`, `"reason":"rewritten"`, 1)
	newHash := canon.Hash([]byte(rewritten), prev)
	if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events DISABLE TRIGGER immutable_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `UPDATE erp.audit_events SET canonical=$2, hash=$3 WHERE id=$1`, id, rewritten, newHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events ENABLE TRIGGER immutable_guard`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		emit(t, db, p)
	}
	res, err := svc.Verify(ctx, p.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intact || res.AnchoredHeadMatches || res.FirstBreak == nil || res.FirstBreak.ChainSeq != 3 {
		t.Fatalf("expected first break at seq 3: %+v", res)
	}
	if res.Count != 5 {
		t.Fatalf("verifier must count every row, got %d", res.Count)
	}
	if res.FirstBreak.Reason == nil || !strings.Contains(*res.FirstBreak.Reason, "anchored") && !strings.Contains(strings.ToLower(*res.FirstBreak.Reason), "anchor") {
		t.Fatalf("reason should name the anchor mismatch: %+v", res.FirstBreak)
	}
	var broken int
	for _, args := range eventsOfType(t, db, "chain.broken") {
		if !args.QuietHoursExempt {
			t.Fatal("chain.broken must be exempt from quiet hours")
		}
		var ev DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.CompanyID != p.CompanyID.String() {
			continue
		}
		if ev.Severity != "CRITICAL" {
			t.Fatalf("severity %s", ev.Severity)
		}
		if suppressed, _ := ev.Context["quiet_hours_suppressed"].(bool); suppressed {
			t.Fatal("quiet hours must not suppress the critical")
		}
		broken++
	}
	if broken == 0 {
		t.Fatal("expected a chain.broken event")
	}
	mail, ok := svc.Mail.(*captureMail)
	if !ok {
		t.Fatal("expected capture mailer")
	}
	if err := svc.SendDailyAnchorEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mail.msgs) != 1 || !strings.Contains(mail.msgs[0].Body, p.CompanyID.String()) {
		t.Fatalf("daily anchor email: %+v", mail.msgs)
	}
	cleanupCompany(t, db, p.CompanyID)
}
