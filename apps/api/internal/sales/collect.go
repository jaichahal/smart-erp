package sales

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// CashSale creates the invoice and the receipt in one transaction for a walk-in customer.
func (s *Service) CashSale(ctx context.Context, p rls.Principal, in CashInput) (CashSale, error) {
	if in.AsOf.IsZero() {
		in.AsOf = time.Now().UTC()
	}
	var sale CashSale
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		cust, err := loadCustomer(ctx, tx, p.CompanyID, in.CustomerID)
		if err != nil {
			return err
		}
		if !cust.Cash {
			return invalid("cash sale requires the cash customer")
		}
		orderIn := OrderInput{CustomerID: in.CustomerID, WarehouseID: in.WarehouseID, Currency: "AED", AsOf: in.AsOf, Lines: in.Lines}
		lines, total, holds, err := s.priceLines(ctx, tx, p, orderIn)
		if err != nil {
			return err
		}
		if _, err := s.credits().Check(ctx, tx, p.CompanyID, in.CustomerID, money(total)); err != nil {
			return err
		}
		if len(holds) > 0 {
			return invalid("cash sale is below the price floor")
		}
		orderID := newID(in.AsOf)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_orders
			(company_id, id, customer_id, warehouse_id, status, currency, total, order_date, holds, created_by)
			VALUES ($1,$2,$3,$4,'reserved','AED',$5,$6,'{}',$7)`,
			p.CompanyID, orderID, in.CustomerID, in.WarehouseID, money(total), in.AsOf, p.UserID); err != nil {
			return err
		}
		var stockLines []StockLine
		for i, line := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_order_lines
				(company_id, id, order_id, line_no, sku, qty, uom, unit_price, tax_code, tax_rate, agreement_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				p.CompanyID, line.ID, orderID, i+1, line.SKU, line.Qty, line.UOM, line.Price, line.TaxCode, line.TaxRate, line.Agreement); err != nil {
				return err
			}
			stockLines = append(stockLines, StockLine{OrderLineID: line.ID, SKU: line.SKU, WarehouseID: in.WarehouseID, Qty: line.Qty})
		}
		if err := s.stocks().Reserve(ctx, tx, p.CompanyID, orderID, stockLines); err != nil {
			return err
		}
		order, err := loadOrder(ctx, tx, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		built, err := buildInvoice(ctx, tx, p.CompanyID, order, cust, in.AsOf)
		if err != nil {
			return err
		}
		built.ID = newID(in.AsOf)
		doc, vat, err := articleFromBuilt(ctx, tx, p, built, cust)
		if err != nil {
			return err
		}
		if field := missingArticle59(doc, vat); field != "" {
			return invalidField(field)
		}
		sum, err := hashDoc(doc)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_invoices
			(company_id, id, order_id, customer_id, status, currency, invoice_date, supply_date, due_date,
			 gross, discount, tax, rounding, total, content_hash, approved_hash, cash_sale, created_by)
			VALUES ($1,$2,$3,$4,'approved',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,true,$15)`,
			p.CompanyID, built.ID, orderID, in.CustomerID, built.Currency, built.InvoiceDate, built.SupplyDate, built.DueDate,
			built.Gross, built.Discount, built.Tax, built.Rounding, built.Total, sum, p.UserID); err != nil {
			return err
		}
		if err := insertInvoiceLines(ctx, tx, p.CompanyID, built); err != nil {
			return err
		}
		registered, err := registerInvoiceTx(ctx, tx, s, p, built.ID, 1)
		if err != nil {
			return err
		}
		receiptID := newID(in.AsOf)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_receipts (company_id, id, invoice_id, amount, discount, paid_on, created_by)
			VALUES ($1,$2,$3,$4,'0.00',$5,$6)`, p.CompanyID, receiptID, registered.ID, registered.Total, in.AsOf, p.UserID); err != nil {
			return err
		}
		if err := s.ledgers().Post(ctx, tx, p.CompanyID, Journal{
			DocType: "receipt", DocID: receiptID, Lines: []JournalLine{
				{Role: "cash", Debit: registered.Total, Credit: "0.00"},
				{Role: "receivable", Debit: "0.00", Credit: registered.Total},
			},
		}); err != nil {
			return err
		}
		sale = CashSale{InvoiceID: registered.ID, ReceiptID: receiptID, InvoiceNumber: registered.Number, Total: registered.Total}
		return nil
	})
	return sale, err
}

func articleFromBuilt(ctx context.Context, tx pgx.Tx, p rls.Principal, inv Invoice, cust party) (articleDoc, bool, error) {
	sup, err := loadSupplier(ctx, tx, p.CompanyID)
	if err != nil {
		return articleDoc{}, false, err
	}
	doc := articleDoc{
		DocumentTitle: "Tax Invoice", SupplierName: sup.Name, SupplierAddress: sup.Address, SupplierTRN: sup.TRN,
		CustomerName: cust.Name, CustomerAddress: cust.Address, CustomerTRN: cust.TRN,
		InvoiceDate: inv.InvoiceDate, SupplyDate: inv.SupplyDate, Currency: inv.Currency,
		Discount: inv.Discount, Gross: inv.Gross, Tax: inv.Tax, Total: inv.Total,
	}
	for _, line := range inv.Lines {
		doc.Lines = append(doc.Lines, articleLine{
			Description: line.Description, Qty: line.Qty, UnitPrice: line.UnitPrice,
			TaxRate: line.TaxRate, TaxAmount: line.TaxAmount, LineTotal: line.LineTotal,
		})
	}
	return doc, cust.VAT, nil
}

// Receipt collects an invoice and posts an early-payment discount inside the window.
func (s *Service) Receipt(ctx context.Context, p rls.Principal, in ReceiptInput) (Receipt, error) {
	var out Receipt
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var days int
		var rate, total, day string
		if err := tx.QueryRow(ctx, `SELECT discount_days, discount_rate, total, invoice_date::text
			FROM erp.sales_invoices WHERE company_id=$1 AND id=$2 AND status='registered'`, p.CompanyID, in.InvoiceID).
			Scan(&days, &rate, &total, &day); err != nil {
			return invalid("registered invoice not found")
		}
		invoiceDay, err := time.Parse("2006-01-02", day)
		if err != nil {
			return err
		}
		rateR, err := parseAmt(rate)
		if err != nil {
			return err
		}
		totalR, err := parseAmt(total)
		if err != nil {
			return err
		}
		discount := zero()
		windowEnd := invoiceDay.AddDate(0, 0, days)
		paid := time.Date(in.PaidOn.Year(), in.PaidOn.Month(), in.PaidOn.Day(), 0, 0, 0, 0, time.UTC)
		if days > 0 && rateR.Sign() > 0 && !paid.After(windowEnd) {
			discount = roundHalfUp(mul(totalR, rateR), 2)
		}
		id := newID(in.PaidOn)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_receipts (company_id, id, invoice_id, amount, discount, paid_on, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, p.CompanyID, id, in.InvoiceID, in.Amount, money(discount), paid, p.UserID); err != nil {
			return err
		}
		amount, err := parseAmt(in.Amount)
		if err != nil {
			return invalid("amount")
		}
		lines := []JournalLine{{Role: "cash", Debit: money(amount), Credit: "0.00"}}
		if discount.Sign() > 0 {
			lines = append(lines, JournalLine{Role: "discount", Debit: money(discount), Credit: "0.00"})
		}
		lines = append(lines, JournalLine{Role: "receivable", Debit: "0.00", Credit: money(add(amount, discount))})
		if err := s.ledgers().Post(ctx, tx, p.CompanyID, Journal{DocType: "receipt", DocID: id, Lines: lines}); err != nil {
			return err
		}
		out = Receipt{ID: id, Amount: money(amount), Discount: money(discount)}
		return nil
	})
	return out, err
}

// Attainment sums registered invoices for the agent and period.
func (s *Service) Attainment(ctx context.Context, p rls.Principal, agent, period string) (string, error) {
	var raw string
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(SUM(total::numeric), 0)::text FROM erp.sales_invoices
			WHERE company_id=$1 AND created_by=$2 AND status='registered' AND to_char(invoice_date, 'YYYY-MM')=$3`,
			p.CompanyID, agent, period).Scan(&raw)
	})
	if err != nil {
		return "", err
	}
	amt, err := parseAmt(raw)
	if err != nil {
		return "", err
	}
	return money(amt), nil
}

// Commission is the target rate times collected receipts, not open invoices.
func (s *Service) Commission(ctx context.Context, p rls.Principal, agent, period string) (string, error) {
	var raw, rate string
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT commission_rate FROM erp.sales_targets WHERE company_id=$1 AND agent_id=$2 AND period=$3`,
			p.CompanyID, agent, period).Scan(&rate); err != nil {
			rate = "0"
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(SUM(r.amount::numeric), 0)::text
			FROM erp.sales_receipts r
			JOIN erp.sales_invoices i ON i.company_id=r.company_id AND i.id=r.invoice_id
			WHERE r.company_id=$1 AND i.created_by=$2 AND to_char(i.invoice_date, 'YYYY-MM')=$3`,
			p.CompanyID, agent, period).Scan(&raw)
	})
	if err != nil {
		return "", err
	}
	collected, err1 := parseAmt(raw)
	rateR, err2 := parseAmt(rate)
	if err1 != nil || err2 != nil {
		return "", invalid("commission")
	}
	return money(roundHalfUp(mul(collected, rateR), 2)), nil
}
