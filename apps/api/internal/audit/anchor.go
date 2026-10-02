package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// AnchorInterval is how often the hourly worker signs the chain head.
const AnchorInterval = time.Hour

// AnchorStaleAfter is how long an off-site anchor may fail before a Critical (I15).
const AnchorStaleAfter = 2 * time.Hour

// complianceRetain is the object-lock retention on anchors (R3.6, seven years).
const complianceRetain = 7 * 365 * 24 * time.Hour

// anchorCore is the signed document. Signature and key id are stored beside it.
type anchorCore struct {
	CompanyID string    `json:"company_id"`
	ChainSeq  int64     `json:"chain_seq"`
	HeadHash  string    `json:"head_hash"`
	RowCount  int64     `json:"row_count"`
	At        time.Time `json:"at"`
}

type anchorEnvelope struct {
	CompanyID string `json:"company_id"`
	ChainSeq  int64  `json:"chain_seq"`
	HeadHash  string `json:"head_hash"`
	RowCount  int64  `json:"row_count"`
	At        string `json:"at"`
	Signature string `json:"signature"`
	KeyID     string `json:"key_id"`
}

func anchorObjectKey(company uuid.UUID, seq int64) string {
	return fmt.Sprintf("anchors/%s/%d.json", company.String(), seq)
}

