package authz

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// DirectoryUser is a user as history and identity see them. Disabled users stay.
type DirectoryUser struct {
	ID          string
	Name        string
	CompanyID   uuid.UUID
	Roles       []string
	Disabled    bool
	LastLogin   *time.Time
	TerritoryID *uuid.UUID
}

// NewUser is the directory row to insert. There is no delete operation.
type NewUser struct {
	ID          string
	Name        string
	TerritoryID *uuid.UUID
	WarehouseID *uuid.UUID
	LastLogin   *time.Time
}

// CreateUser inserts a user. Callers who lack user:write are refused.
func (s *Service) CreateUser(ctx context.Context, actor rls.Principal, in NewUser) error {
	if in.ID == "" || in.Name == "" {
		return s.fail(ctx, apierr.ValidationError, "validation.field")
	}
	return rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "user:write", "company"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.users (id, company_id, name, territory_id, warehouse_id, last_login_at)
			VALUES ($1, erp.current_company(), $2, $3, $4, $5)`,
			in.ID, in.Name, in.TerritoryID, in.WarehouseID, in.LastLogin)
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		return nil
	})
}

// ResolveUser returns the user, including when they are disabled (A15, R1.9).
func (s *Service) ResolveUser(ctx context.Context, actor rls.Principal, userID string) (DirectoryUser, error) {
	var user DirectoryUser
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		var disabledAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT id, name, company_id, disabled_at, last_login_at, territory_id,
			       COALESCE((
			         SELECT array_agg(role_name ORDER BY role_name)
			         FROM erp.user_roles
			         WHERE user_id = erp.users.id AND active
			       ), '{}')
			FROM erp.users
			WHERE id = $1`, userID).Scan(
			&user.ID, &user.Name, &user.CompanyID, &disabledAt, &user.LastLogin, &user.TerritoryID, &user.Roles)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.fail(ctx, apierr.NotFound, "not_found")
		}
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		user.Disabled = disabledAt != nil
		if user.Roles == nil {
			user.Roles = []string{}
		}
		return nil
	})
	return user, err
}

// CanAuthenticate is false for an unknown or disabled user and true otherwise (A15).
func (s *Service) CanAuthenticate(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT erp.user_can_authenticate($1)`, userID).Scan(&ok)
	if err != nil {
		return false, apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	return ok, nil
}

// DisableUser marks the user disabled, writes the audit event, and enqueues
// user.disabled. It does not remove the row.
func (s *Service) DisableUser(ctx context.Context, actor rls.Principal, userID string) error {
	return rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "user:write", "company"); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE erp.users
			SET disabled_at = COALESCE(disabled_at, clock_timestamp()), state_version = state_version + 1
			WHERE id = $1 AND company_id IS NOT DISTINCT FROM erp.current_company()`, userID)
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if tag.RowsAffected() == 0 {
			return s.fail(ctx, apierr.NotFound, "not_found")
		}
		if _, err := audit.EmitAs(ctx, tx, actor, audit.Event{
			Type:          "user.disabled",
			ReferenceType: "user",
			ReferenceID:   userID,
			After:         map[string]any{"disabled": true},
		}); err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if s.river != nil {
			if _, err := outbox.InsertTx(ctx, s.river, tx, UserDisabledArgs{
				CompanyID: actor.CompanyID.String(),
				UserID:    userID,
			}, nil); err != nil {
				return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
			}
		}
		return nil
	})
}
