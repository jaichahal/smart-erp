package authz

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// RulePage is one page of SoD rules and the total visible to the caller.
type RulePage struct {
	Rules      []oapi.SodRule
	Total      int
	NextCursor *string
}

// MatrixPage is one page of approval-matrix rules.
type MatrixPage struct {
	Rules      []oapi.ApprovalMatrixRule
	Total      int
	NextCursor *string
}

// ListSodRules returns the matrix rows the caller may read.
func (s *Service) ListSodRules(ctx context.Context, actor rls.Principal, cursor string, limit int) (RulePage, error) {
	limit = clampLimit(limit)
	var page RulePage
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "sod:read", ""); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.sod_rules`).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, kind, left_code, right_code, state_version, status, approval_request_id
			FROM erp.sod_rules
			WHERE ($1::uuid IS NULL OR id > $1::uuid)
			ORDER BY id
			LIMIT $2`, nullUUID(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanSod(rows)
			if err != nil {
				return err
			}
			page.Rules = append(page.Rules, rule)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.Rules, page.NextCursor = trimSod(page.Rules, limit)
		return nil
	})
	return page, err
}

// SaveSodRule stores a company rule as pending_approval. If-Match is the
// state version, or 0 when the company does not yet have this pair.
func (s *Service) SaveSodRule(ctx context.Context, actor rls.Principal, expected int, in oapi.SodRuleWrite) (oapi.SodRule, error) {
	if !in.Kind.Valid() || in.LeftCode == "" || in.RightCode == "" || in.LeftCode == in.RightCode {
		return oapi.SodRule{}, s.fail(ctx, apierr.ValidationError, "validation.field")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return oapi.SodRule{}, s.fail(ctx, apierr.ValidationError, "validation.body")
	}
	var saved oapi.SodRule
	err = rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "sod:write", "company"); err != nil {
			return err
		}
		current, found, err := s.findSod(ctx, tx, string(in.Kind), in.LeftCode, in.RightCode)
		if err != nil {
			return err
		}
		if !found {
			if err := s.versionMismatch(ctx, int64(expected), 0, nil); err != nil {
				return err
			}
			requestID, err := s.matrix.Submit(ctx, actor.CompanyID, "sod_rule", payload)
			if err != nil {
				return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
			}
			id := uuid.New()
			if err := tx.QueryRow(ctx, `
				INSERT INTO erp.sod_rules (id, company_id, kind, left_code, right_code, state_version, status, approval_request_id)
				VALUES ($1, erp.current_company(), $2, $3, $4, 1, 'pending_approval', $5)
				RETURNING id, kind, left_code, right_code, state_version, status, approval_request_id`,
				id, string(in.Kind), in.LeftCode, in.RightCode, requestID).Scan(
				scanSodDest(&saved)...); err != nil {
				return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
			}
			return nil
		}
		if err := s.versionMismatch(ctx, int64(expected), int64(current.StateVersion), current); err != nil {
			return err
		}
		requestID, err := s.matrix.Submit(ctx, actor.CompanyID, "sod_rule", payload)
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if err := tx.QueryRow(ctx, `
			UPDATE erp.sod_rules
			SET state_version = state_version + 1, status = 'pending_approval', approval_request_id = $2
			WHERE id = $1
			RETURNING id, kind, left_code, right_code, state_version, status, approval_request_id`,
			uuid.UUID(current.Id), requestID).Scan(scanSodDest(&saved)...); err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		return nil
	})
	return saved, err
}

func (s *Service) findSod(ctx context.Context, tx pgx.Tx, kind, left, right string) (oapi.SodRule, bool, error) {
	var rule oapi.SodRule
	err := tx.QueryRow(ctx, `
		SELECT id, kind, left_code, right_code, state_version, status, approval_request_id
		FROM erp.sod_rules
		WHERE company_id IS NOT DISTINCT FROM erp.current_company()
		  AND kind = $1 AND left_code = $2 AND right_code = $3`, kind, left, right).Scan(scanSodDest(&rule)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return oapi.SodRule{}, false, nil
	}
	if err != nil {
		return oapi.SodRule{}, false, err
	}
	return rule, true, nil
}

func scanSod(rows pgx.Rows) (oapi.SodRule, error) {
	var rule oapi.SodRule
	err := rows.Scan(scanSodDest(&rule)...)
	return rule, err
}

func scanSodDest(rule *oapi.SodRule) []any {
	return []any{&rule.Id, &rule.Kind, &rule.LeftCode, &rule.RightCode, &rule.StateVersion, &rule.Status, &rule.ApprovalRequestId}
}

func trimSod(rules []oapi.SodRule, limit int) ([]oapi.SodRule, *string) {
	if len(rules) > limit {
		next := rules[limit-1].Id.String()
		return rules[:limit], &next
	}
	if rules == nil {
		rules = []oapi.SodRule{}
	}
	return rules, nil
}

// ListApprovalMatrix returns the rules the caller may read.
func (s *Service) ListApprovalMatrix(ctx context.Context, actor rls.Principal, cursor string, limit int) (MatrixPage, error) {
	limit = clampLimit(limit)
	var page MatrixPage
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "approval_matrix:read", ""); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.authz_approval_matrix`).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, document_type, threshold_amount, threshold_currency, below_threshold_role,
			       first_approver_role, final_gate_role, voting_any, voting_of, state_version, status, approval_request_id
			FROM erp.authz_approval_matrix
			WHERE ($1::uuid IS NULL OR id > $1::uuid)
			ORDER BY id
			LIMIT $2`, nullUUID(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanMatrix(rows)
			if err != nil {
				return err
			}
			page.Rules = append(page.Rules, rule)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.Rules, page.NextCursor = trimMatrix(page.Rules, limit)
		return nil
	})
	return page, err
}

