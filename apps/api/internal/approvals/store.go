package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
)

type matrixRow struct {
	ThresholdAmount string
	Currency        string
	BelowMode       string
	AboveMode       string
	BelowRoles      []string
	FirstRoles      []string
	FinalRole       string
	VoteN           int
	StepUpAbove     bool
}

type requestRow struct {
	RequestID           string
	DocID               string
	DocType             string
	DocNumber           string
	Party               *string
	Amount              string
	Currency            string
	ContentHash         string
	Snapshot            []byte
	State               string
	StateVersion        int64
	Stage               string
	InitiatorID         string
	InitiatorName       string
	InitiatorDepartment string
	VotesNeeded         int
	VotesHave           int
	DelegateUserID      string
	DelegateUntil       *time.Time
	ThresholdCrossed    bool
	StepUpRequired      bool
	Fraud               []byte
	Severity            string
	WaitingSince        time.Time
}

type decisionRow struct {
	ActorID   string
	ActorName string
	Decision  string
	Reason    string
	HashSeen  string
	DecidedAt time.Time
}

type tokenRow struct {
	RequestID   string
	TokenHash   string
	ContentHash string
	ExpiresAt   time.Time
}

func lockRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7232::int, hashtext($1))`, requestID)
	return err
}

func lockToken(ctx context.Context, tx pgx.Tx, tokenHash string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7233::int, hashtext($1))`, tokenHash)
	return err
}

func loadMatrix(ctx context.Context, tx pgx.Tx, company uuid.UUID, docType string) (matrixRow, error) {
	var m matrixRow
	err := tx.QueryRow(ctx, `SELECT threshold_amount, threshold_currency, below_mode, above_mode, approver_roles_below, first_approver_roles, final_gate_role, vote_n, requires_step_up_above
		FROM erp.approval_matrix WHERE company_id=$1 AND doc_type=$2`, company, docType).
		Scan(&m.ThresholdAmount, &m.Currency, &m.BelowMode, &m.AboveMode, &m.BelowRoles, &m.FirstRoles, &m.FinalRole, &m.VoteN, &m.StepUpAbove)
	if errors.Is(err, pgx.ErrNoRows) {
		return matrixRow{}, apierr.New(apierr.MissingConfig, t(ctx, "config.missing_matrix")).WithDetails(map[string]any{"doc_type": docType})
	}
	return m, err
}

func loadFraudConfig(ctx context.Context, tx pgx.Tx, company uuid.UUID) (fraudConfig, error) {
	cfg := defaultFraud()
	err := tx.QueryRow(ctx, `SELECT variance_percent, round_above, round_step, repeated_rejection_min, corrections_per_month_min
		FROM erp.approval_fraud_config WHERE company_id=$1`, company).
		Scan(&cfg.VariancePercent, &cfg.RoundAbove, &cfg.RoundStep, &cfg.RepeatedRejectionMin, &cfg.CorrectionsPerMonthMin)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultFraud(), nil
	}
	return cfg, err
}

func loadLatest(ctx context.Context, tx pgx.Tx, requestID string) (requestRow, error) {
	var row requestRow
	err := tx.QueryRow(ctx, `SELECT request_id, doc_id, doc_type, doc_number, party, amount, currency, content_hash, snapshot,
		state, state_version, stage, initiator_id, initiator_name, initiator_department, votes_needed, votes_have,
		delegate_user_id, delegate_until, threshold_crossed, step_up_required, fraud, severity, waiting_since
		FROM erp.approval_requests WHERE request_id=$1 ORDER BY state_version DESC LIMIT 1`, requestID).
		Scan(&row.RequestID, &row.DocID, &row.DocType, &row.DocNumber, &row.Party, &row.Amount, &row.Currency, &row.ContentHash, &row.Snapshot,
			&row.State, &row.StateVersion, &row.Stage, &row.InitiatorID, &row.InitiatorName, &row.InitiatorDepartment, &row.VotesNeeded, &row.VotesHave,
			&row.DelegateUserID, &row.DelegateUntil, &row.ThresholdCrossed, &row.StepUpRequired, &row.Fraud, &row.Severity, &row.WaitingSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return requestRow{}, apierr.New(apierr.NotFound, t(ctx, "request.not_found"))
	}
	return row, err
}

