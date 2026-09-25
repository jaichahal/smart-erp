package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// AccessReview is one quarterly report: every user, their roles, and last login.
type AccessReview struct {
	ID         string
	Period     string
	GraceUntil time.Time
	Items      []AccessReviewItem
}

// AccessReviewItem is one user on the review.
type AccessReviewItem struct {
	UserID    string
	Roles     []string
	LastLogin *time.Time
	Confirmed bool
}

// Exception is one exceptions-report row.
type Exception struct {
	Kind      string
	SubjectID string
	Detail    json.RawMessage
}

// RunScheduledAccessReview is the function the scheduler calls. It opens the
// quarter's report and flags users still unconfirmed after the grace period.
func (s *Service) RunScheduledAccessReview(ctx context.Context, company rls.Principal, asOf time.Time) (AccessReview, error) {
	review, err := s.openReview(ctx, company, asOf.UTC())
	if err != nil {
		return AccessReview{}, err
	}
	if err := s.flagExceptions(ctx, company, asOf.UTC()); err != nil {
		return AccessReview{}, err
	}
	return review, nil
}

// RunScheduledAccessReviewForCompany is the scheduler entry point. It runs as
// the system principal for one company.
func (s *Service) RunScheduledAccessReviewForCompany(ctx context.Context, companyID uuid.UUID, asOf time.Time) (AccessReview, error) {
	actor := rls.System
	actor.CompanyID = companyID
	return s.RunScheduledAccessReview(ctx, actor, asOf)
}

func (s *Service) openReview(ctx context.Context, actor rls.Principal, asOf time.Time) (AccessReview, error) {
	period := fmt.Sprintf("%dQ%d", asOf.Year(), (int(asOf.Month())-1)/3+1)
	var review AccessReview
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "access_review:write", "company"); err != nil {
			return err
		}
		var days int
		if err := tx.QueryRow(ctx, `SELECT value::int FROM erp.authz_settings WHERE key = 'access_review_grace_days'`).Scan(&days); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.access_reviews (company_id, period, opened_at, grace_until)
			VALUES (erp.current_company(), $1, $2::timestamptz, $2::timestamptz + ($3 * interval '1 day'))
			ON CONFLICT (company_id, period) DO NOTHING`, period, asOf, days); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT id::text, period, grace_until FROM erp.access_reviews
			WHERE period = $1 AND company_id IS NOT DISTINCT FROM erp.current_company()`, period).
			Scan(&review.ID, &review.Period, &review.GraceUntil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.access_review_items (review_id, user_id, roles, last_login_at)
			SELECT $1, u.id,
			       COALESCE((
			         SELECT array_agg(ur.role_name ORDER BY ur.role_name)
			         FROM erp.user_roles ur WHERE ur.user_id = u.id AND ur.active
			       ), '{}'),
			       u.last_login_at
			FROM erp.users u
			WHERE u.company_id IS NOT DISTINCT FROM erp.current_company()
			ON CONFLICT (review_id, user_id) DO NOTHING`, review.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT user_id, roles, last_login_at, confirmed_at IS NOT NULL
			FROM erp.access_review_items WHERE review_id = $1 ORDER BY user_id`, review.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item AccessReviewItem
			if err := rows.Scan(&item.UserID, &item.Roles, &item.LastLogin, &item.Confirmed); err != nil {
				return err
			}
			if item.Roles == nil {
				item.Roles = []string{}
			}
			review.Items = append(review.Items, item)
		}
		return rows.Err()
	})
	return review, err
}

// ConfirmAccessReview records a Stakeholder confirmation for one user.
func (s *Service) ConfirmAccessReview(ctx context.Context, actor rls.Principal, reviewID, userID string) error {
	return rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "access_review:confirm", ""); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE erp.access_review_items
			SET confirmed_at = COALESCE(confirmed_at, clock_timestamp()),
			    confirmed_by = COALESCE(confirmed_by, erp.current_user_id())
			WHERE review_id = $1 AND user_id = $2`, reviewID, userID)
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if tag.RowsAffected() == 0 {
			return s.fail(ctx, apierr.NotFound, "not_found")
		}
		return nil
	})
}

func (s *Service) flagExceptions(ctx context.Context, actor rls.Principal, asOf time.Time) error {
	return rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "exception:write", "company"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.exceptions (company_id, month, kind, subject_id, detail)
			SELECT r.company_id, date_trunc('month', $1::timestamptz)::date,
			       'access_review_unconfirmed', i.user_id,
			       jsonb_build_object('user_id', i.user_id, 'roles', to_jsonb(i.roles), 'last_login_at', i.last_login_at)
			FROM erp.access_review_items i
			JOIN erp.access_reviews r ON r.id = i.review_id
			WHERE i.confirmed_at IS NULL
			  AND r.grace_until <= $1
			  AND r.company_id IS NOT DISTINCT FROM erp.current_company()
			ON CONFLICT (company_id, month, kind, subject_id) DO NOTHING`, asOf)
		return err
	})
}

// ListExceptions returns exceptions for the month containing asOf.
func (s *Service) ListExceptions(ctx context.Context, actor rls.Principal, asOf time.Time) ([]Exception, error) {
	var out []Exception
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		if err := s.requireScope(ctx, tx, "exception:read", ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT kind, subject_id, detail FROM erp.exceptions
			WHERE month = date_trunc('month', $1::timestamptz)::date
			ORDER BY subject_id`, asOf)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ex Exception
			if err := rows.Scan(&ex.Kind, &ex.SubjectID, &ex.Detail); err != nil {
				return err
			}
			out = append(out, ex)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Exception{}
	}
	return out, err
}
