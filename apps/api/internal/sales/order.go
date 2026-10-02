package sales

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type pricedLine struct {
	ID, SKU, Qty, UOM, Price, TaxCode, TaxRate, Agreement string
	Net, Tax                                              *big.Rat
}

// CreateOrder checks credit and the price floor, then reserves stock in the same transaction.
// It does not post to the ledger.
func (s *Service) CreateOrder(ctx context.Context, p rls.Principal, in OrderInput) (Order, error) {
	if in.CustomerID == "" || in.WarehouseID == "" || len(in.Lines) == 0 {
		return Order{}, invalid("order requires a customer, warehouse, and lines")
	}
	if in.Currency == "" {
		in.Currency = "AED"
	}
	if in.AsOf.IsZero() {
		in.AsOf = time.Now().UTC()
	}
	var key *uuid.UUID
	if in.IdempotencyKey != "" {
		parsed, err := uuid.Parse(in.IdempotencyKey)
		if err != nil {
			return Order{}, invalid("idempotency key")
		}
		key = &parsed
	}
	var order Order
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		if key != nil {
			var id string
			err := tx.QueryRow(ctx, `SELECT id FROM erp.sales_orders WHERE company_id=$1 AND idempotency_key=$2`, p.CompanyID, *key).Scan(&id)
			if err == nil {
				loaded, err := loadOrder(ctx, tx, p.CompanyID, id)
				if err != nil {
					return err
				}
				order = loaded
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		lines, total, holds, err := s.priceLines(ctx, tx, p, in)
		if err != nil {
			return err
		}
		decision, err := s.credits().Check(ctx, tx, p.CompanyID, in.CustomerID, money(total))
		if err != nil {
			return err
		}
		if decision.OverLimit {
			holds = append(holds, HoldCredit)
		}
		if holds == nil {
			holds = []string{}
		}
		status := StatusReserved
		notice := ""
		switch {
		case in.Offline:
			status = StatusPendingReservation
			notice = NoticePendingReservation
		case len(holds) > 0:
			status = StatusHeld
		}
		id := newID(in.AsOf)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_orders
			(company_id, id, customer_id, warehouse_id, status, currency, total, order_date, offline, agent_notice, holds, idempotency_key, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			p.CompanyID, id, in.CustomerID, in.WarehouseID, status, in.Currency, money(total), in.AsOf,
			in.Offline, notice, holds, key, p.UserID); err != nil {
			return err
		}
		var stockLines []StockLine
		for i, line := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_order_lines
				(company_id, id, order_id, line_no, sku, qty, uom, unit_price, tax_code, tax_rate, agreement_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				p.CompanyID, line.ID, id, i+1, line.SKU, line.Qty, line.UOM, line.Price, line.TaxCode, line.TaxRate, line.Agreement); err != nil {
				return err
			}
			stockLines = append(stockLines, StockLine{OrderLineID: line.ID, SKU: line.SKU, WarehouseID: in.WarehouseID, Qty: line.Qty})
		}
		if status == StatusReserved {
			if err := s.stocks().Reserve(ctx, tx, p.CompanyID, id, stockLines); err != nil {
				return err
			}
		}
		loaded, err := loadOrder(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		order = loaded
		return nil
	})
	return order, err
}

