package audit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func principal() rls.Principal {
	return rls.Principal{UserID: "accountant@example.com", CompanyID: uuid.New(), Roles: []string{"accountant"}}
}

func emit(t *testing.T, db *testdb.DB, p rls.Principal, ev audit.Event) *audit.Appended {
	t.Helper()
	var out *audit.Appended
	err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
		a, err := audit.EmitAs(context.Background(), tx, p, ev)
		out = a
		return err
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	return out
}

// B1: UPDATE or DELETE on an immutable table by the application role fails at the database.
func TestB1_ImmutableRejectsUpdateAndDelete(t *testing.T) {
	db := testdb.New(t)
	p := principal()
	a := emit(t, db, p, audit.Event{Type: "TEST", ReferenceType: "doc", ReferenceID: "1"})
	ctx := context.Background()

	for _, stmt := range []string{
		`UPDATE erp.audit_events SET reason = 'tampered' WHERE id = $1`,
		`DELETE FROM erp.audit_events WHERE id = $1`,
	} {
		_, err := db.App.Exec(ctx, stmt, a.ID)
		if err == nil {
			t.Fatalf("app role must not be able to run %q", stmt)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "42501" && !strings.Contains(pgErr.Message, "immutable")) {
			t.Fatalf("expected privilege or immutable error, got %v", err)
		}
	}
	// Even the owner role is stopped by the trigger (grants are the first layer, triggers the second).
	for _, stmt := range []string{
		`UPDATE erp.audit_events SET reason = 'tampered' WHERE id = $1`,
		`DELETE FROM erp.audit_events WHERE id = $1`,
	} {
		_, err := db.Migrator.Exec(ctx, stmt, a.ID)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("owner role must be stopped by the trigger for %q, got %v", stmt, err)
		}
	}
	if _, err := db.Migrator.Exec(ctx, `TRUNCATE erp.audit_events`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("truncate must be refused, got %v", err)
	}
	// The row is still there, unchanged.
	var reason string
	if err := db.App.QueryRow(ctx, `SELECT reason FROM erp.audit_events WHERE id=$1`, a.ID).Scan(&reason); err != nil || reason != "" {
		t.Fatalf("row altered or missing: %q %v", reason, err)
	}
}

// B4: hash equals sha256(canonical || prev_hash) for every row, and the chain links.
func TestB4_HashDefinitionAndLinking(t *testing.T) {
	db := testdb.New(t)
	p := principal()
	first := emit(t, db, p, audit.Event{Type: "INITIATED", ReferenceType: "invoice", ReferenceID: "INV-1", After: map[string]any{"amount": "100.00"}})
	second := emit(t, db, p, audit.Event{Type: "APPROVED", ReferenceType: "invoice", ReferenceID: "INV-1"})

	if first.PrevHash != "" || first.ChainSeq != 1 {
		t.Fatalf("first event must have empty prev and seq 1: %+v", first)
	}
	if second.PrevHash != first.Hash || second.ChainSeq != 2 {
		t.Fatalf("second must link to first: %+v", second)
	}
	for _, a := range []*audit.Appended{first, second} {
		if canon.Hash([]byte(a.Canonical), a.PrevHash) != a.Hash {
			t.Fatalf("hash != sha256(canonical||prev) for seq %d", a.ChainSeq)
		}
		if strings.Contains(a.Canonical, "prev_hash") || strings.Contains(a.Canonical, `"hash"`) {
			t.Fatalf("canonical must exclude chain fields: %s", a.Canonical)
		}
	}
	// The stored canonical is exactly what the DB hashed: chain_head agrees.
	var head string
	if err := db.App.QueryRow(context.Background(), `SELECT head_hash FROM erp.chain_head WHERE company_id=$1`, p.CompanyID).Scan(&head); err != nil || head != second.Hash {
		t.Fatalf("chain head mismatch: %s vs %s (%v)", head, second.Hash, err)
	}
	res, err := audit.Verify(context.Background(), db.App, p.CompanyID)
	if err != nil || !res.Intact || res.Count != 2 {
		t.Fatalf("verify: %+v %v", res, err)
	}
}

