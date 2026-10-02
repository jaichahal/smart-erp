package authz

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Customer is one row of the territory-scoped customer list.
type Customer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CustomerPage is one page of customers plus the total over the same filter.
type CustomerPage struct {
	Rows       []Customer
	Total      int
	NextCursor *string
}

// ListCustomers returns the caller's visible customers. Total counts the same
// rows the pages walk, under the same row-level security session (A12, R1.11).
func (s *Service) ListCustomers(ctx context.Context, actor rls.Principal, cursor string, limit int) (CustomerPage, error) {
	limit = clampLimit(limit)
	var page CustomerPage
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		scope, err := s.scope(ctx, tx, "customer:read")
		if err != nil {
			return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
		}
		if scope == "" {
			return s.fail(ctx, apierr.PermissionDenied, "permission.denied")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.customers`).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, name FROM erp.customers
			WHERE ($1::uuid IS NULL OR id > $1::uuid)
			ORDER BY id
			LIMIT $2`, nullUUID(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Customer
			if err := rows.Scan(&c.ID, &c.Name); err != nil {
				return err
			}
			page.Rows = append(page.Rows, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Rows) > limit {
			next := page.Rows[limit-1].ID
			page.Rows = page.Rows[:limit]
			page.NextCursor = &next
		}
		if page.Rows == nil {
			page.Rows = []Customer{}
		}
		return nil
	})
	return page, err
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func nullUUID(cursor string) *uuid.UUID {
	if cursor == "" {
		return nil
	}
	id, err := uuid.Parse(cursor)
	if err != nil {
		return nil
	}
	return &id
}