func (s *Service) priceLines(ctx context.Context, tx pgx.Tx, p rls.Principal, in OrderInput) ([]pricedLine, *big.Rat, []string, error) {
	var lines []pricedLine
	total := zero()
	var holds []string
	floorHeld := false
	for _, inLine := range in.Lines {
		var floor, taxCode, taxRate, uom string
		err := tx.QueryRow(ctx, `SELECT floor_price, tax_code, tax_rate, uom FROM erp.sales_skus WHERE company_id=$1 AND sku=$2`, p.CompanyID, inLine.SKU).
			Scan(&floor, &taxCode, &taxRate, &uom)
		if err != nil {
			return nil, nil, nil, apierr.New(apierr.NotFound, "sku not found")
		}
		qty, err1 := parseAmt(inLine.Qty)
		offered, err2 := parseAmt(inLine.UnitPrice)
		rate, err3 := parseAmt(taxRate)
		floorR, err4 := parseAmt(floor)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || qty.Sign() <= 0 {
			return nil, nil, nil, invalid("order line")
		}
		price := offered
		agreement := ""
		var agrPrice, agrID string
		err = tx.QueryRow(ctx, `SELECT id, price FROM erp.sales_agreements
			WHERE company_id=$1 AND customer_id=$2 AND sku=$3 AND valid_from <= $4 AND valid_to >= $4 AND qty_min::numeric <= $5::numeric
			ORDER BY price::numeric
			LIMIT 1`, p.CompanyID, in.CustomerID, inLine.SKU, in.AsOf, inLine.Qty).Scan(&agrID, &agrPrice)
		switch {
		case err == nil:
			agreed, err := parseAmt(agrPrice)
			if err != nil {
				return nil, nil, nil, err
			}
			price = agreed
			agreement = agrID
		case errors.Is(err, pgx.ErrNoRows):
			if cmp(price, floorR) < 0 && !floorHeld {
				holds = append(holds, HoldPriceFloor)
				floorHeld = true
			}
		default:
			return nil, nil, nil, err
		}
		net := roundHalfUp(mul(qty, price), 2)
		tax := roundHalfUp(mul(net, rate), 2)
		total = add(total, add(net, tax))
		lines = append(lines, pricedLine{
			ID: newID(in.AsOf), SKU: inLine.SKU, Qty: formatQty(qty), UOM: uom, Price: money(price),
			TaxCode: taxCode, TaxRate: taxRate, Agreement: agreement, Net: net, Tax: tax,
		})
	}
	return lines, total, holds, nil
}

// ReleaseHold clears one hold. Stock is reserved only when no holds remain.
func (s *Service) ReleaseHold(ctx context.Context, p rls.Principal, orderID, hold, reason string, version int64) (Order, error) {
	if strings.TrimSpace(reason) == "" {
		return Order{}, invalidField("reason")
	}
	role := RoleCreditController
	if hold == HoldPriceFloor {
		role = RolePriceApprover
	} else if hold != HoldCredit {
		return Order{}, invalid("hold")
	}
	if !hasRole(p, role) {
		return Order{}, apierr.New(apierr.PermissionDenied, "override requires "+role)
	}
	var order Order
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		current, err := loadOrder(ctx, tx, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		if current.StateVersion != version {
			return apierr.New(apierr.Conflict, "state_version mismatch")
		}
		if !containsHold(current.Holds, hold) {
			return invalid("hold is not open")
		}
		var next []string
		for _, h := range current.Holds {
			if h != hold {
				next = append(next, h)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_overrides
			(company_id, id, order_id, hold, actor_id, actor_role, reason, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp())`,
			p.CompanyID, newID(time.Now()), orderID, hold, p.UserID, role, strings.TrimSpace(reason)); err != nil {
			return err
		}
		if _, err := audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "sales.override", ReferenceType: "sales_order", ReferenceID: orderID,
			Reason: reason, After: map[string]any{"hold": hold, "role": role},
		}); err != nil {
			return err
		}
		if next == nil {
			next = []string{}
		}
		status := StatusHeld
		if len(next) == 0 {
			status = StatusReserved
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_orders SET holds=$3, status=$4, state_version=state_version+1
			WHERE company_id=$1 AND id=$2`, p.CompanyID, orderID, next, status); err != nil {
			return err
		}
		if status == StatusReserved {
			lines, warehouse, err := stockLinesFor(ctx, tx, p.CompanyID, orderID)
			if err != nil {
				return err
			}
			_ = warehouse
			if err := s.stocks().Reserve(ctx, tx, p.CompanyID, orderID, lines); err != nil {
				return err
			}
		}
		order, err = loadOrder(ctx, tx, p.CompanyID, orderID)
		return err
	})
	return order, err
}

// CancelOrder releases a reservation that has not been delivered.
func (s *Service) CancelOrder(ctx context.Context, p rls.Principal, orderID string, version int64) (Order, error) {
	var order Order
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		current, err := loadOrder(ctx, tx, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		if current.StateVersion != version {
			return apierr.New(apierr.Conflict, "state_version mismatch")
		}
		var delivered int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.sales_deliveries WHERE company_id=$1 AND order_id=$2`, p.CompanyID, orderID).Scan(&delivered); err != nil {
			return err
		}
		if delivered > 0 {
			return invalid("order already has a delivery")
		}
		if err := s.stocks().Release(ctx, tx, p.CompanyID, orderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_orders SET status=$3, state_version=state_version+1 WHERE company_id=$1 AND id=$2`,
			p.CompanyID, orderID, StatusCancelled); err != nil {
			return err
		}
		order, err = loadOrder(ctx, tx, p.CompanyID, orderID)
		return err
	})
	return order, err
}

