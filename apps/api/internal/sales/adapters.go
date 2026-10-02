package sales

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

type pgStock struct{}

// Reserve checks availability and records an active reservation.
func (pgStock) Reserve(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string, lines []StockLine) error {
	for _, line := range lines {
		if err := takeAvailable(ctx, tx, company, line.SKU, line.WarehouseID, line.Qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_reservations
			(company_id, id, order_id, order_line_id, sku, warehouse_id, qty, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'active')`,
			company, newID(time.Now()), orderID, line.OrderLineID, line.SKU, line.WarehouseID, line.Qty); err != nil {
			return err
		}
	}
	return nil
}

func takeAvailable(ctx context.Context, tx pgx.Tx, company uuid.UUID, sku, warehouse, qty string) error {
	var onHand string
	err := tx.QueryRow(ctx, `SELECT on_hand FROM erp.sales_stock
		WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3 FOR UPDATE`, company, sku, warehouse).Scan(&onHand)
	if err != nil {
		return apierr.New(apierr.NegativeStock, "stock is not available")
	}
	var reserved string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(qty::numeric), 0)::text FROM erp.sales_reservations
		WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3 AND status='active'`, company, sku, warehouse).Scan(&reserved); err != nil {
		return err
	}
	have, err1 := parseAmt(onHand)
	held, err2 := parseAmt(reserved)
	want, err3 := parseAmt(qty)
	if err1 != nil || err2 != nil || err3 != nil {
		return invalid("quantity")
	}
	if cmp(sub(have, held), want) < 0 {
		return apierr.New(apierr.NegativeStock, "reservation exceeds available stock")
	}
	return nil
}

// Release returns active reservations to available stock.
func (pgStock) Release(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string) error {
	_, err := tx.Exec(ctx, `UPDATE erp.sales_reservations SET status='released'
		WHERE company_id=$1 AND order_id=$2 AND status='active'`, company, orderID)
	return err
}

// Consume draws the reservation and reduces on-hand value at moving average.
func (pgStock) Consume(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string, lines []StockLine) ([]Consumed, error) {
	var out []Consumed
	for _, line := range lines {
		want, err := parseAmt(line.Qty)
		if err != nil || want.Sign() <= 0 {
			return nil, invalid("quantity")
		}
		var active string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(qty::numeric),0)::text FROM erp.sales_reservations
			WHERE company_id=$1 AND order_id=$2 AND sku=$3 AND status='active'`, company, orderID, line.SKU).Scan(&active); err != nil {
			return nil, err
		}
		held, err := parseAmt(active)
		if err != nil {
			return nil, err
		}
		if cmp(want, held) > 0 {
			return nil, apierr.New(apierr.NegativeStock, "delivery exceeds the reserved quantity")
		}
		if err := drawReservations(ctx, tx, company, orderID, line.SKU, want); err != nil {
			return nil, err
		}
		var onHand, value string
		if err := tx.QueryRow(ctx, `SELECT on_hand, value FROM erp.sales_stock
			WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3 FOR UPDATE`, company, line.SKU, line.WarehouseID).Scan(&onHand, &value); err != nil {
			return nil, apierr.New(apierr.NegativeStock, "stock is not available")
		}
		qoh, err1 := parseAmt(onHand)
		val, err2 := parseAmt(value)
		if err1 != nil || err2 != nil || qoh.Sign() <= 0 {
			return nil, apierr.New(apierr.NegativeStock, "stock is not available")
		}
		unit := new(big.Rat).Quo(val, qoh)
		cogs := roundHalfUp(mul(want, unit), 2)
		nextQty := sub(qoh, want)
		nextVal := sub(val, cogs)
		if nextQty.Sign() < 0 || nextVal.Sign() < 0 {
			return nil, apierr.New(apierr.NegativeStock, "stock would go negative")
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_stock SET on_hand=$4, value=$5
			WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3`, company, line.SKU, line.WarehouseID, formatQty(nextQty), money(nextVal)); err != nil {
			return nil, err
		}
		out = append(out, Consumed{SKU: line.SKU, Qty: formatQty(want), UnitCost: money(unit), Value: money(cogs)})
	}
	return out, nil
}

