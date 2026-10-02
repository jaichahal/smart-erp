package masters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the master-data API. Approval decisions go through the approval
// engine; this service applies the version only after that decision.
type Service struct {
	Pool      *pgxpool.Pool
	Approvals *approvals.Service
}

// New wires the approval engine. A bank-change step-up uses the platform stub token.
func New(pool *pgxpool.Pool, jobs *river.Client[pgx.Tx]) *Service {
	return &Service{Pool: pool, Approvals: approvals.New(pool, jobs, approvals.NewSQLDirectory(pool), approvals.StubStepUp{}, nil)}
}

type row map[string]any

type proposal struct {
	DocType string
	Subject uuid.UUID
	Match   int64
	Party   string
	Payload any
	New     bool
	Insert  func(ctx context.Context, tx pgx.Tx, requestID string) error
}

func (s *Service) propose(ctx context.Context, p rls.Principal, pr proposal) (row, error) {
	if err := s.ensureActor(ctx, p); err != nil {
		return nil, err
	}
	if err := s.ensureMatrix(ctx, p, pr.DocType); err != nil {
		return nil, err
	}
	raw, snap, err := snapshot(pr.Payload)
	if err != nil {
		return nil, err
	}
	out, err := s.Approvals.Submit(ctx, p, approvals.SubmitInput{
		DocID: pr.Subject.String(), DocType: pr.DocType, DocNumber: pr.Party, Party: pr.Party,
		Amount: "0", Currency: "AED", Snapshot: snap,
	})
	if err != nil {
		return nil, err
	}
	req := out.Decision.RequestId
	version := pr.Match
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if pr.New {
			if pr.Match != 0 {
				return apierr.New(apierr.ValidationError, "If-Match must be 0 when creating master data")
			}
			if err := pr.Insert(ctx, tx, req); err != nil {
				return err
			}
			version = 1
		} else {
			current, err := headVersion(ctx, tx, pr.DocType, pr.Subject)
			if err != nil {
				return err
			}
			if current != pr.Match {
				return apierr.New(apierr.Conflict, "state_version mismatch").WithDetails(row{"current_state_version": current})
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.master_changes WHERE company_id=$1 AND subject_id=$2 AND status='pending_approval'`, p.CompanyID, pr.Subject).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return apierr.New(apierr.Conflict, "a change is already waiting for approval")
			}
			version = current
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.master_changes
			(id, company_id, doc_type, subject_id, approval_request_id, status, payload, created_by, state_version)
			VALUES ($1,$2,$3,$4,$5,'pending_approval',$6,$7,$8)`,
			uuid.New(), p.CompanyID, pr.DocType, pr.Subject, req, raw, p.UserID, version)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": pr.Subject.String(), "status": "pending_approval", "approval_request_id": req, "state_version": version}, nil
}

func (s *Service) Decide(ctx context.Context, p rls.Principal, requestID string, match int64, decision, reason, stepUp, _ string) (row, error) {
	var docType string
	var subject uuid.UUID
	var payload []byte
	var status string
	var seen int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT doc_type, subject_id, payload, status, state_version
			FROM erp.master_changes WHERE company_id=$1 AND approval_request_id=$2`, p.CompanyID, requestID).
			Scan(&docType, &subject, &payload, &status, &seen)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "approval request not found")
	}
	if err != nil {
		return nil, err
	}
	if status == "applied" || status == "rejected" {
		return s.view(ctx, p, docType, subject)
	}
	if match != seen {
		return nil, apierr.New(apierr.Conflict, "state_version mismatch").WithDetails(row{"current_state_version": seen})
	}
	if decision == "delegate" {
		return nil, apierr.New(apierr.PermissionDenied, "this approval is not delegable")
	}
	if decision == "approve" && executiveDoc(docType) {
		ok, err := s.isExecutive(ctx, p)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(apierr.PermissionDenied, "only a CFO or Partner can approve this")
		}
	}
	if err := s.ensureActor(ctx, p); err != nil {
		return nil, err
	}
	var out *approvals.Outcome
	switch decision {
	case "approve":
		out, err = s.Approvals.Approve(ctx, p, approvals.DecisionInput{RequestID: requestID, StateVersion: 1, Reason: reason, StepUpToken: stepUp})
	case "reject":
		out, err = s.Approvals.Reject(ctx, p, approvals.DecisionInput{RequestID: requestID, StateVersion: 1, Reason: reason, StepUpToken: stepUp})
	default:
		return nil, apierr.New(apierr.ValidationError, "decision must be approve, reject, or delegate")
	}
	if err != nil {
		return nil, err
	}
	state := ""
	if out != nil {
		state = string(out.Decision.State)
	}
	if state == "rejected" {
		if err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE erp.master_changes SET status='rejected' WHERE approval_request_id=$1 AND status='pending_approval'`, requestID)
			return err
		}); err != nil {
			return nil, err
		}
		return row{"id": subject.String(), "status": "rejected", "state_version": seen}, nil
	}
	if state != "approved" {
		return s.view(ctx, p, docType, subject)
	}
	var approved row
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT status FROM erp.master_changes WHERE approval_request_id=$1 FOR UPDATE`, requestID).Scan(&cur); err != nil {
			return err
		}
		if cur == "applied" {
			var err error
			approved, err = head(ctx, tx, docType, subject)
			return err
		}
		if executiveDoc(docType) {
			if err := guardExecutive(ctx, tx, p, requestID); err != nil {
				return err
			}
		}
		if err := applyDoc(ctx, tx, p, docType, subject, requestID, payload, seen); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.master_changes SET status='applied' WHERE approval_request_id=$1`, requestID); err != nil {
			return err
		}
		if _, err := audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "config.changed", ReferenceType: docType, ReferenceID: subject.String(), Reason: reason,
			After: row{"approval_request_id": requestID, "status": "approved"},
		}); err != nil {
			return err
		}
		approved, err = head(ctx, tx, docType, subject)
		return err
	})
	if err != nil {
		return nil, err
	}
	return approved, nil
}

