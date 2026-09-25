package sales

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

const deliveryWindow = 24 * time.Hour

// GatePass creates the delivery note and posts cost of goods at moving average.
func (s *Service) GatePass(ctx context.Context, p rls.Principal, in GateInput) (Delivery, error) {
	if in.At.IsZero() {
		in.At = time.Now().UTC()
	}
	var out Delivery
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		inv, err := loadInvoice(ctx, tx, p.CompanyID, in.InvoiceID)
		if err != nil {
			return err
		}
		if !inv.GatePassEnabled || inv.Status != StatusRegistered {
			return invalid("gate pass is disabled before registration")
		}
		lines, warehouse, err := stockLinesFor(ctx, tx, p.CompanyID, inv.OrderID)
		if err != nil {
			return err
		}
		want := map[string]string{}
		if len(in.Lines) == 0 {
			for _, line := range lines {
				want[line.SKU] = line.Qty
			}
		} else {
			for _, line := range in.Lines {
				want[line.SKU] = line.Qty
			}
		}
		var consume []StockLine
		for _, line := range lines {
			qty, ok := want[line.SKU]
			if !ok {
				continue
			}
			consume = append(consume, StockLine{OrderLineID: line.OrderLineID, SKU: line.SKU, WarehouseID: warehouse, Qty: qty})
		}
		if len(consume) == 0 {
			return invalid("delivery lines")
		}
		consumed, err := s.stocks().Consume(ctx, tx, p.CompanyID, inv.OrderID, consume)
		if err != nil {
			return err
		}
		id := newID(in.At)
		gateID := newID(in.At.Add(time.Millisecond))
		cal, err := loadCalendar(ctx, tx, p.CompanyID)
		if err != nil {
			return err
		}
		due, err := cal.due(in.At, deliveryWindow)
		if err != nil {
			return err
		}
		clockID := newID(in.At.Add(2 * time.Millisecond))
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_clocks
			(company_id, id, doc_id, doc_type, kind, started_at, due_at)
			VALUES ($1,$2,$3,'delivery_note','delivery_note',$4,$5)`, p.CompanyID, clockID, id, in.At, due); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_deliveries
			(company_id, id, invoice_id, order_id, gate_pass_id, status) VALUES ($1,$2,$3,$4,$5,'open')`,
			p.CompanyID, id, inv.ID, inv.OrderID, gateID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_gate_passes (company_id, id, invoice_id, delivery_id, status)
			VALUES ($1,$2,$3,$4,'issued')`, p.CompanyID, gateID, inv.ID, id); err != nil {
			return err
		}
		cogs := zero()
		for _, line := range consumed {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_delivery_lines (company_id, id, delivery_id, sku, qty, unit_cost)
				VALUES ($1,$2,$3,$4,$5,$6)`, p.CompanyID, newID(time.Now()), id, line.SKU, line.Qty, line.UnitCost); err != nil {
				return err
			}
			val, _ := parseAmt(line.Value)
			cogs = add(cogs, val)
		}
		if err := s.ledgers().Post(ctx, tx, p.CompanyID, Journal{
			DocType: "delivery_note", DocID: id, Lines: []JournalLine{
				{Role: "cogs", Debit: money(cogs), Credit: "0.00"},
				{Role: "inventory", Debit: "0.00", Credit: money(cogs)},
			},
		}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT sku, qty FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2 AND status='active'`, p.CompanyID, inv.OrderID)
		if err != nil {
			return err
		}
		var back []LineInput
		for rows.Next() {
			var line LineInput
			if err := rows.Scan(&line.SKU, &line.Qty); err != nil {
				rows.Close()
				return err
			}
			back = append(back, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, line := range back {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_backorders (company_id, id, order_id, sku, qty, status)
				VALUES ($1,$2,$3,$4,$5,'open')`, p.CompanyID, newID(time.Now()), inv.OrderID, line.SKU, line.Qty); err != nil {
				return err
			}
		}
		out = Delivery{ID: id, InvoiceID: inv.ID, OrderID: inv.OrderID, GatePassID: gateID, Status: "open", ClockDue: due.UTC(), Backorder: back}
		for _, line := range consumed {
			out.Lines = append(out.Lines, DeliveryLine{SKU: line.SKU, Qty: line.Qty, UnitCost: line.UnitCost})
		}
		return nil
	})
	return out, err
}

