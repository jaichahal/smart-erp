package authz

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type conflict struct {
	Kind  string
	Left  string
	Right string
}

// AssignRoles replaces the user's active roles. An incompatible pair from the
// active SoD matrix is refused and audited unless overrideApprovalID is granted
// by OverrideApprover (A14, R1.13).
func (s *Service) AssignRoles(ctx context.Context, actor rls.Principal, userID string, roles []string, overrideApprovalID string) error {
	clean, err := normalizeRoleNames(roles)
	if err != nil {
		return s.fail(ctx, apierr.ValidationError, "validation.role")
	}
	var conflicts []conflict
	err = rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "user:write", "company"); err != nil {
			return err
		}
		if err := s.knownRoles(ctx, tx, clean); err != nil {
			return err
		}
		found, err := s.conflicts(ctx, tx, clean)
		if err != nil {
			return err
		}
		conflicts = found
		granted := false
		if len(conflicts) > 0 && overrideApprovalID != "" {
			ok, gerr := s.overrides.Granted(ctx, overrideApprovalID, userID, clean)
			if gerr != nil {
				return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), gerr)
			}
			granted = ok
		}
		if len(conflicts) > 0 && !granted {
			return errSoDRefusal
		}
		before, err := activeRoles(ctx, tx, userID)
		if err != nil {
			return err
		}
		if before == nil {
			before = []string{}
		}
		if err := s.replaceRoles(ctx, tx, userID, clean, overrideApprovalID); err != nil {
			return err
		}
		if _, err := audit.EmitAs(ctx, tx, actor, audit.Event{
			Type:          "role.changed",
			ReferenceType: "user",
			ReferenceID:   userID,
			Before:        map[string]any{"roles": before},
			After:         map[string]any{"roles": clean, "approval_id": overrideApprovalID},
		}); err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if s.river != nil {
			if _, err := outbox.InsertTx(ctx, s.river, tx, RoleChangedArgs{
				CompanyID:  actor.CompanyID.String(),
				UserID:     userID,
				Roles:      clean,
				ApprovalID: overrideApprovalID,
			}, nil); err != nil {
				return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
			}
		}
		return nil
	})
	if errors.Is(err, errSoDRefusal) {
		if _, aerr := audit.EmitCommitted(ctx, s.pool, actor, audit.Event{
			Type:          "sod.assignment_refused",
			ReferenceType: "user",
			ReferenceID:   userID,
			After: map[string]any{
				"roles":       clean,
				"conflicts":   conflictMaps(conflicts),
				"approval_id": overrideApprovalID,
			},
			Reason: "sod",
		}); aerr != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), aerr)
		}
		return s.fail(ctx, apierr.SoDViolation, "sod.violation")
	}
	return err
}

func conflictMaps(in []conflict) []map[string]string {
	out := make([]map[string]string, len(in))
	for i, c := range in {
		out[i] = map[string]string{"kind": c.Kind, "left": c.Left, "right": c.Right}
	}
	return out
}

func normalizeRoleNames(roles []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" || role == "system" {
			return nil, errBadRole
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	sort.Strings(out)
	return out, nil
}

var errSoDRefusal = errors.New("sod refusal")

var errBadRole = errRole{}

type errRole struct{}

func (errRole) Error() string { return "unrecognised role" }

func (s *Service) knownRoles(ctx context.Context, tx pgx.Tx, roles []string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.roles WHERE name = ANY($1) AND name <> 'system'`, roles).Scan(&n); err != nil {
		return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	if n != len(roles) {
		return s.fail(ctx, apierr.ValidationError, "validation.role")
	}
	return nil
}

func (s *Service) conflicts(ctx context.Context, tx pgx.Tx, roles []string) ([]conflict, error) {
	rows, err := tx.Query(ctx, `SELECT kind, left_code, right_code FROM erp.sod_conflicts($1)`, roles)
	if err != nil {
		return nil, apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	defer rows.Close()
	var out []conflict
	for rows.Next() {
		var c conflict
		if err := rows.Scan(&c.Kind, &c.Left, &c.Right); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func activeRoles(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT role_name FROM erp.user_roles WHERE user_id = $1 AND active ORDER BY role_name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Service) replaceRoles(ctx context.Context, tx pgx.Tx, userID string, roles []string, approvalID string) error {
	if _, err := tx.Exec(ctx, `UPDATE erp.user_roles SET active = false WHERE user_id = $1 AND NOT (role_name = ANY($2))`, userID, roles); err != nil {
		return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.user_roles (user_id, role_name, active, override_approval_id)
			VALUES ($1, $2, true, NULLIF($3, ''))
			ON CONFLICT (user_id, role_name) DO UPDATE
			SET active = true, override_approval_id = NULLIF($3, ''), granted_at = clock_timestamp()`,
			userID, role, approvalID); err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
	}
	return nil
}