// B5: concurrent appends produce a linear chain with distinct sequences and no shared prev_hash.
func TestB5_ConcurrentAppendsDoNotFork(t *testing.T) {
	db := testdb.New(t)
	p := principal()
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := rls.Tx(context.Background(), db.App, p, func(tx pgx.Tx) error {
				_, err := audit.EmitAs(context.Background(), tx, p, audit.Event{Type: "CONCURRENT", ReferenceID: string(rune('a' + i%26))})
				return err
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append failed: %v", err)
	}
	rows, err := db.App.Query(context.Background(), `SELECT chain_seq, prev_hash, hash FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq`, p.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seenPrev := map[string]bool{}
	expect := int64(1)
	prev := ""
	for rows.Next() {
		var seq int64
		var ph, h string
		if err := rows.Scan(&seq, &ph, &h); err != nil {
			t.Fatal(err)
		}
		if seq != expect {
			t.Fatalf("gap or duplicate at seq %d (expected %d)", seq, expect)
		}
		if ph != prev {
			t.Fatalf("seq %d prev_hash %s does not equal previous hash %s", seq, ph, prev)
		}
		if seenPrev[ph] && ph != "" {
			t.Fatalf("fork: prev_hash %s used twice", ph)
		}
		seenPrev[ph] = true
		prev = h
		expect++
	}
	if expect-1 != n {
		t.Fatalf("expected %d rows, got %d", n, expect-1)
	}
	res, err := audit.Verify(context.Background(), db.App, p.CompanyID)
	if err != nil || !res.Intact {
		t.Fatalf("chain not intact after concurrent appends: %+v %v", res, err)
	}
}

// B6/B7: direct tampering is detected, reported at the first break, and does not cascade.
func TestB6_TamperDetectedWithoutCascade(t *testing.T) {
	db := testdb.New(t)
	p := principal()
	var events []*audit.Appended
	for i := 0; i < 5; i++ {
		events = append(events, emit(t, db, p, audit.Event{Type: "E", ReferenceID: string(rune('0' + i))}))
	}
	ctx := context.Background()
	// Simulate a hostile operator: disable the trigger as superuser and rewrite row 3.
	if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events DISABLE TRIGGER immutable_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `UPDATE erp.audit_events SET reason='rewritten' WHERE id=$1`, events[2].ID); err != nil {
		t.Fatal(err)
	}
	// Canonical unchanged but hash no longer matches? No: reason is not in canonical
	// after the fact, so also tamper the canonical to model a real rewrite.
	if _, err := db.Admin.Exec(ctx, `UPDATE erp.audit_events SET canonical = replace(canonical, '"reason":""', '"reason":"rewritten"') WHERE id=$1`, events[2].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events ENABLE TRIGGER immutable_guard`); err != nil {
		t.Fatal(err)
	}
	res, err := audit.Verify(ctx, db.App, p.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intact || res.FirstBreak == nil || *res.FirstBreak != 3 {
		t.Fatalf("expected first break at seq 3: %+v", res)
	}
	// The DDL event trigger recorded the operator's ALTER TABLE.
	var n int
	if err := db.Admin.QueryRow(ctx, `SELECT count(*) FROM ops.ddl_log WHERE command_tag = 'ALTER TABLE' AND object_identity = 'erp.audit_events'`).Scan(&n); err != nil || n < 2 {
		t.Fatalf("ddl_log must record the trigger disable/enable, got %d (%v)", n, err)
	}
}

// B8: timestamps are database-assigned; the event carries the DB clock, not the client's.
func TestB8_DatabaseAssignedTime(t *testing.T) {
	db := testdb.New(t)
	p := principal()
	a := emit(t, db, p, audit.Event{Type: "T"})
	var dbNow int64
	if err := db.App.QueryRow(context.Background(), `SELECT extract(epoch from clock_timestamp())::bigint`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	if d := dbNow - a.OccurredAt.Unix(); d < 0 || d > 60 {
		t.Fatalf("occurred_at not from db clock: delta %d", d)
	}
}
