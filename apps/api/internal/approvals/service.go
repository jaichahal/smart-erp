package approvals

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the approval workflow. HTTP and other modules share it.
type Service struct {
	Pool   *pgxpool.Pool
	River  *river.Client[pgx.Tx]
	Dir    Directory
	StepUp StepUpVerifier
	Clock  Clock
}

// New builds a service. A nil step-up verifier or clock is replaced with the stub and the wall clock.
func New(pool *pgxpool.Pool, jobs *river.Client[pgx.Tx], dir Directory, step StepUpVerifier, clock Clock) *Service {
	if step == nil {
		step = StubStepUp{}
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Service{Pool: pool, River: jobs, Dir: dir, StepUp: step, Clock: clock}
}

// Matrix is the per-document-type configuration (R2.2).
type Matrix struct {
	DocType             string
	ThresholdAmount     string
	Currency            string
	BelowRoles          []string
	FirstRoles          []string
	FinalRole           string
	AboveMode           string
	VoteN               int
	RequiresStepUpAbove bool
}

// FraudSettings are the hint thresholds (R2.10, D12).
type FraudSettings struct {
	VariancePercent        string
	RoundAbove             string
	RoundStep              string
	RepeatedRejectionMin   int
	CorrectionsPerMonthMin int
}

// SubmitInput opens a request for a draft. ClientState is ignored (R2.5).
type SubmitInput struct {
	DocID                string
	DocType              string
	DocNumber            string
	Party                string
	Amount               string
	Currency             string
	Snapshot             map[string]any
	UsualAmount          string
	OriginalAmount       string
	OriginalApproverIDs  []string
	RejectionChain       int
	CorrectionsThisMonth int
	OriginalDocID        string
	ClientState          string
}

// DecisionInput is an approve or reject. ClientState is ignored (D6).
type DecisionInput struct {
	RequestID    string
	StateVersion int
	Comment      string
	Reason       string
	StepUpToken  string
	ClientState  string
}

// Outcome is a recorded decision. PostingToken is set only on final approval and only in this process.
type Outcome struct {
	Decision       oapi.ApprovalDecision
	Notice         string
	PostingToken   string
	PostingExpires time.Time
}

// Restored is the stored decision fields (R2.5).
type Restored struct {
	State        string
	Amount       string
	ContentHash  string
	InitiatorID  string
	StateVersion int
}

// Submit records the draft hash and opens the request (R2.11).
func (s *Service) Submit(ctx context.Context, p rls.Principal, in SubmitInput) (*Outcome, error) {
	_ = in.ClientState
	if !docTypeRe.MatchString(in.DocType) {
		return nil, apierr.New(apierr.ValidationError, t(ctx, "validation.doc_type"))
	}
	if !currencyRe.MatchString(in.Currency) {
		return nil, apierr.New(apierr.ValidationError, t(ctx, "validation.currency"))
	}
	amt, err := parseAmount(ctx, in.Amount)
	if err != nil {
		return nil, err
	}
	initiator, err := s.Dir.Get(ctx, p.CompanyID, p.UserID)
	if err != nil {
		return nil, err
	}
	if in.Snapshot == nil {
		in.Snapshot = map[string]any{}
	}
	sum, err := contentHash(in.Snapshot)
	if err != nil {
		return nil, err
	}
	snap, err := json.Marshal(in.Snapshot)
	if err != nil {
		return nil, err
	}
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	matrix, err := loadMatrix(ctx, tx, p.CompanyID, in.DocType)
	if err != nil {
		return nil, err
	}
	threshold, err := parseAmount(ctx, matrix.ThresholdAmount)
	if err != nil {
		return nil, err
	}
	above := amt.Cmp(threshold) >= 0
	stage := stageBelow
	votes := 1
	if above {
		if matrix.AboveMode == "vote_n_of_m" {
			stage = stageVote
			votes = matrix.VoteN
			if votes < 1 {
				return nil, apierr.New(apierr.MissingConfig, t(ctx, "config.missing_matrix"))
			}
		} else {
			stage = stageFirst
		}
	}
	facts := fraudFacts{
		UsualAmount: in.UsualAmount, OriginalAmount: in.OriginalAmount, OriginalApprovers: in.OriginalApproverIDs,
		RejectionChain: in.RejectionChain, CorrectionsThisMonth: in.CorrectionsThisMonth, OriginalDocID: in.OriginalDocID,
	}
	if facts.OriginalApprovers == nil {
		facts.OriginalApprovers = []string{}
	}
	rawFacts, err := json.Marshal(facts)
	if err != nil {
		return nil, err
	}
	id, err := NewULID(s.Clock.Now())
	if err != nil {
		return nil, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	var party *string
	if in.Party != "" {
		party = &in.Party
	}
	row := requestRow{
		RequestID: id, DocID: in.DocID, DocType: in.DocType, DocNumber: in.DocNumber, Party: party,
		Amount: in.Amount, Currency: in.Currency, ContentHash: sum, Snapshot: snap,
		State: statePending, StateVersion: 1, Stage: stage,
		InitiatorID: initiator.ID, InitiatorName: initiator.Name, InitiatorDepartment: initiator.Department,
		VotesNeeded: votes, Fraud: rawFacts, Severity: string(cardSeverity(in.DocType, above, 0)),
		WaitingSince: now, ThresholdCrossed: above, StepUpRequired: (above && matrix.StepUpAbove) || alwaysStepUp(in.DocType),
	}
	if err := insertRequest(ctx, tx, p.CompanyID, row); err != nil {
		return nil, err
	}
	if err := s.auditOK(ctx, tx, p, "approval.requested", row, ""); err != nil {
		return nil, err
	}
	if err := s.enqueue(ctx, tx, p, "approval.requested", row, initiator.Name, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.fresh(ctx, p, id)
}

// Approve records an approval using stored fields only (R2.5, R2.6).
func (s *Service) Approve(ctx context.Context, p rls.Principal, in DecisionInput) (*Outcome, error) {
	return s.decide(ctx, p, in, "approve")
}

// Reject records a rejection. An empty reason is refused at this layer (R2.4).
func (s *Service) Reject(ctx context.Context, p rls.Principal, in DecisionInput) (*Outcome, error) {
	return s.decide(ctx, p, in, "reject")
}

// Delegate grants a time-boxed first-stage approval to someone else (R2.9, D11).
func (s *Service) Delegate(ctx context.Context, p rls.Principal, requestID, toUser string, until time.Time, stateVersion int) (*Outcome, error) {
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, done, err := s.prepare(ctx, tx, p, requestID, stateVersion)
	if err != nil || done != nil {
		return done, err
	}
	if head.Stage == stageFinal || head.Stage == stageDone {
		return s.deny(ctx, tx, p, head, "final_gate", apierr.New(apierr.PermissionDenied, t(ctx, "delegate.final_gate")))
	}
	if head.Stage != stageBelow && head.Stage != stageFirst && head.Stage != stageVote {
		return s.deny(ctx, tx, p, head, "not_delegable", apierr.New(apierr.PermissionDenied, t(ctx, "delegate.not_allowed")))
	}
	if !until.After(s.Clock.Now()) {
		return s.deny(ctx, tx, p, head, "until_past", apierr.New(apierr.ValidationError, t(ctx, "delegate.until_past")))
	}
	actor, code, err := s.actor(ctx, p)
	if err != nil {
		return s.deny(ctx, tx, p, head, code, err)
	}
	if toUser == head.InitiatorID {
		return s.deny(ctx, tx, p, head, "self_approval", apierr.New(apierr.SoDViolation, t(ctx, "sod.self_approval")))
	}
	ok, err := s.roleOK(ctx, tx, p.CompanyID, actor, head)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.deny(ctx, tx, p, head, "not_eligible", apierr.New(apierr.PermissionDenied, t(ctx, "auth.not_eligible")))
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	next := head
	next.StateVersion++
	next.State = stateDelegated
	next.DelegateUserID = toUser
	u := until.UTC()
	next.DelegateUntil = &u
	if err := insertRequest(ctx, tx, p.CompanyID, next); err != nil {
		return nil, err
	}
	if err := insertDecision(ctx, tx, p.CompanyID, head.RequestID, decisionRow{
		ActorID: actor.ID, ActorName: actor.Name, Decision: "delegated", Reason: toUser, HashSeen: head.ContentHash, DecidedAt: now,
	}); err != nil {
		return nil, err
	}
	if err := s.auditOK(ctx, tx, p, "approval.delegated", next, toUser); err != nil {
		return nil, err
	}
	if err := s.enqueue(ctx, tx, p, "approval.delegated", next, actor.Name, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.fresh(ctx, p, head.RequestID)
}

// Restore returns stored decision fields and drops client copies of them (D6).
func (s *Service) Restore(ctx context.Context, p rls.Principal, requestID string, client map[string]any) (Restored, error) {
	discardClientDecisionFields(client)
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return Restored{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := loadLatest(ctx, tx, requestID)
	if err != nil {
		return Restored{}, err
	}
	return Restored{State: head.State, Amount: head.Amount, ContentHash: head.ContentHash, InitiatorID: head.InitiatorID, StateVersion: int(head.StateVersion)}, nil
}

func discardClientDecisionFields(client map[string]any) {
	if client == nil {
		return
	}
	for _, k := range []string{"state", "amount", "content_hash", "initiator_id", "snapshot_hash"} {
		delete(client, k)
	}
}

// Get returns the live approval including the submission snapshot and hash.
func (s *Service) Get(ctx context.Context, p rls.Principal, requestID string) (*oapi.ApprovalDetail, error) {
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := loadLatest(ctx, tx, requestID)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, tx, p, head)
}

// Inbox lists cards for the caller.
func (s *Service) Inbox(ctx context.Context, p rls.Principal, state string, limit int) ([]oapi.ApprovalCard, error) {
	if state == "" {
		state = "needs_me"
	}
	switch state {
	case "needs_me", "waiting_on_others", "fyi":
	default:
		return nil, apierr.New(apierr.ValidationError, t(ctx, "inbox.state"))
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := listLatest(ctx, tx)
	if err != nil {
		return nil, err
	}
	var cards []oapi.ApprovalCard
	for _, head := range rows {
		bucket, card, err := s.bucket(ctx, tx, p, head)
		if err != nil {
			return nil, err
		}
		if bucket != state {
			continue
		}
		cards = append(cards, card)
		if len(cards) >= limit {
			break
		}
	}
	if cards == nil {
		cards = []oapi.ApprovalCard{}
	}
	return cards, nil
}

// PutMatrix upserts one matrix row.
func (s *Service) PutMatrix(ctx context.Context, p rls.Principal, m Matrix) error {
	if !docTypeRe.MatchString(m.DocType) {
		return apierr.New(apierr.ValidationError, t(ctx, "validation.doc_type"))
	}
	if _, err := parseAmount(ctx, m.ThresholdAmount); err != nil {
		return err
	}
	if m.Currency == "" {
		m.Currency = "AED"
	}
	if !currencyRe.MatchString(m.Currency) {
		return apierr.New(apierr.ValidationError, t(ctx, "validation.currency"))
	}
	if m.AboveMode == "" {
		m.AboveMode = "first_then_final"
	}
	if m.BelowRoles == nil {
		m.BelowRoles = []string{}
	}
	if m.FirstRoles == nil {
		m.FirstRoles = []string{}
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.approval_matrix(
			company_id, doc_type, threshold_amount, threshold_currency, above_mode, approver_roles_below, first_approver_roles, final_gate_role, vote_n, requires_step_up_above)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (company_id, doc_type) DO UPDATE SET
			  threshold_amount=EXCLUDED.threshold_amount, threshold_currency=EXCLUDED.threshold_currency, above_mode=EXCLUDED.above_mode,
			  approver_roles_below=EXCLUDED.approver_roles_below, first_approver_roles=EXCLUDED.first_approver_roles, final_gate_role=EXCLUDED.final_gate_role,
			  vote_n=EXCLUDED.vote_n, requires_step_up_above=EXCLUDED.requires_step_up_above`,
			p.CompanyID, m.DocType, m.ThresholdAmount, m.Currency, m.AboveMode, m.BelowRoles, m.FirstRoles, m.FinalRole, m.VoteN, m.RequiresStepUpAbove)
		return err
	})
}

// PutFraudConfig upserts hint thresholds.
func (s *Service) PutFraudConfig(ctx context.Context, p rls.Principal, c FraudSettings) error {
	if _, err := parseAmount(ctx, c.VariancePercent); err != nil {
		return err
	}
	if _, err := parseAmount(ctx, c.RoundAbove); err != nil {
		return err
	}
	if _, err := parseAmount(ctx, c.RoundStep); err != nil {
		return err
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.approval_fraud_config(company_id, variance_percent, round_above, round_step, repeated_rejection_min, corrections_per_month_min)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (company_id) DO UPDATE SET variance_percent=EXCLUDED.variance_percent, round_above=EXCLUDED.round_above,
			  round_step=EXCLUDED.round_step, repeated_rejection_min=EXCLUDED.repeated_rejection_min, corrections_per_month_min=EXCLUDED.corrections_per_month_min`,
			p.CompanyID, c.VariancePercent, c.RoundAbove, c.RoundStep, c.RepeatedRejectionMin, c.CorrectionsPerMonthMin)
		return err
	})
}

// UpsertActor writes the approvals read model for one person.
func (s *Service) UpsertActor(ctx context.Context, p rls.Principal, a Actor) error {
	if a.Roles == nil {
		a.Roles = []string{}
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.approval_actors(company_id, user_id, name, department, roles) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (company_id, user_id) DO UPDATE SET name=EXCLUDED.name, department=EXCLUDED.department, roles=EXCLUDED.roles`,
			p.CompanyID, a.ID, a.Name, a.Department, a.Roles)
		return err
	})
}

// AssignApprover adds a person to a configured slot. An accountant cannot add themselves (D2).
func (s *Service) AssignApprover(ctx context.Context, p rls.Principal, docType, slot, userID string) error {
	switch slot {
	case stageBelow, stageFirst, stageFinal, stageVote:
	default:
		return apierr.New(apierr.ValidationError, t(ctx, "validation.slot"))
	}
	actor, err := s.Dir.Get(ctx, p.CompanyID, p.UserID)
	if err != nil {
		return err
	}
	if hasRole(actor.Roles, roleAccountant) && actor.ID == userID {
		if err := s.refuse(ctx, p, "", "accountant_self_assign", map[string]any{"doc_type": docType, "slot": slot, "user_id": userID}); err != nil {
			return err
		}
		return apierr.New(apierr.SoDViolation, t(ctx, "sod.accountant_self_assign"))
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.approval_assignments(company_id, doc_type, slot, user_id, assigned_by) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT DO NOTHING`, p.CompanyID, docType, slot, userID, actor.ID); err != nil {
			return err
		}
		_, err := audit.EmitAs(ctx, tx, p, audit.Event{Type: "approval.assignment", ReferenceType: "approval_matrix", ReferenceID: docType, Reason: slot, After: map[string]any{"user_id": userID}})
		return err
	})
}

// ConsumePostingToken validates a single-use five-minute token and the approved hash (D9, D10).
func (s *Service) ConsumePostingToken(ctx context.Context, p rls.Principal, token string, content any) error {
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	got, err := contentHash(content)
	if err != nil {
		return err
	}
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockToken(ctx, tx, tokenHash); err != nil {
		return err
	}
	row, err := loadToken(ctx, tx, tokenHash)
	if err != nil {
		_ = tx.Rollback(ctx)
		if aerr := s.refuse(ctx, p, "", "token_unknown", map[string]any{"token_hash": tokenHash}); aerr != nil {
			return aerr
		}
		return err
	}
	if !s.Clock.Now().Before(row.ExpiresAt) {
		return s.denyToken(ctx, tx, p, row.RequestID, "token_expired", apierr.New(apierr.Conflict, t(ctx, "token.expired")))
	}
	used, err := tokenUsed(ctx, tx, tokenHash)
	if err != nil {
		return err
	}
	if used {
		return s.denyToken(ctx, tx, p, row.RequestID, "token_used", apierr.New(apierr.Conflict, t(ctx, "token.used")))
	}
	if got != row.ContentHash {
		return s.denyToken(ctx, tx, p, row.RequestID, "hash_mismatch", apierr.New(apierr.Conflict, t(ctx, "token.hash_mismatch")).WithDetails(map[string]any{
			"approved_hash": row.ContentHash, "posted_hash": got,
		}))
	}
	if err := lockRequest(ctx, tx, row.RequestID); err != nil {
		return err
	}
	head, err := loadLatest(ctx, tx, row.RequestID)
	if err != nil {
		return err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	if err := insertUse(ctx, tx, p.CompanyID, row.RequestID, tokenHash, got, now); err != nil {
		return err
	}
	next := head
	next.StateVersion++
	next.State = statePosted
	next.Stage = stageDone
	if err := insertRequest(ctx, tx, p.CompanyID, next); err != nil {
		return err
	}
	if err := s.auditOK(ctx, tx, p, "approval.posted", next, "posting token consumed"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) denyToken(ctx context.Context, tx pgx.Tx, p rls.Principal, requestID, code string, cause error) error {
	_ = tx.Rollback(ctx)
	if err := s.refuse(ctx, p, requestID, code, nil); err != nil {
		return err
	}
	return cause
}

func (s *Service) decide(ctx context.Context, p rls.Principal, in DecisionInput, kind string) (*Outcome, error) {
	_ = in.ClientState
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, done, err := s.prepare(ctx, tx, p, in.RequestID, in.StateVersion)
	if err != nil || done != nil {
		return done, err
	}
	if kind == "reject" {
		if err := NonEmptyReason(ctx, in.Reason); err != nil {
			return s.deny(ctx, tx, p, head, "empty_reason", err)
		}
	}
	if len(in.Comment) > 2000 || len(in.Reason) > 2000 {
		return s.deny(ctx, tx, p, head, "too_long", apierr.New(apierr.ValidationError, t(ctx, "reason.too_long")))
	}
	actor, code, err := s.actor(ctx, p)
	if err != nil {
		return s.deny(ctx, tx, p, head, code, err)
	}
	if err := s.gate(ctx, tx, p, actor, head, kind, in.StepUpToken); err != nil {
		return s.deny(ctx, tx, p, head, codeOf(err), err)
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	next, final := advance(head, kind)
	if err := insertRequest(ctx, tx, p.CompanyID, next); err != nil {
		return nil, err
	}
	reason := in.Comment
	decision := "approved"
	if kind == "reject" {
		reason = in.Reason
		decision = "rejected"
	}
	if err := insertDecision(ctx, tx, p.CompanyID, head.RequestID, decisionRow{
		ActorID: actor.ID, ActorName: actor.Name, Decision: decision, Reason: reason, HashSeen: head.ContentHash, DecidedAt: now,
	}); err != nil {
		return nil, err
	}
	var rawToken string
	var exp time.Time
	if final {
		rawToken, exp, err = s.issueToken(ctx, tx, p.CompanyID, next)
		if err != nil {
			return nil, err
		}
	}
	auditType := "approval.approved"
	if kind == "reject" {
		auditType = "approval.rejected"
	}
	if err := s.auditOK(ctx, tx, p, auditType, next, reason); err != nil {
		return nil, err
	}
	if err := s.enqueue(ctx, tx, p, "approval.decided", next, actor.Name, map[string]any{"decision": decision, "final": final}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out, err := s.fresh(ctx, p, head.RequestID)
	if err != nil {
		return nil, err
	}
	out.PostingToken = rawToken
	out.PostingExpires = exp
	return out, nil
}

// prepare locks the request and resolves the concurrency outcome (D7).
// A non-nil Outcome means the caller should return it without writing.
func (s *Service) prepare(ctx context.Context, tx pgx.Tx, p rls.Principal, requestID string, stateVersion int) (requestRow, *Outcome, error) {
	if err := lockRequest(ctx, tx, requestID); err != nil {
		return requestRow{}, nil, err
	}
	head, err := loadLatest(ctx, tx, requestID)
	if err != nil {
		return requestRow{}, nil, err
	}
	if terminal(head.State) || head.StateVersion > int64(stateVersion) {
		out, err := s.outcomeFrom(ctx, tx, head, string(oapi.DecisionMetaNoticeALREADYDECIDED))
		return requestRow{}, out, err
	}
	if head.StateVersion < int64(stateVersion) {
		cause := ifmatch.Check(int64(stateVersion), head.StateVersion, map[string]any{"state": head.State, "state_version": head.StateVersion})
		out, err := s.deny(ctx, tx, p, head, "state_version", cause)
		return requestRow{}, out, err
	}
	return head, nil, nil
}

func (s *Service) actor(ctx context.Context, p rls.Principal) (Actor, string, error) {
	a, err := s.Dir.Get(ctx, p.CompanyID, p.UserID)
	if err != nil {
		return Actor{}, "not_in_directory", err
	}
	return a, "", nil
}

func (s *Service) gate(ctx context.Context, tx pgx.Tx, p rls.Principal, actor Actor, head requestRow, kind, stepToken string) error {
	if err := s.eligible(ctx, tx, p, actor, head, kind); err != nil {
		return err
	}
	if kind == "approve" && head.StepUpRequired {
		if err := s.StepUp.Verify(ctx, p, stepToken, stepUpAction(head.DocType)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) eligible(ctx context.Context, tx pgx.Tx, p rls.Principal, actor Actor, head requestRow, kind string) error {
	if actor.ID == head.InitiatorID {
		return ruled(ctx, apierr.SoDViolation, "sod.self_approval")
	}
	if (head.Stage == stageFirst || head.Stage == stageVote) && inAccounts(head.InitiatorDepartment) && inAccounts(actor.Department) {
		return ruled(ctx, apierr.SoDViolation, "sod.accounts_independence")
	}
	activeDelegate := head.DelegateUserID == actor.ID && head.DelegateUntil != nil && s.Clock.Now().Before(*head.DelegateUntil)
	if !activeDelegate {
		ok, err := s.roleOK(ctx, tx, p.CompanyID, actor, head)
		if err != nil {
			return err
		}
		if !ok {
			if head.DelegateUserID == actor.ID && head.DelegateUntil != nil && !s.Clock.Now().Before(*head.DelegateUntil) {
				return ruled(ctx, apierr.PermissionDenied, "delegate.expired")
			}
			return ruled(ctx, apierr.PermissionDenied, "auth.not_eligible")
		}
	}
	if head.Stage == stageVote && kind == "approve" {
		decs, err := loadDecisions(ctx, tx, head.RequestID)
		if err != nil {
			return err
		}
		for _, d := range decs {
			if d.ActorID == actor.ID && d.Decision == "approved" {
				return apierr.New(apierr.PermissionDenied, t(ctx, "auth.not_eligible"))
			}
		}
	}
	return nil
}

func (s *Service) roleOK(ctx context.Context, tx pgx.Tx, company uuid.UUID, actor Actor, head requestRow) (bool, error) {
	matrix, err := loadMatrix(ctx, tx, company, head.DocType)
	if err != nil {
		return false, err
	}
	var slot string
	var roles []string
	switch head.Stage {
	case stageBelow:
		slot, roles = stageBelow, matrix.BelowRoles
	case stageFirst:
		slot, roles = stageFirst, matrix.FirstRoles
	case stageVote:
		slot, roles = stageVote, matrix.FirstRoles
	case stageFinal:
		slot, roles = stageFinal, []string{matrix.FinalRole}
	default:
		return false, nil
	}
	if !hasRole(actor.Roles, roles...) {
		return false, nil
	}
	return assignmentAllows(ctx, tx, company, head.DocType, slot, actor.ID)
}

func advance(head requestRow, kind string) (requestRow, bool) {
	next := head
	next.StateVersion++
	next.DelegateUserID = ""
	next.DelegateUntil = nil
	if kind == "reject" {
		next.State = stateRejected
		next.Stage = stageDone
		return next, false
	}
	switch head.Stage {
	case stageBelow, stageFinal:
		next.State = stateApproved
		next.Stage = stageDone
		return next, true
	case stageFirst:
		next.State = statePending
		next.Stage = stageFinal
		return next, false
	case stageVote:
		next.VotesHave++
		if next.VotesHave >= next.VotesNeeded {
			next.State = stateApproved
			next.Stage = stageDone
			return next, true
		}
		next.State = statePending
		next.Stage = stageVote
		return next, false
	default:
		next.State = head.State
		return next, false
	}
}

func (s *Service) issueToken(ctx context.Context, tx pgx.Tx, company uuid.UUID, head requestRow) (string, time.Time, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", time.Time{}, err
	}
	raw := hex.EncodeToString(buf[:])
	sum := sha256.Sum256([]byte(raw))
	exp := s.Clock.Now().Add(postingTTL)
	err := insertToken(ctx, tx, company, tokenRow{RequestID: head.RequestID, TokenHash: hex.EncodeToString(sum[:]), ContentHash: head.ContentHash, ExpiresAt: exp})
	return raw, exp, err
}

func (s *Service) deny(ctx context.Context, tx pgx.Tx, p rls.Principal, head requestRow, code string, cause error) (*Outcome, error) {
	_ = tx.Rollback(ctx)
	if err := s.refuse(ctx, p, head.RequestID, code, map[string]any{"state": head.State, "state_version": head.StateVersion}); err != nil {
		return nil, err
	}
	return nil, cause
}

func (s *Service) refuse(ctx context.Context, p rls.Principal, requestID, code string, before any) error {
	_, err := audit.EmitCommitted(ctx, s.Pool, p, audit.Event{
		Type: "approval.refused", ReferenceType: "approval_request", ReferenceID: requestID, Reason: code, Before: before,
	})
	return err
}

func (s *Service) auditOK(ctx context.Context, tx pgx.Tx, p rls.Principal, eventType string, row requestRow, reason string) error {
	_, err := audit.EmitAs(ctx, tx, p, audit.Event{
		Type: eventType, ReferenceType: "approval_request", ReferenceID: row.RequestID, Reason: reason,
		After: map[string]any{"state": row.State, "state_version": row.StateVersion, "stage": row.Stage, "content_hash": row.ContentHash},
	})
	return err
}

func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, p rls.Principal, eventType string, row requestRow, actorName string, extra map[string]any) error {
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	id, err := NewULID(now)
	if err != nil {
		return err
	}
	cfg, err := loadFraudConfig(ctx, tx, p.CompanyID)
	if err != nil {
		return err
	}
	contextMap := map[string]any{
		"request_id": row.RequestID, "stage": row.Stage, "state": row.State,
		"threshold_crossed": row.ThresholdCrossed, "fraud_hints": hints(ctx, row.Amount, factsOf(row.Fraud), cfg, ""),
	}
	for k, v := range extra {
		contextMap[k] = v
	}
	docNumber := row.DocNumber
	ev := eventPayload{
		EventID: id, Type: eventType, Severity: string(cardSeverity(row.DocType, row.ThresholdCrossed, len(hints(ctx, row.Amount, factsOf(row.Fraud), cfg, "")))),
		CompanyID: p.CompanyID.String(), OccurredAt: now.UTC().Format(time.RFC3339Nano),
		Actor:          eventActor{ID: p.UserID, Name: actorName},
		Subject:        eventSubject{DocType: row.DocType, DocID: row.DocID, DocNumber: &docNumber, Party: row.Party},
		Amount:         &eventMoney{Amount: row.Amount, Currency: row.Currency},
		DeepLink:       "smarterp://approval/" + row.RequestID,
		AllowedActions: actionNames(row),
		StateVersion:   int(row.StateVersion),
		Context:        contextMap,
	}
	return raise(ctx, s.River, tx, ev)
}

func actionNames(row requestRow) []string {
	if terminal(row.State) {
		return []string{"open"}
	}
	out := []string{"approve", "reject", "open"}
	if row.Stage == stageBelow || row.Stage == stageFirst || row.Stage == stageVote {
		out = append(out, "delegate")
	}
	return out
}

func (s *Service) fresh(ctx context.Context, p rls.Principal, requestID string) (*Outcome, error) {
	tx, err := rls.Begin(ctx, s.Pool, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	head, err := loadLatest(ctx, tx, requestID)
	if err != nil {
		return nil, err
	}
	return s.outcomeFrom(ctx, tx, head, "")
}

func (s *Service) outcomeFrom(ctx context.Context, tx pgx.Tx, head requestRow, notice string) (*Outcome, error) {
	decs, err := loadDecisions(ctx, tx, head.RequestID)
	if err != nil {
		return nil, err
	}
	d := oapi.ApprovalDecision{RequestId: head.RequestID, State: oapi.ApprovalState(head.State), StateVersion: int(head.StateVersion)}
	if len(decs) > 0 {
		last := decs[len(decs)-1]
		at := last.DecidedAt
		d.DecidedAt = &at
		d.DecidedBy = &oapi.Actor{Id: last.ActorID, Name: last.ActorName}
	}
	return &Outcome{Decision: d, Notice: notice}, nil
}

func (s *Service) detail(ctx context.Context, tx pgx.Tx, p rls.Principal, head requestRow) (*oapi.ApprovalDetail, error) {
	card, _, err := s.cardFor(ctx, tx, p, head)
	if err != nil {
		return nil, err
	}
	decs, err := loadDecisions(ctx, tx, head.RequestID)
	if err != nil {
		return nil, err
	}
	var wire []struct {
		At       time.Time                            `json:"at"`
		By       oapi.Actor                           `json:"by"`
		Comment  *string                              `json:"comment,omitempty"`
		Decision oapi.ApprovalDetailDecisionsDecision `json:"decision"`
		HashSeen string                               `json:"hash_seen"`
	}
	for _, d := range decs {
		item := struct {
			At       time.Time                            `json:"at"`
			By       oapi.Actor                           `json:"by"`
			Comment  *string                              `json:"comment,omitempty"`
			Decision oapi.ApprovalDetailDecisionsDecision `json:"decision"`
			HashSeen string                               `json:"hash_seen"`
		}{
			At: d.DecidedAt, By: oapi.Actor{Id: d.ActorID, Name: d.ActorName},
			Decision: oapi.ApprovalDetailDecisionsDecision(d.Decision), HashSeen: d.HashSeen,
		}
		if d.Reason != "" {
			reason := d.Reason
			item.Comment = &reason
		}
		wire = append(wire, item)
	}
	snap := map[string]any{}
	if len(head.Snapshot) > 0 {
		_ = json.Unmarshal(head.Snapshot, &snap)
	}
	detail := &oapi.ApprovalDetail{
		AllowedActions: card.AllowedActions, Amount: card.Amount, DeepLink: card.DeepLink,
		DocNumber: card.DocNumber, DocType: card.DocType, FraudHints: card.FraudHints, Party: card.Party,
		RequestId: card.RequestId, Requester: card.Requester, Severity: card.Severity, StateVersion: card.StateVersion,
		WaitingSince: card.WaitingSince, State: oapi.ApprovalState(head.State), Snapshot: snap, SnapshotHash: head.ContentHash,
	}
	if wire != nil {
		detail.Decisions = &wire
	}
	return detail, nil
}

func (s *Service) bucket(ctx context.Context, tx pgx.Tx, p rls.Principal, head requestRow) (string, oapi.ApprovalCard, error) {
	card, canAct, err := s.cardFor(ctx, tx, p, head)
	if err != nil {
		return "", oapi.ApprovalCard{}, err
	}
	if canAct && !terminal(head.State) {
		return "needs_me", card, nil
	}
	if p.UserID == head.InitiatorID && head.State == stateDelegated {
		return "fyi", card, nil
	}
	if p.UserID == head.InitiatorID && !terminal(head.State) {
		return "waiting_on_others", card, nil
	}
	decs, err := loadDecisions(ctx, tx, head.RequestID)
	if err != nil {
		return "", oapi.ApprovalCard{}, err
	}
	for _, d := range decs {
		if d.ActorID == p.UserID && !terminal(head.State) {
			return "waiting_on_others", card, nil
		}
	}
	return "", card, nil
}

func (s *Service) cardFor(ctx context.Context, tx pgx.Tx, p rls.Principal, head requestRow) (oapi.ApprovalCard, bool, error) {
	canAct := false
	actor, gerr := s.Dir.Get(ctx, p.CompanyID, p.UserID)
	if gerr == nil {
		err := s.eligible(ctx, tx, p, actor, head, "approve")
		switch {
		case err == nil:
			canAct = true
		case !isAPIErr(err):
			return oapi.ApprovalCard{}, false, err
		}
	}
	cfg, err := loadFraudConfig(ctx, tx, p.CompanyID)
	if err != nil {
		return oapi.ApprovalCard{}, false, err
	}
	hs := hints(ctx, head.Amount, factsOf(head.Fraud), cfg, p.UserID)
	acts := []oapi.AllowedAction{oapi.Open}
	if canAct && !terminal(head.State) {
		acts = []oapi.AllowedAction{oapi.Approve, oapi.Reject, oapi.Open}
		if head.Stage == stageBelow || head.Stage == stageFirst || head.Stage == stageVote {
			acts = append(acts, oapi.Delegate)
		}
	}
	return oapi.ApprovalCard{
		AllowedActions: acts,
		Amount:         oapi.Money{Amount: head.Amount, Currency: head.Currency},
		DeepLink:       "smarterp://approval/" + head.RequestID,
		DocNumber:      head.DocNumber,
		DocType:        head.DocType,
		FraudHints:     hs,
		Party:          head.Party,
		RequestId:      head.RequestID,
		Requester:      oapi.Actor{Id: head.InitiatorID, Name: head.InitiatorName},
		Severity:       cardSeverity(head.DocType, head.ThresholdCrossed, len(hs)),
		StateVersion:   int(head.StateVersion),
		WaitingSince:   head.WaitingSince,
	}, canAct, nil
}

func ruled(ctx context.Context, code apierr.Code, key string) *apierr.Error {
	return apierr.New(code, t(ctx, key)).WithDetails(map[string]any{"rule": key})
}

func codeOf(err error) string {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		return "refused"
	}
	if ae.Details != nil {
		if rule, ok := ae.Details["rule"].(string); ok && rule != "" {
			return rule
		}
	}
	switch ae.Code {
	case apierr.SoDViolation:
		return "sod"
	case apierr.StepUpRequired:
		return "step_up"
	case apierr.PermissionDenied:
		return "not_eligible"
	case apierr.ValidationError:
		return "validation"
	default:
		return string(ae.Code)
	}
}

func isAPIErr(err error) bool {
	var ae *apierr.Error
	return errors.As(err, &ae)
}

func dbNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at)
	return at, err
}
