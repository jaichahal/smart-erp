// Package audit appends events to the per-company hash chain (R2.7, R3.4, R3.12).
//
// Emit must be called inside the same transaction as the business write so the
// event and the change commit or roll back together. For refused attempts that
// must survive a rollback (R2.7), use EmitCommitted with the pool instead.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Event is what a module records. Before and After are optional JSON snapshots.
type Event struct {
	Type          string
	ReferenceType string
	ReferenceID   string
	Before        any
	After         any
	Reason        string
}

// Appended is the persisted event with its chain position.
type Appended struct {
	ID         uuid.UUID
	ChainSeq   int64
	OccurredAt time.Time
	PrevHash   string
	Hash       string
	Canonical  string
}

// payload is the exact shape that is canonicalised and hashed. Field names are the
// wire names; prev_hash and hash are excluded by canon. Keep in sync with
// contracts/fixtures/canonical/audit_event.json.
type payload struct {
	ID            string          `json:"id"`
	CompanyID     string          `json:"company_id"`
	EventType     string          `json:"event_type"`
	ActorID       string          `json:"actor_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	ReferenceType string          `json:"reference_type"`
	ReferenceID   string          `json:"reference_id"`
	Before        json.RawMessage `json:"before_state"`
	After         json.RawMessage `json:"after_state"`
	Reason        string          `json:"reason"`
}

// Emit appends the event within tx for the principal on ctx.
func Emit(ctx context.Context, tx pgx.Tx, ev Event) (*Appended, error) {
	p, err := rls.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return EmitAs(ctx, tx, p, ev)
}

// EmitAs appends the event within tx for an explicit principal.
func EmitAs(ctx context.Context, tx pgx.Tx, p rls.Principal, ev Event) (*Appended, error) {
	if p.CompanyID == uuid.Nil {
		return nil, fmt.Errorf("audit: principal has no company")
	}
	if ev.Type == "" {
		return nil, fmt.Errorf("audit: event type is required")
	}
	before, err := rawJSON(ev.Before)
	if err != nil {
		return nil, err
	}
	after, err := rawJSON(ev.After)
	if err != nil {
		return nil, err
	}
	// Database-assigned time (B8): read the clock inside the transaction.
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return nil, err
	}
	id := uuid.New()
	pl := payload{ID: id.String(), CompanyID: p.CompanyID.String(), EventType: ev.Type, ActorID: p.UserID, OccurredAt: at.UTC(),
		ReferenceType: ev.ReferenceType, ReferenceID: ev.ReferenceID, Before: before, After: after, Reason: ev.Reason}
	canonical, err := canon.Marshal(pl)
	if err != nil {
		return nil, err
	}
	var seq int64
	var prev, hash string
	if err := tx.QueryRow(ctx, `SELECT chain_seq, prev_hash, hash FROM erp.chain_append($1, $2)`, p.CompanyID, string(canonical)).Scan(&seq, &prev, &hash); err != nil {
		return nil, fmt.Errorf("audit: chain append: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.audit_events(id, company_id, chain_seq, event_type, actor_id, occurred_at, reference_type, reference_id, before_state, after_state, reason, canonical, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		id, p.CompanyID, seq, ev.Type, p.UserID, at, ev.ReferenceType, ev.ReferenceID, nullable(before), nullable(after), ev.Reason, string(canonical), prev, hash)
	if err != nil {
		return nil, fmt.Errorf("audit: insert: %w", err)
	}
	return &Appended{ID: id, ChainSeq: seq, OccurredAt: at, PrevHash: prev, Hash: hash, Canonical: string(canonical)}, nil
}

// EmitCommitted records an event in its own transaction and commits it, for
// refusals that must outlive the caller's rollback (R2.7 "committed before raising").
func EmitCommitted(ctx context.Context, pool *pgxpool.Pool, p rls.Principal, ev Event) (*Appended, error) {
	var out *Appended
	err := rls.Tx(context.WithoutCancel(ctx), pool, p, func(tx pgx.Tx) error {
		a, err := EmitAs(ctx, tx, p, ev)
		out = a
		return err
	})
	return out, err
}

// VerifyResult is the outcome of walking one company's chain.
type VerifyResult struct {
	Intact     bool   `json:"intact"`
	Count      int64  `json:"count"`
	FirstBreak *int64 `json:"first_break,omitempty"`
	HeadHash   string `json:"head_hash"`
}

// Verify re-walks the company chain: recomputes each hash from the stored
// canonical and prev_hash, and checks each prev_hash equals the previous hash.
// It reports the first break and does not cascade (05 "Hash chain").
func Verify(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, company uuid.UUID) (*VerifyResult, error) {
	rows, err := q.Query(ctx, `SELECT chain_seq, canonical, prev_hash, hash FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq`, company)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &VerifyResult{Intact: true}
	runningPrev := ""
	expectSeq := int64(1)
	for rows.Next() {
		var seq int64
		var canonical, prev, hash string
		if err := rows.Scan(&seq, &canonical, &prev, &hash); err != nil {
			return nil, err
		}
		res.Count++
		if res.FirstBreak == nil && (seq != expectSeq || prev != runningPrev || canon.Hash([]byte(canonical), prev) != hash) {
			s := seq
			res.FirstBreak = &s
			res.Intact = false
		}
		runningPrev = hash
		expectSeq = seq + 1
		res.HeadHash = hash
	}
	return res, rows.Err()
}

func rawJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if r, ok := v.(json.RawMessage); ok {
		return r, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: snapshot: %w", err)
	}
	return b, nil
}

func nullable(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return string(r)
}
