package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type chainRow struct {
	id        string
	seq       int64
	canonical string
	prev      string
	hash      string
}

// Verify walks one company chain and compares recomputed heads with anchors (B11).
// The first break is reported once; later rows are still counted and are not flagged (B7).
func (s *Service) Verify(ctx context.Context, company uuid.UUID) (oapi.ChainVerification, error) {
	kitRes, err := audit.Verify(ctx, s.Pool, company)
	if err != nil {
		return oapi.ChainVerification{}, err
	}
	rows, err := s.chainRows(ctx, company)
	if err != nil {
		return oapi.ChainVerification{}, err
	}
	recomputed := map[int64]string{}
	bySeq := map[int64]chainRow{}
	for _, row := range rows {
		recomputed[row.seq] = canon.Hash([]byte(row.canonical), row.prev)
		bySeq[row.seq] = row
	}

	var first *breakInfo
	if kitRes.FirstBreak != nil {
		seq := *kitRes.FirstBreak
		row := bySeq[seq]
		reason := Text(s.lang(), "verify.hash_mismatch")
		if row.id != "" && (row.seq != seq || canon.Hash([]byte(row.canonical), row.prev) == row.hash) {
			reason = Text(s.lang(), "verify.link_mismatch")
		}
		first = &breakInfo{Seq: seq, Table: "erp.audit_events", RowID: row.id, Reason: reason}
	}

	anchors, err := s.anchoredHeads(ctx, company)
	if err != nil {
		return oapi.ChainVerification{}, err
	}
	matches := true
	for _, a := range anchors {
		got, ok := recomputed[a.seq]
		if !ok || got != a.hash {
			matches = false
			if first == nil || a.seq < first.Seq {
				row := bySeq[a.seq]
				first = &breakInfo{Seq: a.seq, Table: "erp.audit_events", RowID: row.id, Reason: Text(s.lang(), "verify.anchor_mismatch")}
			}
		}
	}

	out := oapi.ChainVerification{
		Intact:              kitRes.Intact && matches,
		Count:               int(kitRes.Count),
		AnchoredHeadMatches: matches,
	}
	if first != nil {
		out.Intact = false
		reason := first.Reason
		out.FirstBreak = &struct {
			ChainSeq int     `json:"chain_seq"`
			Reason   *string `json:"reason,omitempty"`
			RowId    string  `json:"row_id"` //nolint:revive // matches generated oapi.ChainVerification
			Table    string  `json:"table"`
		}{ChainSeq: int(first.Seq), Reason: &reason, RowId: first.RowID, Table: first.Table}
	}
	if err := s.insertVerification(ctx, company, out, kitRes.HeadHash); err != nil {
		return out, err
	}
	if err := s.publishVerification(ctx, company, out); err != nil {
		return out, err
	}
	return out, nil
}

type breakInfo struct {
	Seq    int64
	Table  string
	RowID  string
	Reason string
}

type anchoredHead struct {
	seq  int64
	hash string
}