// ProveDelivery closes the delivery-note clock and hashes the proof into the audit chain.
func (s *Service) ProveDelivery(ctx context.Context, p rls.Principal, deliveryID string, proof Proof) (Delivery, error) {
	var out Delivery
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var orderID, invoiceID, gateID string
		if err := tx.QueryRow(ctx, `SELECT invoice_id, order_id, gate_pass_id FROM erp.sales_deliveries WHERE company_id=$1 AND id=$2`,
			p.CompanyID, deliveryID).Scan(&invoiceID, &orderID, &gateID); err != nil {
			return apierr.New(apierr.NotFound, "delivery not found")
		}
		appended, err := audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "delivery.confirmed", ReferenceType: "delivery_note", ReferenceID: deliveryID,
			After: map[string]any{"signature": proof.Signature, "photo_sha": proof.PhotoSHA, "lat": proof.Lat, "lng": proof.Lng, "at": proof.At.UTC().Format(time.RFC3339Nano)},
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_clocks SET closed_at=clock_timestamp()
			WHERE company_id=$1 AND doc_id=$2 AND closed_at IS NULL`, p.CompanyID, deliveryID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_deliveries SET proof_hash=$3, confirmed_at=clock_timestamp(), status='confirmed'
			WHERE company_id=$1 AND id=$2`, p.CompanyID, deliveryID, appended.Hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_events (company_id, id, type, doc_type, doc_id, occurred_at, payload)
			VALUES ($1,$2,'delivery.confirmed','delivery_note',$3,clock_timestamp(),$4::jsonb)`,
			p.CompanyID, newID(time.Now()), deliveryID, `{"delivery_id":"`+deliveryID+`"}`); err != nil {
			return err
		}
		var due time.Time
		var closed *time.Time
		_ = tx.QueryRow(ctx, `SELECT due_at, closed_at FROM erp.sales_clocks WHERE company_id=$1 AND doc_id=$2`, p.CompanyID, deliveryID).Scan(&due, &closed)
		out = Delivery{ID: deliveryID, InvoiceID: invoiceID, OrderID: orderID, GatePassID: gateID, Status: "confirmed", ClockDue: due.UTC(), ClockClosed: closed != nil, ProofHash: appended.Hash}
		return nil
	})
	return out, err
}

// EscalateClocks flags delivery notes whose business-hours window has passed.
func (s *Service) EscalateClocks(ctx context.Context, p rls.Principal, now time.Time) (int, error) {
	n := 0
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, doc_id FROM erp.sales_clocks
			WHERE company_id=$1 AND closed_at IS NULL AND escalated_at IS NULL AND due_at <= $2`, p.CompanyID, now)
		if err != nil {
			return err
		}
		type dueRow struct{ id, doc string }
		var found []dueRow
		for rows.Next() {
			var row dueRow
			if err := rows.Scan(&row.id, &row.doc); err != nil {
				rows.Close()
				return err
			}
			found = append(found, row)
		}
		rows.Close()
		for _, row := range found {
			if _, err := tx.Exec(ctx, `UPDATE erp.sales_clocks SET escalated_at=$3 WHERE company_id=$1 AND id=$2`, p.CompanyID, row.id, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_events (company_id, id, type, doc_type, doc_id, occurred_at, payload)
				VALUES ($1,$2,'clock.expired','delivery_note',$3,$4,'{}'::jsonb)`, p.CompanyID, newID(now), row.doc, now); err != nil {
				return err
			}
			n++
		}
		return rows.Err()
	})
	return n, err
}

// CreditNote references the original invoice. It does not edit that invoice.
func (s *Service) CreditNote(ctx context.Context, p rls.Principal, in CreditInput) (CreditNote, error) {
	if in.Reason == "" || in.InvoiceID == "" || len(in.Lines) == 0 {
		return CreditNote{}, invalid("credit note")
	}
	var note CreditNote
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		inv, err := loadInvoice(ctx, tx, p.CompanyID, in.InvoiceID)
		if err != nil {
			return err
		}
		if inv.Status != StatusRegistered {
			return invalid("credit note requires a registered invoice")
		}
		gross := zero()
		tax := zero()
		type credLine struct {
			sku, qty, price, taxAmt, cost string
		}
		var lines []credLine
		_, warehouse, err := stockLinesFor(ctx, tx, p.CompanyID, inv.OrderID)
		if err != nil {
			return err
		}
		for _, req := range in.Lines {
			var price, rate string
			if err := tx.QueryRow(ctx, `SELECT unit_price, tax_rate FROM erp.sales_invoice_lines WHERE company_id=$1 AND invoice_id=$2 AND sku=$3`,
				p.CompanyID, inv.ID, req.SKU).Scan(&price, &rate); err != nil {
				return err
			}
			qty, err1 := parseAmt(req.Qty)
			priceR, err2 := parseAmt(price)
			rateR, err3 := parseAmt(rate)
			if err1 != nil || err2 != nil || err3 != nil {
				return invalid("credit line")
			}
			net := roundHalfUp(mul(qty, priceR), 2)
			lineTax := roundHalfUp(mul(net, rateR), 2)
			gross = add(gross, net)
			tax = add(tax, lineTax)
			var cost string
			err = tx.QueryRow(ctx, `SELECT unit_cost FROM erp.sales_delivery_lines d
				JOIN erp.sales_deliveries h ON h.company_id=d.company_id AND h.id=d.delivery_id
				WHERE d.company_id=$1 AND h.invoice_id=$2 AND d.sku=$3 ORDER BY d.id LIMIT 1`, p.CompanyID, inv.ID, req.SKU).Scan(&cost)
			if err != nil {
				cost = "0.00"
			}
			lines = append(lines, credLine{sku: req.SKU, qty: formatQty(qty), price: money(priceR), taxAmt: money(lineTax), cost: cost})
		}
		total := add(gross, tax)
		id := newID(time.Now())
		year := yearOf(inv.InvoiceDate)
		_, number, err := allocate(ctx, tx, p.CompanyID, "credit_note", "CN", year)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_credit_notes
			(company_id, id, invoice_id, status, number, total, tax, restore_stock, created_by)
			VALUES ($1,$2,$3,'registered',$4,$5,$6,$7,$8)`,
			p.CompanyID, id, inv.ID, number, money(total), money(tax), in.RestoreStock, p.UserID); err != nil {
			return err
		}
		var restore []RestoreLine
		restoreValue := zero()
		for _, line := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_credit_lines
				(company_id, id, credit_note_id, sku, qty, unit_price, tax_amount, unit_cost)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, p.CompanyID, newID(time.Now()), id, line.sku, line.qty, line.price, line.taxAmt, line.cost); err != nil {
				return err
			}
			if in.RestoreStock {
				restore = append(restore, RestoreLine{SKU: line.sku, WarehouseID: warehouse, Qty: line.qty, UnitCost: line.cost})
				q, _ := parseAmt(line.qty)
				c, _ := parseAmt(line.cost)
				restoreValue = add(restoreValue, roundHalfUp(mul(q, c), 2))
			}
		}
		journal := Journal{DocType: "credit_note", DocID: id, DocNumber: number, Lines: []JournalLine{
			{Role: "revenue", Debit: money(gross), Credit: "0.00"},
			{Role: "vat_output", Debit: money(tax), Credit: "0.00"},
			{Role: "receivable", Debit: "0.00", Credit: money(total)},
		}}
		if in.RestoreStock {
			if err := s.stocks().Restore(ctx, tx, p.CompanyID, restore); err != nil {
				return err
			}
			journal.Lines = append(journal.Lines,
				JournalLine{Role: "inventory", Debit: money(restoreValue), Credit: "0.00"},
				JournalLine{Role: "cogs", Debit: "0.00", Credit: money(restoreValue)},
			)
		}
		if err := s.ledgers().Post(ctx, tx, p.CompanyID, journal); err != nil {
			return err
		}
		note = CreditNote{ID: id, OriginalInvoiceID: inv.ID, Number: number, Total: money(total)}
		return nil
	})
	return note, err
}