// Anchor signs the current chain head and writes it to both buckets.
func (s *Service) Anchor(ctx context.Context, company uuid.UUID) error {
	var seq int64
	var head string
	var at time.Time
	err := s.Pool.QueryRow(ctx, `SELECT chain_seq, head_hash, appended_at FROM erp.chain_head WHERE company_id=$1`, company).Scan(&seq, &head, &at)
	if err != nil {
		return fmt.Errorf("chain head: %w", err)
	}
	var rows int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1`, company).Scan(&rows); err != nil {
		return err
	}
	if s.Now != nil {
		at = s.Now().UTC()
	}
	// Truncate so the signed instant round-trips through RFC3339.
	at = at.UTC().Truncate(time.Second)
	core := anchorCore{CompanyID: company.String(), ChainSeq: seq, HeadHash: head, RowCount: rows, At: at.UTC()}
	canonical, err := canon.Marshal(core)
	if err != nil {
		return err
	}
	if s.KMS == nil {
		return fmt.Errorf("%s", Text(s.lang(), "config.missing", "ERP_ANCHOR_KEY_ID"))
	}
	sig, err := s.KMS.Sign(IdentityAnchorWorker, canonical)
	if err != nil {
		s.recordAttempt(ctx, company, false, err.Error())
		return err
	}
	env := anchorEnvelope{
		CompanyID: core.CompanyID,
		ChainSeq:  core.ChainSeq,
		HeadHash:  core.HeadHash,
		RowCount:  core.RowCount,
		At:        at.UTC().Format(time.RFC3339),
		Signature: base64.RawURLEncoding.EncodeToString(sig),
		KeyID:     s.KMS.KeyID(),
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	retain := at.Add(complianceRetain)
	key := anchorObjectKey(company, seq)
	var onErr, offErr error
	if !s.SkipOnPrem {
		onErr = s.putIf(ctx, s.OnPrem, key, body, retain)
		if errors.Is(onErr, ErrImmutableObject) {
			if _, _, err := s.anchorFromStore(ctx, s.OnPrem, company, seq); err == nil {
				onErr = nil
			}
		}
	}
	if !s.SkipOffsite {
		offErr = s.putIf(ctx, s.Offsite, key, body, retain)
		if errors.Is(offErr, ErrImmutableObject) {
			if _, _, err := s.anchorFromStore(ctx, s.Offsite, company, seq); err == nil {
				offErr = nil
			}
		}
	}
	switch {
	case s.SkipOffsite:
		s.recordAttempt(ctx, company, false, "off-site target skipped")
		if onErr != nil {
			return fmt.Errorf("on-prem anchor: %w", onErr)
		}
	case offErr != nil:
		s.recordAttempt(ctx, company, false, offErr.Error())
		if onErr != nil {
			return fmt.Errorf("on-prem: %w; off-site: %v", onErr, offErr)
		}
		return fmt.Errorf("off-site anchor: %w", offErr)
	case onErr != nil:
		s.recordAttempt(ctx, company, false, onErr.Error())
		return fmt.Errorf("on-prem anchor: %w", onErr)
	}
	p := rls.System
	p.CompanyID = company
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.anchors(company_id, chain_seq, head_hash, row_count, anchored_at, signature, key_id, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			company, seq, head, rows, at.UTC(), env.Signature, env.KeyID, string(canonical))
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil
		}
		s.recordAttempt(ctx, company, false, err.Error())
		return err
	}
	if !s.SkipOffsite {
		s.recordAttempt(ctx, company, true, "")
	}
	return nil
}

func (s *Service) putIf(ctx context.Context, store ObjectStore, key string, body []byte, retain time.Time) error {
	if store == nil {
		return fmt.Errorf("object store is not configured")
	}
	return store.PutCompliance(ctx, key, body, retain)
}

func (s *Service) recordAttempt(ctx context.Context, company uuid.UUID, ok bool, detail string) {
	at := time.Now().UTC()
	if s.Now != nil {
		at = s.Now().UTC()
	}
	_, _ = s.Pool.Exec(ctx, `INSERT INTO erp.anchor_attempts(company_id, attempted_at, offsite_ok, detail) VALUES ($1,$2,$3,$4)`,
		company, at, ok, detail)
}

// RaiseIfAnchorStale emits a Critical when the off-site anchor has failed for two hours (I15).
func (s *Service) RaiseIfAnchorStale(ctx context.Context, company uuid.UUID, now time.Time) error {
	var lastOK *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT max(attempted_at) FROM erp.anchor_attempts WHERE company_id=$1 AND offsite_ok`, company).Scan(&lastOK)
	if err != nil {
		return err
	}
	var failures int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM erp.anchor_attempts WHERE company_id=$1 AND NOT offsite_ok AND attempted_at > $2`,
		company, now.Add(-AnchorStaleAfter)).Scan(&failures); err != nil {
		return err
	}
	stale := lastOK == nil || now.Sub(lastOK.UTC()) >= AnchorStaleAfter
	if !stale || failures == 0 {
		return nil
	}
	ev, err := newEvent(now, "chain.broken", "CRITICAL", company.String(), "chain", company.String(), "smarterp://audit/anchor",
		[]string{"acknowledge", "open"}, map[string]any{
			"reason":                 "offsite_anchor_stale",
			"quiet_hours_suppressed": false,
			"audience":               []string{"stakeholder", "system_manager"},
		})
	if err != nil {
		return err
	}
	return s.publish(ctx, ev, true)
}

// anchorFromStore reads the signed object and checks the signature.
func (s *Service) anchorFromStore(ctx context.Context, store ObjectStore, company uuid.UUID, seq int64) (anchorEnvelope, []byte, error) {
	var env anchorEnvelope
	body, err := store.Get(ctx, anchorObjectKey(company, seq))
	if err != nil {
		return env, nil, err
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return env, nil, err
	}
	core := anchorCore{CompanyID: env.CompanyID, ChainSeq: env.ChainSeq, HeadHash: env.HeadHash, RowCount: env.RowCount}
	parsed, err := time.Parse(time.RFC3339, env.At)
	if err != nil {
		return env, nil, err
	}
	core.At = parsed.UTC()
	canonical, err := canon.Marshal(core)
	if err != nil {
		return env, nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil {
		return env, nil, err
	}
	if s.KMS == nil || env.KeyID != s.KMS.KeyID() || !s.KMS.Verify(canonical, sig) {
		return env, canonical, fmt.Errorf("anchor signature does not verify with the KMS public key")
	}
	return env, canonical, nil
}