func guardExecutive(ctx context.Context, tx pgx.Tx, p rls.Principal, requestID string) error {
	var delegated int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.approval_requests WHERE request_id=$1 AND delegate_user_id <> ''`, requestID).Scan(&delegated); err != nil {
		return err
	}
	if delegated > 0 {
		return apierr.New(apierr.PermissionDenied, "this approval is not delegable")
	}
	var actor string
	if err := tx.QueryRow(ctx, `SELECT actor_id FROM erp.approval_decisions WHERE request_id=$1 AND decision='approved' ORDER BY decided_at DESC LIMIT 1`, requestID).Scan(&actor); err != nil {
		return err
	}
	if actor != p.UserID || !hasRole(p.Roles, "stakeholder") {
		return apierr.New(apierr.PermissionDenied, "only a CFO or Partner can approve this")
	}
	var titled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM erp.executive_titles WHERE company_id=$1 AND user_id=$2 AND active AND title IN ('cfo','partner')
	)`, p.CompanyID, p.UserID).Scan(&titled); err != nil {
		return err
	}
	if !titled {
		return apierr.New(apierr.PermissionDenied, "only a CFO or Partner can approve this")
	}
	return nil
}

func (s *Service) ensureActor(ctx context.Context, p rls.Principal) error {
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.approval_actors (company_id, user_id, name, department, roles)
			VALUES ($1,$2,$3,'',$4)
			ON CONFLICT (company_id, user_id) DO UPDATE SET roles = EXCLUDED.roles`,
			p.CompanyID, p.UserID, p.UserID, p.Roles)
		return err
	})
}

func (s *Service) ensureMatrix(ctx context.Context, p rls.Principal, docType string) error {
	roles := []string{"approver", "stakeholder", "credit_controller"}
	step := false
	switch docType {
	case docVendor, docVendorSKU, docBlacklist:
		roles = []string{"stakeholder"}
	case docBank:
		roles = []string{"approver", "stakeholder"}
		step = true
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.approval_matrix
			(company_id, doc_type, threshold_amount, threshold_currency, below_mode, above_mode,
			 approver_roles_below, first_approver_roles, final_gate_role, vote_n, requires_step_up_above)
			VALUES ($1,$2,'1000000000','AED','any_one','first_then_final',$3,$3,'stakeholder',0,$4)
			ON CONFLICT (company_id, doc_type) DO NOTHING`, p.CompanyID, docType, roles, step)
		return err
	})
}

