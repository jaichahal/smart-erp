package masters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type orderLineIn struct {
	SKUID     string `json:"sku_id"`
	Qty       string `json:"qty"`
	UOM       string `json:"uom"`
	UnitPrice string `json:"unit_price"`
}

type orderIn struct {
	CustomerID string        `json:"customer_id"`
	Lines      []orderLineIn `json:"lines"`
}

type overrideIn struct {
	Reason string `json:"reason"`
}

func (s *Service) CreateOrder(ctx context.Context, p rls.Principal, in orderIn) (row, error) {
	if len(in.Lines) == 0 {
		return nil, apierr.New(apierr.ValidationError, "lines are required")
	}
	id := uuid.New()
	var hold error
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var credit, custVersion, status string
		if err := tx.QueryRow(ctx, `SELECT c.status, coalesce(c.current_version_id::text,''), coalesce(v.credit_limit::text,'0')
			FROM erp.customers c
			LEFT JOIN erp.customer_versions v ON v.id = c.current_version_id
			WHERE c.id=$1`, in.CustomerID).Scan(&status, &custVersion, &credit); err != nil {
			return apierr.New(apierr.NotFound, "customer not found")
		}
		if status != "approved" || custVersion == "" {
			return apierr.New(apierr.ValidationError, "customer is not approved")
		}
		var exposure string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(total),0)::text FROM erp.commercial_orders
			WHERE customer_id=$1 AND status IN ('open','overridden')`, in.CustomerID).Scan(&exposure); err != nil {
			return err
		}
		total := newRat(0)
		type line struct {
			sku, version, qty, price, uom string
			n                             int
		}
		var lines []line
		floorHold := false
		for i, ln := range in.Lines {
			qty, err := mustQty(ln.Qty, "qty")
			if err != nil {
				return err
			}
			price, err := mustMoney(ln.UnitPrice, "unit_price")
			if err != nil {
				return err
			}
			var class, floor, skuVersion string
			if err := tx.QueryRow(ctx, `SELECT item_class, floor_price::text, coalesce(current_version_id::text,''), status FROM erp.skus WHERE id=$1`, ln.SKUID).
				Scan(&class, &floor, &skuVersion, &status); err != nil {
				return apierr.New(apierr.NotFound, "sku not found")
			}
			if status != "approved" || skuVersion == "" {
				return apierr.New(apierr.ValidationError, "sku is not approved")
			}
			if class == "raw_material" {
				return apierr.New(apierr.ValidationError, "a raw material is not sold")
			}
			qr, _ := rat(qty)
			pr, _ := rat(price)
			total = add(total, mul(qr, pr))
			var agreed string
			err = tx.QueryRow(ctx, `SELECT price::text FROM erp.price_agreements
				WHERE customer_id=$1 AND sku_id=$2 AND status='approved'
				  AND valid_from <= current_date AND valid_to >= current_date
				  AND min_qty <= $3::numeric AND max_qty >= $3::numeric
				ORDER BY price LIMIT 1`, in.CustomerID, ln.SKUID, qty).Scan(&agreed)
			covered := false
			if err == nil {
				ag, _ := rat(agreed)
				if pr.Cmp(ag) >= 0 {
					covered = true
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			fl, _ := rat(floor)
			if !covered && pr.Cmp(fl) < 0 {
				floorHold = true
			}
			uom := ln.UOM
			if uom == "" {
				uom = "EA"
			}
			lines = append(lines, line{ln.SKUID, skuVersion, qty, price, uom, i + 1})
		}
		totalText := quantize(total, moneyScale)
		statusOut := "open"
		if add(mustRat(exposure), mustRat(totalText)).Cmp(mustRat(credit)) > 0 {
			statusOut = "held_credit"
			hold = apierr.New(apierr.CreditHold, "customer is over the credit limit").WithDetails(row{"order_id": id.String(), "status": statusOut})
		} else if floorHold {
			statusOut = "held_floor"
			hold = apierr.New(apierr.PriceFloorHold, "order line is below the floor price").WithDetails(row{"order_id": id.String(), "status": statusOut})
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.commercial_orders
			(id, company_id, customer_id, customer_version_id, status, total, state_version, created_by)
			VALUES ($1,$2,$3,$4,$5,$6::numeric,1,$7)`,
			id, p.CompanyID, in.CustomerID, custVersion, statusOut, totalText, p.UserID); err != nil {
			return err
		}
		for _, ln := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.commercial_order_lines
				(order_id, line_no, sku_id, sku_version_id, qty, uom, unit_price)
				VALUES ($1,$2,$3,$4,$5::numeric,$6,$7::numeric)`, id, ln.n, ln.sku, ln.version, ln.qty, ln.uom, ln.price); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hold != nil {
		return nil, hold
	}
	return row{"id": id.String(), "status": "open", "state_version": int64(1)}, nil
}

func (s *Service) Override(ctx context.Context, p rls.Principal, id uuid.UUID, kind string, in overrideIn, match int64) (row, error) {
	if in.Reason == "" {
		return nil, apierr.New(apierr.ValidationError, "reason is required")
	}
	want := "held_credit"
	if kind == "price_floor" {
		want = "held_floor"
		if !hasRole(p.Roles, "approver", "stakeholder", "credit_controller") {
			return nil, apierr.New(apierr.PermissionDenied, "floor override requires an approver")
		}
	} else if !hasRole(p.Roles, "credit_controller") {
		return nil, apierr.New(apierr.PermissionDenied, "credit override requires the credit controller")
	}
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var status, created string
		var state int64
		if err := tx.QueryRow(ctx, `SELECT status, state_version, created_by FROM erp.commercial_orders WHERE id=$1 FOR UPDATE`, id).
			Scan(&status, &state, &created); err != nil {
			return apierr.New(apierr.NotFound, "order not found")
		}
		if state != match {
			return apierr.New(apierr.Conflict, "state_version mismatch")
		}
		if status != want {
			return apierr.New(apierr.ValidationError, "order is not held for this override")
		}
		if created == p.UserID {
			return apierr.New(apierr.SoDViolation, "the enterer cannot override their own order")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.master_overrides (id, company_id, kind, subject_id, actor_id, reason)
			VALUES ($1,$2,$3,$4,$5,$6)`, uuid.New(), p.CompanyID, kind, id.String(), p.UserID, in.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.commercial_orders SET status='overridden', state_version=state_version+1 WHERE id=$1`, id); err != nil {
			return err
		}
		_, err := audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "exception.raised", ReferenceType: "sales_order", ReferenceID: id.String(), Reason: in.Reason,
			After: row{"kind": kind, "status": "overridden"},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": "overridden"}, nil
}

func (s *Service) Overrides(ctx context.Context, p rls.Principal, kind string) (row, error) {
	var rows []row
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		list, err := tx.Query(ctx, `SELECT kind, subject_id, actor_id, reason FROM erp.master_overrides
			WHERE company_id=$1 AND ($2='' OR kind=$2) ORDER BY created_at`, p.CompanyID, kind)
		if err != nil {
			return err
		}
		defer list.Close()
		for list.Next() {
			var k, subject, actor, reason string
			if err := list.Scan(&k, &subject, &actor, &reason); err != nil {
				return err
			}
			rows = append(rows, row{"kind": k, "subject_id": subject, "actor_id": actor, "reason": reason})
		}
		return list.Err()
	})
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []row{}
	}
	return row{"rows": rows}, nil
}
