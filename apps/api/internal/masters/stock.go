package masters

import (
	"context"
	"errors"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type stockIn struct {
	SKUID       string `json:"sku_id"`
	WarehouseID string `json:"warehouse_id"`
	Qty         string `json:"qty"`
	UnitCost    string `json:"unit_cost"`
}

type warehouseIn struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (s *Service) Warehouse(ctx context.Context, p rls.Principal, in warehouseIn) (row, error) {
	if in.Code == "" || in.Name == "" {
		return nil, apierr.New(apierr.ValidationError, "code and name are required")
	}
	id := uuid.New()
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.warehouses (id, company_id, code, name) VALUES ($1,$2,$3,$4)`, id, p.CompanyID, in.Code, in.Name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "code": in.Code, "name": in.Name}, nil
}

func (s *Service) Receive(ctx context.Context, p rls.Principal, in stockIn) (row, error) {
	return s.move(ctx, p, in, "receipt")
}

func (s *Service) Issue(ctx context.Context, p rls.Principal, in stockIn) (row, error) {
	return s.move(ctx, p, in, "issue")
}

func (s *Service) move(ctx context.Context, p rls.Principal, in stockIn, kind string) (row, error) {
	qty, err := mustQty(in.Qty, "qty")
	if err != nil {
		return nil, err
	}
	cost := "0.0000"
	if kind == "receipt" {
		cost, err = mustMoney(in.UnitCost, "unit_cost")
		if err != nil {
			return nil, err
		}
	}
	id := uuid.New()
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		onHand, value, reserved, err := stockSums(ctx, tx, in.SKUID, in.WarehouseID)
		if err != nil {
			return err
		}
		q, _ := rat(qty)
		if kind == "issue" {
			left := add(onHand, neg(q))
			if left.Cmp(reserved) < 0 || left.Sign() < 0 {
				return apierr.New(apierr.NegativeStock, "stock cannot go negative")
			}
			avg := newRat(0)
			if onHand.Sign() > 0 {
				avg = quo(value, onHand)
			}
			cost = quantize(avg, moneyScale)
			_, err = tx.Exec(ctx, `INSERT INTO erp.stock_ledger
				(id, company_id, sku_id, warehouse_id, qty_delta, unit_cost, value_delta, movement_type, source_doc_id)
				VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,'issue',$8)`,
				id, p.CompanyID, in.SKUID, in.WarehouseID, quantize(neg(q), qtyScale), cost, quantize(neg(mul(q, mustRat(cost))), moneyScale), id.String())
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.stock_ledger
			(id, company_id, sku_id, warehouse_id, qty_delta, unit_cost, value_delta, movement_type, source_doc_id)
			VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,'receipt',$8)`,
			id, p.CompanyID, in.SKUID, in.WarehouseID, qty, cost, quantize(mul(q, mustRat(cost)), moneyScale), id.String())
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String()}, nil
}