func (s *Service) isExecutive(ctx context.Context, p rls.Principal) (bool, error) {
	if !hasRole(p.Roles, "stakeholder") {
		return false, nil
	}
	var ok bool
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM erp.executive_titles
			WHERE company_id=$1 AND user_id=$2 AND active AND title IN ('cfo','partner')
		)`, p.CompanyID, p.UserID).Scan(&ok)
	})
	return ok, err
}

func (s *Service) GrantTitle(ctx context.Context, p rls.Principal, userID, title string) (row, error) {
	title = strings.ToLower(strings.TrimSpace(title))
	if title != "cfo" && title != "partner" {
		return nil, apierr.New(apierr.ValidationError, "title must be cfo or partner")
	}
	if userID == "" {
		return nil, apierr.New(apierr.ValidationError, "user_id is required")
	}
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var stakeholder bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM erp.user_roles ur
			JOIN erp.users u ON u.id = ur.user_id
			WHERE u.id=$1 AND u.company_id=$2 AND ur.role_name='stakeholder' AND ur.active
		)`, userID, p.CompanyID).Scan(&stakeholder); err != nil {
			return err
		}
		if !stakeholder {
			return apierr.New(apierr.ValidationError, "CFO and Partner titles are set on users who hold Stakeholder")
		}
		var already bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM erp.executive_titles WHERE company_id=$1 AND user_id=$2 AND title=$3 AND active
		)`, p.CompanyID, userID, title).Scan(&already); err != nil {
			return err
		}
		if already {
			return nil
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.executive_titles WHERE company_id=$1 AND active`, p.CompanyID).Scan(&active); err != nil {
			return err
		}
		if active == 0 {
			if !hasRole(p.Roles, "system_manager", "system") {
				return apierr.New(apierr.PermissionDenied, "the first CFO or Partner is configured by a system manager")
			}
		} else {
			ok, err := titled(ctx, tx, p)
			if err != nil {
				return err
			}
			if !ok {
				return apierr.New(apierr.PermissionDenied, "only a current CFO or Partner can change this list")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.executive_titles (company_id, user_id, title, granted_by)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (company_id, user_id, title) DO UPDATE SET active=true, granted_by=EXCLUDED.granted_by, granted_at=clock_timestamp()`,
			p.CompanyID, userID, title, p.UserID); err != nil {
			return err
		}
		_, err := audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "config.changed", ReferenceType: "executive_title", ReferenceID: userID, Reason: "grant " + title,
			After: row{"user_id": userID, "title": title},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"user_id": userID, "title": title}, nil
}

func titled(ctx context.Context, tx pgx.Tx, p rls.Principal) (bool, error) {
	if !hasRole(p.Roles, "stakeholder") {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM erp.executive_titles WHERE company_id=$1 AND user_id=$2 AND active AND title IN ('cfo','partner')
	)`, p.CompanyID, p.UserID).Scan(&ok)
	return ok, err
}

func headVersion(ctx context.Context, tx pgx.Tx, docType string, id uuid.UUID) (int64, error) {
	q, err := headSQL(docType)
	if err != nil {
		return 0, err
	}
	var v int64
	err = tx.QueryRow(ctx, q, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apierr.New(apierr.NotFound, "master record not found")
	}
	return v, err
}

func headSQL(docType string) (string, error) {
	switch docType {
	case docCustomer:
		return `SELECT state_version FROM erp.customers WHERE id=$1 FOR UPDATE`, nil
	case docVendor, docVendorEdit, docBank, docVendorSKU, docBlacklist:
		return `SELECT state_version FROM erp.vendors WHERE id=$1 FOR UPDATE`, nil
	case docSKU:
		return `SELECT state_version FROM erp.skus WHERE id=$1 FOR UPDATE`, nil
	case docBOM:
		return `SELECT state_version FROM erp.boms WHERE id=$1 FOR UPDATE`, nil
	case docPriceList:
		return `SELECT state_version FROM erp.price_lists WHERE id=$1 FOR UPDATE`, nil
	case docAgreement:
		return `SELECT state_version FROM erp.price_agreements WHERE id=$1 FOR UPDATE`, nil
	case docTerms:
		return `SELECT state_version FROM erp.payment_terms WHERE id=$1 FOR UPDATE`, nil
	default:
		return "", fmt.Errorf("masters: unknown doc type %s", docType)
	}
}

func bumpSQL(docType string) (string, error) {
	switch docType {
	case docCustomer:
		return `UPDATE erp.customers SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docVendor, docVendorEdit, docBank, docVendorSKU, docBlacklist:
		return `UPDATE erp.vendors SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docSKU:
		return `UPDATE erp.skus SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docBOM:
		return `UPDATE erp.boms SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docPriceList:
		return `UPDATE erp.price_lists SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docAgreement:
		return `UPDATE erp.price_agreements SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	case docTerms:
		return `UPDATE erp.payment_terms SET state_version = state_version + 1 WHERE id=$1 AND state_version=$2`, nil
	default:
		return "", fmt.Errorf("masters: unknown doc type %s", docType)
	}
}

func snapshot(v any) ([]byte, map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, err
	}
	return raw, m, nil
}

func hasRole(roles []string, want ...string) bool {
	for _, r := range roles {
		for _, w := range want {
			if strings.EqualFold(strings.TrimSpace(r), w) {
				return true
			}
		}
	}
	return false
}

func mustMoney(s, field string) (string, error) {
	r, ok := rat(s)
	if !ok || r.Sign() < 0 {
		return "", apierr.New(apierr.ValidationError, field+" must be a non-negative decimal")
	}
	return quantize(r, moneyScale), nil
}

func mustQty(s, field string) (string, error) {
	r, ok := rat(s)
	if !ok || r.Sign() <= 0 {
		return "", apierr.New(apierr.ValidationError, field+" must be a positive decimal")
	}
	return quantize(r, qtyScale), nil
}

func nullUUID(s string) any {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return id
}

func jsonList(v any) []byte {
	if v == nil {
		return []byte(`[]`)
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(`[]`)
	}
	return b
}