// Timeline lists the order and the documents created from it.
func (s *Service) Timeline(ctx context.Context, p rls.Principal, orderID string) ([]FlowDoc, error) {
	var docs []FlowDoc
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		order, err := loadOrder(ctx, tx, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		docs = append(docs, FlowDoc{DocType: "sales_order", DocID: order.ID, Status: order.Status})
		rows, err := tx.Query(ctx, `SELECT id, status, number FROM erp.sales_invoices WHERE company_id=$1 AND order_id=$2 ORDER BY id`, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, status, number string
			if err := rows.Scan(&id, &status, &number); err != nil {
				rows.Close()
				return err
			}
			docs = append(docs, FlowDoc{DocType: "sales_invoice", DocID: id, Status: status, Number: number})
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT g.id, g.status FROM erp.sales_gate_passes g
			JOIN erp.sales_invoices i ON i.company_id=g.company_id AND i.id=g.invoice_id
			WHERE g.company_id=$1 AND i.order_id=$2`, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, status string
			if err := rows.Scan(&id, &status); err != nil {
				rows.Close()
				return err
			}
			docs = append(docs, FlowDoc{DocType: "gate_pass", DocID: id, Status: status})
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id, status FROM erp.sales_deliveries WHERE company_id=$1 AND order_id=$2`, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, status string
			if err := rows.Scan(&id, &status); err != nil {
				rows.Close()
				return err
			}
			docs = append(docs, FlowDoc{DocType: "delivery_note", DocID: id, Status: status})
		}
		rows.Close()
		return nil
	})
	return docs, err
}

func stockLinesFor(ctx context.Context, tx pgx.Tx, company uuid.UUID, orderID string) ([]StockLine, string, error) {
	var warehouse string
	if err := tx.QueryRow(ctx, `SELECT warehouse_id FROM erp.sales_orders WHERE company_id=$1 AND id=$2`, company, orderID).Scan(&warehouse); err != nil {
		return nil, "", err
	}
	rows, err := tx.Query(ctx, `SELECT id, sku, qty FROM erp.sales_order_lines WHERE company_id=$1 AND order_id=$2 ORDER BY line_no`, company, orderID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var lines []StockLine
	for rows.Next() {
		var line StockLine
		if err := rows.Scan(&line.OrderLineID, &line.SKU, &line.Qty); err != nil {
			return nil, "", err
		}
		line.WarehouseID = warehouse
		lines = append(lines, line)
	}
	return lines, warehouse, rows.Err()
}

func loadOrder(ctx context.Context, tx pgx.Tx, company uuid.UUID, id string) (Order, error) {
	var o Order
	var holds []string
	err := tx.QueryRow(ctx, `SELECT id, customer_id, status, holds, agent_notice, total, currency, state_version
		FROM erp.sales_orders WHERE company_id=$1 AND id=$2`, company, id).
		Scan(&o.ID, &o.CustomerID, &o.Status, &holds, &o.AgentNotice, &o.Total, &o.Currency, &o.StateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, apierr.New(apierr.NotFound, "order not found")
	}
	if err != nil {
		return Order{}, err
	}
	if holds == nil {
		holds = []string{}
	}
	o.Holds = holds
	rows, err := tx.Query(ctx, `SELECT id, sku, qty, unit_price, tax_code, agreement_id
		FROM erp.sales_order_lines WHERE company_id=$1 AND order_id=$2 ORDER BY line_no`, company, id)
	if err != nil {
		return Order{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var line OrderLine
		if err := rows.Scan(&line.ID, &line.SKU, &line.Qty, &line.UnitPrice, &line.TaxCode, &line.AgreementID); err != nil {
			return Order{}, err
		}
		o.Lines = append(o.Lines, line)
	}
	return o, rows.Err()
}

func (s *Service) load(ctx context.Context, p rls.Principal, id string) (Order, error) {
	var order Order
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		loaded, err := loadOrder(ctx, tx, p.CompanyID, id)
		order = loaded
		return err
	})
	return order, err
}

func (s *Service) loadInv(ctx context.Context, p rls.Principal, id string) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		loaded, err := loadInvoice(ctx, tx, p.CompanyID, id)
		inv = loaded
		return err
	})
	return inv, err
}

func containsHold(holds []string, want string) bool {
	for _, h := range holds {
		if h == want {
			return true
		}
	}
	return false
}