func drawReservations(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID, sku string, want *big.Rat) error {
	rows, err := tx.Query(ctx, `SELECT id, qty FROM erp.sales_reservations
		WHERE company_id=$1 AND order_id=$2 AND sku=$3 AND status='active' ORDER BY id FOR UPDATE`, company, orderID, sku)
	if err != nil {
		return err
	}
	type row struct{ id, qty string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.qty); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	left := new(big.Rat).Set(want)
	for _, r := range found {
		if left.Sign() == 0 {
			break
		}
		have, err := parseAmt(r.qty)
		if err != nil {
			return err
		}
		if cmp(have, left) <= 0 {
			if _, err := tx.Exec(ctx, `UPDATE erp.sales_reservations SET status='consumed' WHERE company_id=$1 AND id=$2`, company, r.id); err != nil {
				return err
			}
			left = sub(left, have)
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_reservations SET qty=$3 WHERE company_id=$1 AND id=$2`, company, r.id, formatQty(sub(have, left))); err != nil {
			return err
		}
		left = zero()
	}
	if left.Sign() != 0 {
		return apierr.New(apierr.NegativeStock, "delivery exceeds the reserved quantity")
	}
	return nil
}

// Restore puts returned quantity back at the original delivery cost.
func (pgStock) Restore(ctx context.Context, tx pgx.Tx, company uuid.UUID, lines []RestoreLine) error {
	for _, line := range lines {
		qty, err1 := parseAmt(line.Qty)
		cost, err2 := parseAmt(line.UnitCost)
		if err1 != nil || err2 != nil {
			return invalid("quantity")
		}
		addVal := roundHalfUp(mul(qty, cost), 2)
		var onHand, value string
		err := tx.QueryRow(ctx, `SELECT on_hand, value FROM erp.sales_stock
			WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3 FOR UPDATE`, company, line.SKU, line.WarehouseID).Scan(&onHand, &value)
		if err != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_stock (company_id, sku, warehouse_id, on_hand, value) VALUES ($1,$2,$3,$4,$5)`,
				company, line.SKU, line.WarehouseID, formatQty(qty), money(addVal)); err != nil {
				return err
			}
			continue
		}
		qoh, _ := parseAmt(onHand)
		val, _ := parseAmt(value)
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_stock SET on_hand=$4, value=$5 WHERE company_id=$1 AND sku=$2 AND warehouse_id=$3`,
			company, line.SKU, line.WarehouseID, formatQty(add(qoh, qty)), money(add(val, addVal))); err != nil {
			return err
		}
	}
	return nil
}

type pgLedger struct{}

// Post records a balanced journal in the sales posting table.
func (pgLedger) Post(ctx context.Context, tx pgx.Tx, company uuid.UUID, journal Journal) error {
	debit := zero()
	credit := zero()
	for i, line := range journal.Lines {
		d, err1 := parseAmt(zeroMoney(line.Debit))
		c, err2 := parseAmt(zeroMoney(line.Credit))
		if err1 != nil || err2 != nil {
			return invalid("posting amount")
		}
		debit = add(debit, d)
		credit = add(credit, c)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_postings
			(company_id, id, doc_type, doc_id, line_no, role, debit, credit)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			company, newID(time.Now()), journal.DocType, journal.DocID, i+1, line.Role, money(d), money(c)); err != nil {
			return err
		}
	}
	if cmp(roundHalfUp(debit, 2), roundHalfUp(credit, 2)) != 0 {
		return fmt.Errorf("sales: journal does not balance")
	}
	return nil
}

func zeroMoney(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

type pgCredit struct{}

// Check compares open receivables plus this order with the customer's limit.
func (pgCredit) Check(ctx context.Context, tx pgx.Tx, company uuid.UUID, customerID, orderTotal string) (CreditDecision, error) {
	var limit string
	err := tx.QueryRow(ctx, `SELECT credit_limit FROM erp.sales_customers WHERE company_id=$1 AND id=$2`, company, customerID).Scan(&limit)
	if err != nil {
		return CreditDecision{}, apierr.New(apierr.NotFound, "customer not found")
	}
	var open string
	if err := tx.QueryRow(ctx, `
		SELECT (
			COALESCE((SELECT SUM(i.total::numeric) FROM erp.sales_invoices i WHERE i.company_id=$1 AND i.customer_id=$2 AND i.status='registered'), 0)
			- COALESCE((SELECT SUM(r.amount::numeric + r.discount::numeric) FROM erp.sales_receipts r
				JOIN erp.sales_invoices i ON i.company_id=r.company_id AND i.id=r.invoice_id
				WHERE r.company_id=$1 AND i.customer_id=$2), 0)
			- COALESCE((SELECT SUM(n.total::numeric) FROM erp.sales_credit_notes n
				JOIN erp.sales_invoices i ON i.company_id=n.company_id AND i.id=n.invoice_id
				WHERE n.company_id=$1 AND i.customer_id=$2), 0)
		)::text`, company, customerID).Scan(&open); err != nil {
		return CreditDecision{}, err
	}
	limitR, err1 := parseAmt(limit)
	openR, err2 := parseAmt(open)
	orderR, err3 := parseAmt(orderTotal)
	if err1 != nil || err2 != nil || err3 != nil {
		return CreditDecision{}, invalid("credit amount")
	}
	return CreditDecision{
		OverLimit: cmp(add(openR, orderR), limitR) > 0,
		Open:      money(openR),
		Limit:     money(limitR),
	}, nil
}