// SaveApprovalMatrixRule stores a rule as pending_approval.
func (s *Service) SaveApprovalMatrixRule(ctx context.Context, actor rls.Principal, expected int, in oapi.ApprovalMatrixRuleWrite) (oapi.ApprovalMatrixRule, error) {
	if in.DocumentType == "" || in.BelowThresholdRole == "" || in.FirstApproverRole == "" || in.FinalGateRole == "" || !validMoney(in.Threshold) {
		return oapi.ApprovalMatrixRule{}, s.fail(ctx, apierr.ValidationError, "validation.field")
	}
	if (in.VotingAny == nil) != (in.VotingOf == nil) {
		return oapi.ApprovalMatrixRule{}, s.fail(ctx, apierr.ValidationError, "validation.field")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return oapi.ApprovalMatrixRule{}, s.fail(ctx, apierr.ValidationError, "validation.body")
	}
	var saved oapi.ApprovalMatrixRule
	err = rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "approval_matrix:write", "company"); err != nil {
			return err
		}
		var current oapi.ApprovalMatrixRule
		err := tx.QueryRow(ctx, `
			SELECT id, document_type, threshold_amount, threshold_currency, below_threshold_role,
			       first_approver_role, final_gate_role, voting_any, voting_of, state_version, status, approval_request_id
			FROM erp.authz_approval_matrix
			WHERE document_type = $1 AND company_id IS NOT DISTINCT FROM erp.current_company()`, in.DocumentType).Scan(scanMatrixDest(&current)...)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !found && expected != 0 {
			return s.versionMismatch(ctx, int64(expected), 0, nil)
		}
		if found {
			if err := s.versionMismatch(ctx, int64(expected), int64(current.StateVersion), current); err != nil {
				return err
			}
		}
		requestID, err := s.matrix.Submit(ctx, actor.CompanyID, "approval_matrix", payload)
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if !found {
			id := uuid.New()
			return tx.QueryRow(ctx, `
				INSERT INTO erp.authz_approval_matrix (
				    id, company_id, document_type, threshold_amount, threshold_currency,
				    below_threshold_role, first_approver_role, final_gate_role, voting_any, voting_of,
				    state_version, status, approval_request_id)
				VALUES ($1, erp.current_company(), $2, $3, $4, $5, $6, $7, $8, $9, 1, 'pending_approval', $10)
				RETURNING id, document_type, threshold_amount, threshold_currency, below_threshold_role,
				          first_approver_role, final_gate_role, voting_any, voting_of, state_version, status, approval_request_id`,
				id, in.DocumentType, in.Threshold.Amount, in.Threshold.Currency,
				in.BelowThresholdRole, in.FirstApproverRole, in.FinalGateRole, in.VotingAny, in.VotingOf, requestID).
				Scan(scanMatrixDest(&saved)...)
		}
		return tx.QueryRow(ctx, `
			UPDATE erp.authz_approval_matrix
			SET threshold_amount = $2, threshold_currency = $3, below_threshold_role = $4,
			    first_approver_role = $5, final_gate_role = $6, voting_any = $7, voting_of = $8,
			    state_version = state_version + 1, status = 'pending_approval', approval_request_id = $9
			WHERE id = $1
			RETURNING id, document_type, threshold_amount, threshold_currency, below_threshold_role,
			          first_approver_role, final_gate_role, voting_any, voting_of, state_version, status, approval_request_id`,
			uuid.UUID(current.Id), in.Threshold.Amount, in.Threshold.Currency,
			in.BelowThresholdRole, in.FirstApproverRole, in.FinalGateRole, in.VotingAny, in.VotingOf, requestID).
			Scan(scanMatrixDest(&saved)...)
	})
	if err != nil {
		return oapi.ApprovalMatrixRule{}, err
	}
	return saved, nil
}

func scanMatrix(rows pgx.Rows) (oapi.ApprovalMatrixRule, error) {
	var rule oapi.ApprovalMatrixRule
	err := rows.Scan(scanMatrixDest(&rule)...)
	return rule, err
}

func scanMatrixDest(rule *oapi.ApprovalMatrixRule) []any {
	return []any{
		&rule.Id, &rule.DocumentType, &rule.Threshold.Amount, &rule.Threshold.Currency,
		&rule.BelowThresholdRole, &rule.FirstApproverRole, &rule.FinalGateRole,
		&rule.VotingAny, &rule.VotingOf, &rule.StateVersion, &rule.Status, &rule.ApprovalRequestId,
	}
}

func trimMatrix(rules []oapi.ApprovalMatrixRule, limit int) ([]oapi.ApprovalMatrixRule, *string) {
	if len(rules) > limit {
		next := rules[limit-1].Id.String()
		return rules[:limit], &next
	}
	if rules == nil {
		rules = []oapi.ApprovalMatrixRule{}
	}
	return rules, nil
}

func (s *Service) versionMismatch(ctx context.Context, expected, actual int64, current any) error {
	err := ifmatch.Check(expected, actual, current)
	if err == nil {
		return nil
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		ae.Message = s.messages.text(langOf(ctx), "conflict.state")
	}
	return err
}

func validMoney(m oapi.Money) bool {
	if len(m.Currency) != 3 {
		return false
	}
	for _, c := range m.Currency {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	if m.Amount == "" {
		return false
	}
	dot := false
	for i, c := range m.Amount {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' && !dot {
			dot = true
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