func (s *Service) chainRows(ctx context.Context, company uuid.UUID) ([]chainRow, error) {
	pgxRows, err := s.Pool.Query(ctx, `SELECT id::text, chain_seq, canonical, prev_hash, hash FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq`, company)
	if err != nil {
		return nil, err
	}
	defer pgxRows.Close()
	var out []chainRow
	for pgxRows.Next() {
		var row chainRow
		if err := pgxRows.Scan(&row.id, &row.seq, &row.canonical, &row.prev, &row.hash); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, pgxRows.Err()
}

func (s *Service) anchoredHeads(ctx context.Context, company uuid.UUID) ([]anchoredHead, error) {
	pgxRows, err := s.Pool.Query(ctx, `SELECT chain_seq, head_hash FROM erp.anchors WHERE company_id=$1 ORDER BY chain_seq`, company)
	if err != nil {
		return nil, err
	}
	defer pgxRows.Close()
	var out []anchoredHead
	for pgxRows.Next() {
		var a anchoredHead
		if err := pgxRows.Scan(&a.seq, &a.hash); err != nil {
			return nil, err
		}
		if s.Offsite != nil {
			env, _, err := s.anchorFromStore(ctx, s.Offsite, company, a.seq)
			if err == nil && env.HeadHash != "" {
				a.hash = env.HeadHash
			}
		}
		out = append(out, a)
	}
	return out, pgxRows.Err()
}

func (s *Service) insertVerification(ctx context.Context, company uuid.UUID, res oapi.ChainVerification, head string) error {
	var seq *int64
	table, rowID, reason := "", "", ""
	if res.FirstBreak != nil {
		v := int64(res.FirstBreak.ChainSeq)
		seq = &v
		table = res.FirstBreak.Table
		rowID = res.FirstBreak.RowId
		if res.FirstBreak.Reason != nil {
			reason = *res.FirstBreak.Reason
		}
	}
	p := rls.System
	p.CompanyID = company
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.verification_runs(company_id, intact, row_count, first_break_seq, first_break_table, first_break_row_id, first_break_reason, anchored_head_matches, head_hash, ran_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, clock_timestamp())`,
			company, res.Intact, res.Count, seq, table, rowID, reason, res.AnchoredHeadMatches, head)
		return err
	})
}

func (s *Service) publishVerification(ctx context.Context, company uuid.UUID, res oapi.ChainVerification) error {
	if s.River == nil {
		return nil
	}
	now := s.now()
	ctxMap := map[string]any{"intact": res.Intact, "count": res.Count, "anchored_head_matches": res.AnchoredHeadMatches}
	if res.Intact {
		ev, err := newEvent(now, "chain.verified", "LOW", company.String(), "chain", company.String(), "smarterp://audit/verify", nil, ctxMap)
		if err != nil {
			return err
		}
		return s.publish(ctx, ev, false)
	}
	ctxMap["quiet_hours_suppressed"] = false
	ctxMap["audience"] = []string{"stakeholder", "system_manager"}
	if res.FirstBreak != nil {
		ctxMap["chain_seq"] = res.FirstBreak.ChainSeq
		if res.FirstBreak.Reason != nil {
			ctxMap["reason"] = *res.FirstBreak.Reason
		}
	}
	ev, err := newEvent(now, "chain.broken", "CRITICAL", company.String(), "chain", company.String(), "smarterp://audit/verify", []string{"acknowledge", "open"}, ctxMap)
	if err != nil {
		return err
	}
	return s.publish(ctx, ev, true)
}

// VerifyExitCode maps a verification to the script exit codes.
// 0 intact and anchored, 2 intact but the newest anchor is outside the grace window, 3 broken.
func (s *Service) VerifyExitCode(ctx context.Context, company uuid.UUID, res oapi.ChainVerification) (int, error) {
	if !res.Intact || !res.AnchoredHeadMatches {
		return 3, nil
	}
	var last *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT max(anchored_at) FROM erp.anchors WHERE company_id=$1`, company).Scan(&last); err != nil {
		return 3, err
	}
	if last == nil || s.now().Sub(last.UTC()) > AnchorStaleAfter {
		return 2, nil
	}
	return 0, nil
}

// HeadAt returns the recomputed hash at chain_seq, used by restore.
func (s *Service) HeadAt(ctx context.Context, company uuid.UUID, seq int64) (string, error) {
	rows, err := s.chainRows(ctx, company)
	if err != nil {
		return "", err
	}
	var running string
	for _, row := range rows {
		h := canon.Hash([]byte(row.canonical), row.prev)
		if row.seq == seq {
			return h, nil
		}
		running = h
		if row.seq > seq {
			break
		}
	}
	if seq == 0 {
		return "", nil
	}
	return "", fmt.Errorf("chain_seq %d not in the chain (last %s)", seq, running)
}