func listLatest(ctx context.Context, tx pgx.Tx) ([]requestRow, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (request_id) request_id, doc_id, doc_type, doc_number, party, amount, currency, content_hash, snapshot,
		state, state_version, stage, initiator_id, initiator_name, initiator_department, votes_needed, votes_have,
		delegate_user_id, delegate_until, threshold_crossed, step_up_required, fraud, severity, waiting_since
		FROM erp.approval_requests ORDER BY request_id, state_version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []requestRow
	for rows.Next() {
		var row requestRow
		if err := rows.Scan(&row.RequestID, &row.DocID, &row.DocType, &row.DocNumber, &row.Party, &row.Amount, &row.Currency, &row.ContentHash, &row.Snapshot,
			&row.State, &row.StateVersion, &row.Stage, &row.InitiatorID, &row.InitiatorName, &row.InitiatorDepartment, &row.VotesNeeded, &row.VotesHave,
			&row.DelegateUserID, &row.DelegateUntil, &row.ThresholdCrossed, &row.StepUpRequired, &row.Fraud, &row.Severity, &row.WaitingSince); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadDecisions(ctx context.Context, tx pgx.Tx, requestID string) ([]decisionRow, error) {
	rows, err := tx.Query(ctx, `SELECT actor_id, actor_name, decision, reason, hash_seen, decided_at
		FROM erp.approval_decisions WHERE request_id=$1 ORDER BY decided_at, chain_seq`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []decisionRow
	for rows.Next() {
		var d decisionRow
		if err := rows.Scan(&d.ActorID, &d.ActorName, &d.Decision, &d.Reason, &d.HashSeen, &d.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadToken(ctx context.Context, tx pgx.Tx, tokenHash string) (tokenRow, error) {
	var row tokenRow
	err := tx.QueryRow(ctx, `SELECT request_id, token_hash, content_hash, expires_at FROM erp.approval_posting_tokens WHERE token_hash=$1`, tokenHash).
		Scan(&row.RequestID, &row.TokenHash, &row.ContentHash, &row.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return tokenRow{}, apierr.New(apierr.NotFound, t(ctx, "token.unknown"))
	}
	return row, err
}

func tokenUsed(ctx context.Context, tx pgx.Tx, tokenHash string) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.approval_token_uses WHERE token_hash=$1`, tokenHash).Scan(&n)
	return n > 0, err
}

func assignmentAllows(ctx context.Context, tx pgx.Tx, company uuid.UUID, docType, slot, userID string) (bool, error) {
	var total, mine int
	err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE user_id=$4)
		FROM erp.approval_assignments WHERE company_id=$1 AND doc_type=$2 AND slot=$3`, company, docType, slot, userID).Scan(&total, &mine)
	if err != nil {
		return false, err
	}
	if total == 0 {
		return true, nil
	}
	return mine > 0, nil
}

func appendChain(ctx context.Context, tx pgx.Tx, company uuid.UUID, payload any) (seq int64, prev, hash, canonical string, err error) {
	raw, err := canon.Marshal(payload)
	if err != nil {
		return 0, "", "", "", err
	}
	canonical = string(raw)
	err = tx.QueryRow(ctx, `SELECT chain_seq, prev_hash, hash FROM erp.chain_append($1, $2)`, company, canonical).Scan(&seq, &prev, &hash)
	return seq, prev, hash, canonical, err
}

func insertRequest(ctx context.Context, tx pgx.Tx, company uuid.UUID, row requestRow) error {
	payload := map[string]any{
		"request_id": row.RequestID, "doc_id": row.DocID, "doc_type": row.DocType, "doc_number": row.DocNumber,
		"amount": row.Amount, "currency": row.Currency, "content_hash": row.ContentHash,
		"state": row.State, "state_version": row.StateVersion, "stage": row.Stage, "initiator_id": row.InitiatorID,
	}
	seq, prev, hash, canonical, err := appendChain(ctx, tx, company, payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.approval_requests(
		company_id, request_id, doc_id, doc_type, doc_number, party, amount, currency, content_hash, snapshot, canonical,
		state, state_version, stage, initiator_id, initiator_name, initiator_department, votes_needed, votes_have,
		delegate_user_id, delegate_until, threshold_crossed, step_up_required, fraud, severity, waiting_since,
		chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		company, row.RequestID, row.DocID, row.DocType, row.DocNumber, row.Party, row.Amount, row.Currency, row.ContentHash, row.Snapshot, canonical,
		row.State, row.StateVersion, row.Stage, row.InitiatorID, row.InitiatorName, row.InitiatorDepartment, row.VotesNeeded, row.VotesHave,
		row.DelegateUserID, row.DelegateUntil, row.ThresholdCrossed, row.StepUpRequired, row.Fraud, row.Severity, row.WaitingSince,
		seq, prev, hash)
	return err
}

func insertDecision(ctx context.Context, tx pgx.Tx, company uuid.UUID, requestID string, d decisionRow) error {
	seq, prev, hash, canonical, err := appendChain(ctx, tx, company, map[string]any{
		"request_id": requestID, "actor_id": d.ActorID, "decision": d.Decision, "hash_seen": d.HashSeen, "reason": d.Reason,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.approval_decisions(
		company_id, request_id, actor_id, actor_name, decision, reason, hash_seen, canonical, decided_at, chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		company, requestID, d.ActorID, d.ActorName, d.Decision, d.Reason, d.HashSeen, canonical, d.DecidedAt, seq, prev, hash)
	return err
}

func insertToken(ctx context.Context, tx pgx.Tx, company uuid.UUID, row tokenRow) error {
	seq, prev, hash, canonical, err := appendChain(ctx, tx, company, map[string]any{
		"request_id": row.RequestID, "token_hash": row.TokenHash, "content_hash": row.ContentHash, "expires_at": row.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.approval_posting_tokens(
		company_id, request_id, token_hash, content_hash, expires_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		company, row.RequestID, row.TokenHash, row.ContentHash, row.ExpiresAt, canonical, seq, prev, hash)
	return err
}

func insertUse(ctx context.Context, tx pgx.Tx, company uuid.UUID, requestID, tokenHash, contentHash string, at time.Time) error {
	seq, prev, hash, canonical, err := appendChain(ctx, tx, company, map[string]any{
		"request_id": requestID, "token_hash": tokenHash, "content_hash": contentHash, "used_at": at.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.approval_token_uses(
		company_id, request_id, token_hash, content_hash, used_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		company, requestID, tokenHash, contentHash, at, canonical, seq, prev, hash)
	return err
}

func factsOf(raw []byte) fraudFacts {
	var f fraudFacts
	if len(raw) == 0 {
		return f
	}
	_ = json.Unmarshal(raw, &f)
	if f.OriginalApprovers == nil {
		f.OriginalApprovers = []string{}
	}
	return f
}
