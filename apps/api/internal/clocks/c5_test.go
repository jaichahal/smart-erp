package clocks

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
	"github.com/jaichahal/smart-erp/apps/api/migrations"
)

func newService(t *testing.T) (*testdb.DB, *Service, rls.Principal) {
	t.Helper()
	db := testdb.New(t)
	dsn := withDB(os.Getenv("ERP_MIGRATOR_DATABASE_URL"), db.Name)
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
	if err := outbox.Migrate(context.Background(), db.Migrator); err != nil {
		t.Fatalf("river migrate: %v", err)
	}
	client, err := outbox.NewClient(db.App, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	company := uuid.New()
	return db, New(db.App, client), rls.Principal{UserID: "user-" + company.String(), CompanyID: company, Roles: []string{"Accountant"}}
}

func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + strings.TrimPrefix(db, "/")
	return u.String()
}

func schemaDoc(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "contracts", "events", "event.schema.json")
		b, err := os.ReadFile(p)
		if err == nil {
			return b
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("event schema not found")
	return nil
}

// C5: a clock started at 16:00 on the day before the weekend with a 24-business-hour
// window is due at 16:00 on the second working day.
func TestC5(t *testing.T) {
	db, svc, p := newService(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	// Thursday 2026-09-24 is the day before a Friday–Saturday weekend.
	// 08:00–20:00 leaves 4 hours on Thursday, 12 on Sunday, and 8 until 16:00 Monday.
	saved, err := svc.SaveCalendar(ctx, p, 0, Calendar{
		Timezone: "Asia/Dubai", BusinessOpen: "08:00", BusinessClose: "20:00",
		Weekend: []string{"friday", "saturday"},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 24, 16, 0, 0, 0, loc)
	due, err := saved.Due(start, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second := secondWorkingDay(start, saved)
	want := time.Date(second.Year(), second.Month(), second.Day(), 16, 0, 0, 0, loc)
	if !due.Equal(want) {
		t.Fatalf("due = %s, want 16:00 on the second working day %s", due, want)
	}
	if due.In(loc).Hour() != 16 || due.In(loc).Minute() != 0 {
		t.Fatalf("clock time = %s, want 16:00", due.In(loc))
	}

	clock, err := svc.Start(ctx, p, StartInput{
		DocID: "dn-1", DocType: "delivery_note", Kind: KindDeliveryNote, StartedAt: start, Window: 24 * time.Hour,
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if !clock.DueAt.Equal(due) {
		t.Fatalf("stored due = %s, want %s", clock.DueAt, due)
	}
	if n, err := svc.ExpireDue(ctx, start); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("expired %d clocks before the due instant", n)
	}
	if _, err := svc.ExpireDue(ctx, due); err != nil {
		t.Fatal(err)
	}
	raw := clockEvent(t, db, clock.ID.String())
	if err := matchEventSchema(schemaDoc(t), raw); err != nil {
		t.Fatalf("clock.expired: %v\n%s", err, raw)
	}
	again := clockEventCount(t, db, clock.ID.String())
	if _, err := svc.ExpireDue(ctx, due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := clockEventCount(t, db, clock.ID.String()); got != again {
		t.Fatalf("second sweep emitted again: %d then %d", again, got)
	}
}

func secondWorkingDay(start time.Time, cal Calendar) time.Time {
	d := start.In(cal.loc)
	found := 0
	for i := 1; i <= 14 && found < 2; i++ {
		next := d.AddDate(0, 0, i)
		if cal.isBusiness(next) {
			found++
			if found == 2 {
				return next
			}
		}
	}
	return time.Time{}
}

func clockEvent(t *testing.T, db *testdb.DB, clockID string) []byte {
	t.Helper()
	var raw []byte
	err := db.App.QueryRow(context.Background(), `SELECT args FROM river_job WHERE kind = 'clock.expired' AND args->'context'->>'clock_id' = $1`, clockID).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func clockEventCount(t *testing.T, db *testdb.DB, clockID string) int {
	t.Helper()
	var n int
	err := db.App.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'clock.expired' AND args->'context'->>'clock_id' = $1`, clockID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEventSchemaExamples(t *testing.T) {
	doc := schemaDoc(t)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Dir(file)
	var examples string
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "contracts", "events", "examples")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			examples = p
			break
		}
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(examples)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(examples, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := matchEventSchema(doc, raw); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no event examples")
	}
}