func (s *Service) Reserve(ctx context.Context, p rls.Principal, in stockIn) (row, error) {
	qty, err := mustQty(in.Qty, "qty")
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		onHand, _, reserved, err := stockSums(ctx, tx, in.SKUID, in.WarehouseID)
		if err != nil {
			return err
		}
		q, _ := rat(qty)
		if add(reserved, q).Cmp(onHand) > 0 {
			return apierr.New(apierr.NegativeStock, "reservation exceeds on-hand stock")
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.stock_reservations
			(id, company_id, order_line_id, sku_id, warehouse_id, qty, status)
			VALUES ($1,$2,$3,$4,$5,$6::numeric,'active')`,
			id, p.CompanyID, id.String(), in.SKUID, in.WarehouseID, qty)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "qty": qty}, nil
}

func (s *Service) Availability(ctx context.Context, p rls.Principal, warehouse, sku, view, class string) (row, error) {
	var result row
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var scope *string
		if err := tx.QueryRow(ctx, `SELECT erp.permission_scope('field:cost:read')`).Scan(&scope); err != nil {
			return err
		}
		showCost := scope != nil
		if sku != "" {
			item, err := oneStock(ctx, tx, sku, warehouse, showCost)
			result = item
			return err
		}
		q := `SELECT id::text, item_class, base_uom FROM erp.skus WHERE company_id=$1 AND status='approved'`
		args := []any{p.CompanyID}
		if class != "" {
			q += ` AND item_class=$2`
			args = append(args, class)
		}
		list, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		type skuRow struct{ id, itemClass, uom string }
		var skus []skuRow
		for list.Next() {
			var item skuRow
			if err := list.Scan(&item.id, &item.itemClass, &item.uom); err != nil {
				list.Close()
				return err
			}
			if view == "sales" && item.itemClass == "raw_material" {
				continue
			}
			if view == "purchase" && item.itemClass == "finished_goods" {
				continue
			}
			skus = append(skus, item)
		}
		if err := list.Err(); err != nil {
			list.Close()
			return err
		}
		list.Close()
		rows := make([]row, 0, len(skus))
		for _, item := range skus {
			stock, err := oneStock(ctx, tx, item.id, warehouse, showCost)
			if err != nil {
				return err
			}
			stock["uom"] = item.uom
			stock["item_class"] = item.itemClass
			rows = append(rows, stock)
		}
		result = row{"rows": rows}
		return nil
	})
	return result, err
}

func (s *Service) Convert(ctx context.Context, p rls.Principal, id uuid.UUID, qty, from, to string) (row, error) {
	var out string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var purchase, stock, production, sales, status string
		if err := tx.QueryRow(ctx, `SELECT purchase_uom, stock_uom, production_uom, sales_uom, status FROM erp.skus WHERE id=$1`, id).
			Scan(&purchase, &stock, &production, &sales, &status); err != nil {
			return apierr.New(apierr.NotFound, "sku not found")
		}
		if status != "approved" {
			return apierr.New(apierr.ValidationError, "sku is not approved")
		}
		fromUOM, err := purposeUOM(from, purchase, stock, production, sales)
		if err != nil {
			return err
		}
		toUOM, err := purposeUOM(to, purchase, stock, production, sales)
		if err != nil {
			return err
		}
		fromFactor, err := factorOf(ctx, tx, id, fromUOM)
		if err != nil {
			return err
		}
		toFactor, err := factorOf(ctx, tx, id, toUOM)
		if err != nil {
			return err
		}
		got, ok := convertQty(qty, fromFactor, toFactor)
		if !ok {
			return apierr.New(apierr.ValidationError, "quantity could not be converted")
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return row{"qty": out, "from": from, "to": to}, nil
}

func oneStock(ctx context.Context, tx pgx.Tx, sku, warehouse string, showCost bool) (row, error) {
	onHand, value, reserved, err := stockSums(ctx, tx, sku, warehouse)
	if err != nil {
		return nil, err
	}
	out := row{
		"sku_id":    sku,
		"on_hand":   quantize(onHand, qtyScale),
		"reserved":  quantize(reserved, qtyScale),
		"available": quantize(add(onHand, neg(reserved)), qtyScale),
	}
	if showCost {
		unit := newRat(0)
		if onHand.Sign() > 0 {
			unit = quo(value, onHand)
		}
		out["unit_cost"] = quantize(unit, moneyScale)
		out["value"] = quantize(value, moneyScale)
	}
	return out, nil
}

func stockSums(ctx context.Context, tx pgx.Tx, sku, warehouse string) (*big.Rat, *big.Rat, *big.Rat, error) {
	q := `SELECT coalesce(sum(qty_delta),0)::text, coalesce(sum(value_delta),0)::text FROM erp.stock_ledger WHERE sku_id=$1`
	args := []any{sku}
	if warehouse != "" {
		q += ` AND warehouse_id=$2`
		args = append(args, warehouse)
	}
	var qty, value string
	if err := tx.QueryRow(ctx, q, args...).Scan(&qty, &value); err != nil {
		return nil, nil, nil, err
	}
	rq := `SELECT coalesce(sum(qty),0)::text FROM erp.stock_reservations WHERE sku_id=$1 AND status='active'`
	rargs := []any{sku}
	if warehouse != "" {
		rq += ` AND warehouse_id=$2`
		rargs = append(rargs, warehouse)
	}
	var reserved string
	if err := tx.QueryRow(ctx, rq, rargs...).Scan(&reserved); err != nil {
		return nil, nil, nil, err
	}
	return mustRat(qty), mustRat(value), mustRat(reserved), nil
}

func purposeUOM(purpose, purchase, stock, production, sales string) (string, error) {
	switch purpose {
	case "purchase":
		return purchase, nil
	case "stock":
		return stock, nil
	case "production":
		return production, nil
	case "sales":
		return sales, nil
	default:
		const prefix = "uom:"
		if len(purpose) > len(prefix) && purpose[:len(prefix)] == prefix {
			return purpose[len(prefix):], nil
		}
		return "", apierr.New(apierr.ValidationError, "unknown unit")
	}
}

func factorOf(ctx context.Context, tx pgx.Tx, sku uuid.UUID, uom string) (string, error) {
	var f string
	err := tx.QueryRow(ctx, `SELECT factor_to_base::text FROM erp.uom_conversions WHERE sku_id=$1 AND uom=$2`, sku, uom).Scan(&f)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apierr.New(apierr.ValidationError, "unit is not configured")
	}
	return f, err
}
