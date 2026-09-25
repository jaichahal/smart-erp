package sales

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type party struct {
	Name, Address, TRN, Version, DiscountRate string
	Terms, DiscountDays                       int
	VAT, Cash                                 bool
}

func loadCustomer(ctx context.Context, tx pgx.Tx, company uuid.UUID, id string) (party, error) {
	var c party
	err := tx.QueryRow(ctx, `SELECT name, address, trn, version_id, payment_terms_days, discount_days, discount_rate, vat_registered, cash_sale
		FROM erp.sales_customers WHERE company_id=$1 AND id=$2`, company, id).
		Scan(&c.Name, &c.Address, &c.TRN, &c.Version, &c.Terms, &c.DiscountDays, &c.DiscountRate, &c.VAT, &c.Cash)
	if errors.Is(err, pgx.ErrNoRows) {
		return party{}, apierr.New(apierr.NotFound, "customer not found")
	}
	return c, err
}

func loadSupplier(ctx context.Context, tx pgx.Tx, company uuid.UUID) (party, error) {
	var c party
	err := tx.QueryRow(ctx, `SELECT name, address, trn FROM erp.sales_suppliers WHERE company_id=$1`, company).
		Scan(&c.Name, &c.Address, &c.TRN)
	if errors.Is(err, pgx.ErrNoRows) {
		return party{}, invalidField("supplier_name")
	}
	return c, err
}

type articleLine struct {
	Description string `json:"description"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unit_price"`
	TaxRate     string `json:"tax_rate"`
	TaxAmount   string `json:"tax_amount"`
	LineTotal   string `json:"line_total"`
}

type articleDoc struct {
	DocumentTitle   string        `json:"document_title"`
	SupplierName    string        `json:"supplier_name"`
	SupplierAddress string        `json:"supplier_address"`
	SupplierTRN     string        `json:"supplier_trn"`
	CustomerName    string        `json:"customer_name"`
	CustomerAddress string        `json:"customer_address"`
	CustomerTRN     string        `json:"customer_trn"`
	InvoiceDate     string        `json:"invoice_date"`
	SupplyDate      string        `json:"supply_date"`
	Currency        string        `json:"currency"`
	Lines           []articleLine `json:"lines"`
	Discount        string        `json:"discount_amount"`
	Gross           string        `json:"gross_amount"`
	Tax             string        `json:"tax_amount"`
	Total           string        `json:"invoice_total"`
}

func missingArticle59(doc articleDoc, customerVAT bool) string {
	checks := []struct{ field, value string }{
		{"document_title", doc.DocumentTitle},
		{"supplier_name", doc.SupplierName},
		{"supplier_address", doc.SupplierAddress},
		{"supplier_trn", doc.SupplierTRN},
		{"customer_name", doc.CustomerName},
		{"customer_address", doc.CustomerAddress},
		{"invoice_date", doc.InvoiceDate},
		{"supply_date", doc.SupplyDate},
		{"currency", doc.Currency},
		{"gross_amount", doc.Gross},
		{"discount_amount", doc.Discount},
		{"tax_amount", doc.Tax},
		{"invoice_total", doc.Total},
	}
	if customerVAT {
		checks = append(checks, struct{ field, value string }{"customer_trn", doc.CustomerTRN})
	}
	for _, c := range checks {
		if c.value == "" {
			return c.field
		}
	}
	if len(doc.Lines) == 0 {
		return "line.description"
	}
	for _, line := range doc.Lines {
		pairs := []struct{ field, value string }{
			{"line.description", line.Description},
			{"line.qty", line.Qty},
			{"line.unit_price", line.UnitPrice},
			{"line.tax_rate", line.TaxRate},
			{"line.tax_amount", line.TaxAmount},
			{"line.line_total", line.LineTotal},
		}
		for _, c := range pairs {
			if c.value == "" {
				return c.field
			}
		}
	}
	if doc.DocumentTitle != "Tax Invoice" && doc.DocumentTitle != "Tax Credit Note" {
		return "document_title"
	}
	return ""
}

// DraftInvoice copies lines, prices, tax codes, and the due date from the order.
func (s *Service) DraftInvoice(ctx context.Context, p rls.Principal, orderID string) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		order, err := loadOrder(ctx, tx, p.CompanyID, orderID)
		if err != nil {
			return err
		}
		if order.Status != StatusReserved && order.Status != StatusPendingReservation {
			return invalid("invoice requires a reserved order")
		}
		cust, err := loadCustomer(ctx, tx, p.CompanyID, order.CustomerID)
		if err != nil {
			return err
		}
		var orderDate time.Time
		if err := tx.QueryRow(ctx, `SELECT order_date FROM erp.sales_orders WHERE company_id=$1 AND id=$2`, p.CompanyID, orderID).Scan(&orderDate); err != nil {
			return err
		}
		built, err := buildInvoice(ctx, tx, p.CompanyID, order, cust, orderDate)
		if err != nil {
			return err
		}
		built.ID = newID(orderDate)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_invoices
			(company_id, id, order_id, customer_id, status, currency, invoice_date, supply_date, due_date,
			 gross, discount, tax, rounding, total, discount_days, discount_rate, created_by)
			VALUES ($1,$2,$3,$4,'draft',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			p.CompanyID, built.ID, orderID, order.CustomerID, built.Currency, built.InvoiceDate, built.SupplyDate, built.DueDate,
			built.Gross, built.Discount, built.Tax, built.Rounding, built.Total, cust.DiscountDays, cust.DiscountRate, p.UserID); err != nil {
			return err
		}
		if err := insertInvoiceLines(ctx, tx, p.CompanyID, built); err != nil {
			return err
		}
		inv, err = loadInvoice(ctx, tx, p.CompanyID, built.ID)
		return err
	})
	return inv, err
}

func buildInvoice(ctx context.Context, tx pgx.Tx, company uuid.UUID, order Order, cust party, orderDate time.Time) (Invoice, error) {
	inv := Invoice{
		OrderID: order.ID, CustomerID: order.CustomerID, Status: StatusDraft, Currency: order.Currency,
		InvoiceDate: orderDate.Format("2006-01-02"), SupplyDate: orderDate.Format("2006-01-02"),
		DueDate:  orderDate.AddDate(0, 0, cust.Terms).Format("2006-01-02"),
		Discount: "0.00", Rounding: "0.00",
	}
	gross := zero()
	tax := zero()
	for _, line := range order.Lines {
		var desc, rate string
		if err := tx.QueryRow(ctx, `SELECT description, tax_rate FROM erp.sales_skus WHERE company_id=$1 AND sku=$2`, company, line.SKU).Scan(&desc, &rate); err != nil {
			return Invoice{}, err
		}
		qty, err1 := parseAmt(line.Qty)
		price, err2 := parseAmt(line.UnitPrice)
		rateR, err3 := parseAmt(rate)
		if err1 != nil || err2 != nil || err3 != nil {
			return Invoice{}, invalid("invoice line")
		}
		net := roundHalfUp(mul(qty, price), 2)
		lineTax := roundHalfUp(mul(net, rateR), 2)
		gross = add(gross, net)
		tax = add(tax, lineTax)
		inv.Lines = append(inv.Lines, InvoiceLine{
			SKU: line.SKU, Description: desc, Qty: line.Qty, UnitPrice: line.UnitPrice,
			TaxCode: line.TaxCode, TaxRate: rate, TaxAmount: money(lineTax), LineTotal: money(add(net, lineTax)),
		})
	}
	inv.Gross = money(gross)
	inv.Tax = money(tax)
	inv.Total = money(add(gross, tax))
	return inv, nil
}

func insertInvoiceLines(ctx context.Context, tx pgx.Tx, company uuid.UUID, inv Invoice) error {
	for i, line := range inv.Lines {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_invoice_lines
			(company_id, id, invoice_id, line_no, sku, description, qty, uom, unit_price, tax_code, tax_rate, tax_amount, line_total)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'ea',$8,$9,$10,$11,$12)`,
			company, newID(time.Now()), inv.ID, i+1, line.SKU, line.Description, line.Qty, line.UnitPrice,
			line.TaxCode, line.TaxRate, line.TaxAmount, line.LineTotal); err != nil {
			return err
		}
	}
	return nil
}

// UpdateInvoice changes prices. The approved hash stays as it was.
func (s *Service) UpdateInvoice(ctx context.Context, p rls.Principal, id string, version int64, patch InvoicePatch) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		current, err := loadInvoice(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if current.Status == StatusRegistered {
			return apierr.New(apierr.Immutable, "registered invoice")
		}
		if current.StateVersion != version {
			return apierr.New(apierr.Conflict, "state_version mismatch")
		}
		for _, line := range patch.Lines {
			price, err := parseAmt(line.UnitPrice)
			if err != nil {
				return invalid("unit price")
			}
			var qty, rate string
			if err := tx.QueryRow(ctx, `SELECT qty, tax_rate FROM erp.sales_invoice_lines WHERE company_id=$1 AND invoice_id=$2 AND sku=$3`,
				p.CompanyID, id, line.SKU).Scan(&qty, &rate); err != nil {
				return err
			}
			q, _ := parseAmt(qty)
			r, _ := parseAmt(rate)
			net := roundHalfUp(mul(q, price), 2)
			lineTax := roundHalfUp(mul(net, r), 2)
			if _, err := tx.Exec(ctx, `UPDATE erp.sales_invoice_lines SET unit_price=$4, tax_amount=$5, line_total=$6
				WHERE company_id=$1 AND invoice_id=$2 AND sku=$3`,
				p.CompanyID, id, line.SKU, money(price), money(lineTax), money(add(net, lineTax))); err != nil {
				return err
			}
		}
		if err := recomputeInvoice(ctx, tx, p.CompanyID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_invoices SET state_version=state_version+1 WHERE company_id=$1 AND id=$2`, p.CompanyID, id); err != nil {
			return err
		}
		inv, err = loadInvoice(ctx, tx, p.CompanyID, id)
		return err
	})
	return inv, err
}

func recomputeInvoice(ctx context.Context, tx pgx.Tx, company uuid.UUID, id string) error {
	rows, err := tx.Query(ctx, `SELECT tax_amount, line_total, qty, unit_price, tax_rate FROM erp.sales_invoice_lines WHERE company_id=$1 AND invoice_id=$2`, company, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	gross := zero()
	tax := zero()
	for rows.Next() {
		var taxAmt, total, qty, price, rate string
		if err := rows.Scan(&taxAmt, &total, &qty, &price, &rate); err != nil {
			return err
		}
		q, _ := parseAmt(qty)
		pr, _ := parseAmt(price)
		lineTax, _ := parseAmt(taxAmt)
		gross = add(gross, roundHalfUp(mul(q, pr), 2))
		tax = add(tax, lineTax)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE erp.sales_invoices SET gross=$3, tax=$4, total=$5 WHERE company_id=$1 AND id=$2`,
		company, id, money(gross), money(tax), money(add(gross, tax)))
	return err
}

// SubmitInvoice refuses a draft missing an Article 59 field or blocked by a rule.
func (s *Service) SubmitInvoice(ctx context.Context, p rls.Principal, id string, version int64) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		current, err := loadInvoice(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if current.StateVersion != version {
			return apierr.New(apierr.Conflict, "state_version mismatch")
		}
		doc, vat, err := articleOf(ctx, tx, p.CompanyID, current)
		if err != nil {
			return err
		}
		if field := missingArticle59(doc, vat); field != "" {
			return invalidField(field)
		}
		warnings, err := applyRules(ctx, tx, p.CompanyID, current)
		if err != nil {
			return err
		}
		sum, err := hashDoc(doc)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_invoices SET status='submitted', content_hash=$3, warnings=$4, state_version=state_version+1
			WHERE company_id=$1 AND id=$2`, p.CompanyID, id, sum, warnings); err != nil {
			return err
		}
		inv, err = loadInvoice(ctx, tx, p.CompanyID, id)
		return err
	})
	return inv, err
}

func applyRules(ctx context.Context, tx pgx.Tx, company uuid.UUID, inv Invoice) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT name, mode, field, op, value FROM erp.sales_rules WHERE company_id=$1 AND doc_type='sales_invoice'`, company)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var warnings []string
	for rows.Next() {
		var name, mode, field, op, value string
		if err := rows.Scan(&name, &mode, &field, &op, &value); err != nil {
			return nil, err
		}
		hit, err := ruleHits(inv, field, op, value)
		if err != nil {
			return nil, err
		}
		if !hit {
			continue
		}
		if mode == "blocking" {
			return nil, apierr.New(apierr.ValidationError, "blocking rule "+name).WithDetails(map[string]any{"rule": name, "field": field})
		}
		warnings = append(warnings, name)
	}
	if warnings == nil {
		warnings = []string{}
	}
	return warnings, rows.Err()
}

func ruleHits(inv Invoice, field, op, value string) (bool, error) {
	if field != "total" || op != "max" {
		return false, nil
	}
	total, err1 := parseAmt(inv.Total)
	limit, err2 := parseAmt(value)
	if err1 != nil || err2 != nil {
		return false, invalid("rule")
	}
	return cmp(total, limit) > 0, nil
}

// ApproveInvoice records the hash registration must still match.
func (s *Service) ApproveInvoice(ctx context.Context, p rls.Principal, id string, version int64) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		current, err := loadInvoice(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		if current.Status != StatusSubmitted || current.StateVersion != version {
			return apierr.New(apierr.Conflict, "invoice is not submitted at this version")
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.sales_invoices SET status='approved', approved_hash=content_hash, state_version=state_version+1
			WHERE company_id=$1 AND id=$2`, p.CompanyID, id); err != nil {
			return err
		}
		inv, err = loadInvoice(ctx, tx, p.CompanyID, id)
		return err
	})
	return inv, err
}

// RegisterInvoice allocates the number, posts receivable, revenue, and VAT, stores the PDF, then enables print and the gate pass.
func (s *Service) RegisterInvoice(ctx context.Context, p rls.Principal, id string, version int64) (Invoice, error) {
	var inv Invoice
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		out, err := registerInvoiceTx(ctx, tx, s, p, id, version)
		inv = out
		return err
	})
	return inv, err
}

func registerInvoiceTx(ctx context.Context, tx pgx.Tx, s *Service, p rls.Principal, id string, version int64) (Invoice, error) {
	current, err := loadInvoice(ctx, tx, p.CompanyID, id)
	if err != nil {
		return Invoice{}, err
	}
	if current.Status != StatusApproved || current.StateVersion != version {
		return Invoice{}, invalid("invoice is not approved at this version")
	}
	doc, vat, err := articleOf(ctx, tx, p.CompanyID, current)
	if err != nil {
		return Invoice{}, err
	}
	if field := missingArticle59(doc, vat); field != "" {
		return Invoice{}, invalidField(field)
	}
	sum, err := hashDoc(doc)
	if err != nil {
		return Invoice{}, err
	}
	if sum != current.ApprovedHash {
		return Invoice{}, apierr.New(apierr.Conflict, "posted hash differs from the approved hash").WithDetails(map[string]any{
			"approved_hash": current.ApprovedHash, "posted_hash": sum,
		})
	}
	if err := checkTolerance(ctx, tx, p, current.Total); err != nil {
		return Invoice{}, err
	}
	year := yearOf(current.InvoiceDate)
	number, formatted, err := allocate(ctx, tx, p.CompanyID, "sales_invoice", "INV", year)
	if err != nil {
		return Invoice{}, err
	}
	versions, err := versionsAt(ctx, tx, p.CompanyID, current)
	if err != nil {
		return Invoice{}, err
	}
	body, err := canon.Marshal(doc)
	if err != nil {
		return Invoice{}, err
	}
	pdfHash := sha256Hex(body)
	if err := s.ledgers().Post(ctx, tx, p.CompanyID, invoiceJournal(id, formatted, current)); err != nil {
		return Invoice{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_pdfs (company_id, doc_type, doc_id, pdf, sha256) VALUES ($1,'sales_invoice',$2,$3,$4)`,
		p.CompanyID, id, body, pdfHash); err != nil {
		return Invoice{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_registrations
		(company_id, invoice_id, doc_type, fiscal_year, number, content_hash, customer_version_id, price_version_id, tax_code_version_id, canonical, pdf_hash, registered_by, registered_at)
		VALUES ($1,$2,'sales_invoice',$3,$4,$5,$6,$7,$8,$9,$10,$11,clock_timestamp())`,
		p.CompanyID, id, year, number, sum, versions.customer, versions.price, versions.tax, string(body), pdfHash, p.UserID); err != nil {
		return Invoice{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.sales_invoices SET status='registered', number=$3, print_enabled=true, gate_pass_enabled=true,
		customer_version_id=$4, price_version_id=$5, tax_code_version_id=$6, state_version=state_version+1
		WHERE company_id=$1 AND id=$2`,
		p.CompanyID, id, formatted, versions.customer, versions.price, versions.tax); err != nil {
		return Invoice{}, err
	}
	return loadInvoice(ctx, tx, p.CompanyID, id)
}

func checkTolerance(ctx context.Context, tx pgx.Tx, p rls.Principal, total string) error {
	var ceiling string
	err := tx.QueryRow(ctx, `SELECT max_amount FROM erp.sales_tolerances WHERE company_id=$1 AND user_id=$2`, p.CompanyID, p.UserID).Scan(&ceiling)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	have, err1 := parseAmt(total)
	limit, err2 := parseAmt(ceiling)
	if err1 != nil || err2 != nil {
		return invalid("posting tolerance")
	}
	if cmp(have, limit) > 0 {
		return apierr.New(apierr.PermissionDenied, "posting exceeds the user tolerance").WithDetails(map[string]any{"field": "posting_tolerance"})
	}
	return nil
}

type versionSet struct{ customer, price, tax string }

func versionsAt(ctx context.Context, tx pgx.Tx, company uuid.UUID, inv Invoice) (versionSet, error) {
	cust, err := loadCustomer(ctx, tx, company, inv.CustomerID)
	if err != nil {
		return versionSet{}, err
	}
	out := versionSet{customer: cust.Version, price: "list"}
	if len(inv.Lines) == 0 {
		return out, invalid("invoice lines")
	}
	if err := tx.QueryRow(ctx, `SELECT tax_code_version_id FROM erp.sales_skus WHERE company_id=$1 AND sku=$2`, company, inv.Lines[0].SKU).Scan(&out.tax); err != nil {
		return versionSet{}, err
	}
	var agreement string
	_ = tx.QueryRow(ctx, `SELECT agreement_id FROM erp.sales_order_lines WHERE company_id=$1 AND order_id=$2 AND sku=$3`, company, inv.OrderID, inv.Lines[0].SKU).Scan(&agreement)
	if agreement != "" {
		_ = tx.QueryRow(ctx, `SELECT version_id FROM erp.sales_agreements WHERE company_id=$1 AND id=$2`, company, agreement).Scan(&out.price)
	}
	return out, nil
}

func invoiceJournal(id, number string, inv Invoice) Journal {
	return Journal{DocType: "sales_invoice", DocID: id, DocNumber: number, Lines: []JournalLine{
		{Role: "receivable", Debit: inv.Total, Credit: "0.00"},
		{Role: "discount", Debit: inv.Discount, Credit: "0.00"},
		{Role: "revenue", Debit: "0.00", Credit: inv.Gross},
		{Role: "vat_output", Debit: "0.00", Credit: inv.Tax},
		{Role: "rounding", Debit: "0.00", Credit: inv.Rounding},
	}}
}

func allocate(ctx context.Context, tx pgx.Tx, company uuid.UUID, docType, prefix string, year int) (int64, string, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO erp.sales_sequences (company_id, doc_type, fiscal_year, next_number)
		VALUES ($1,$2,$3,1) ON CONFLICT DO NOTHING`, company, docType, year); err != nil {
		return 0, "", err
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_number FROM erp.sales_sequences
		WHERE company_id=$1 AND doc_type=$2 AND fiscal_year=$3 FOR UPDATE`, company, docType, year).Scan(&number); err != nil {
		return 0, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.sales_sequences SET next_number=next_number+1
		WHERE company_id=$1 AND doc_type=$2 AND fiscal_year=$3`, company, docType, year); err != nil {
		return 0, "", err
	}
	return number, fmt.Sprintf("%s-%d-%06d", prefix, year, number), nil
}

func yearOf(day string) int {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Now().UTC().Year()
	}
	return t.Year()
}

// PrintInvoice returns the stored PDF only after registration.
func (s *Service) PrintInvoice(ctx context.Context, p rls.Principal, id string) (Print, error) {
	var out Print
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT print_enabled FROM erp.sales_invoices WHERE company_id=$1 AND id=$2`, p.CompanyID, id).Scan(&enabled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apierr.New(apierr.NotFound, "invoice not found")
			}
			return err
		}
		if !enabled {
			return invalid("print is disabled before registration")
		}
		return tx.QueryRow(ctx, `SELECT pdf, sha256 FROM erp.sales_pdfs WHERE company_id=$1 AND doc_type='sales_invoice' AND doc_id=$2`, p.CompanyID, id).
			Scan(&out.Body, &out.Hash)
	})
	return out, err
}

func articleOf(ctx context.Context, tx pgx.Tx, company uuid.UUID, inv Invoice) (articleDoc, bool, error) {
	sup, err := loadSupplier(ctx, tx, company)
	if err != nil {
		return articleDoc{}, false, err
	}
	cust, err := loadCustomer(ctx, tx, company, inv.CustomerID)
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

func hashDoc(doc articleDoc) (string, error) {
	body, err := canon.Marshal(doc)
	if err != nil {
		return "", err
	}
	return canon.Hash(body, ""), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadInvoice(ctx context.Context, tx pgx.Tx, company uuid.UUID, id string) (Invoice, error) {
	var inv Invoice
	var warnings []string
	err := tx.QueryRow(ctx, `SELECT id, order_id, customer_id, status, number, invoice_date::text, supply_date::text, due_date::text,
		currency, gross, discount, tax, rounding, total, print_enabled, gate_pass_enabled,
		customer_version_id, price_version_id, tax_code_version_id, approved_hash, content_hash, warnings, state_version
		FROM erp.sales_invoices WHERE company_id=$1 AND id=$2`, company, id).
		Scan(&inv.ID, &inv.OrderID, &inv.CustomerID, &inv.Status, &inv.Number, &inv.InvoiceDate, &inv.SupplyDate, &inv.DueDate,
			&inv.Currency, &inv.Gross, &inv.Discount, &inv.Tax, &inv.Rounding, &inv.Total, &inv.PrintEnabled, &inv.GatePassEnabled,
			&inv.CustomerVersionID, &inv.PriceVersionID, &inv.TaxCodeVersionID, &inv.ApprovedHash, &inv.ContentHash, &warnings, &inv.StateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, apierr.New(apierr.NotFound, "invoice not found")
	}
	if err != nil {
		return Invoice{}, err
	}
	if warnings == nil {
		warnings = []string{}
	}
	inv.Warnings = warnings
	err = tx.QueryRow(ctx, `SELECT sha256 FROM erp.sales_pdfs WHERE company_id=$1 AND doc_type='sales_invoice' AND doc_id=$2`, company, id).Scan(&inv.PDFHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, err
	}
	rows, err := tx.Query(ctx, `SELECT sku, description, qty, unit_price, tax_code, tax_rate, tax_amount, line_total
		FROM erp.sales_invoice_lines WHERE company_id=$1 AND invoice_id=$2 ORDER BY line_no`, company, id)
	if err != nil {
		return Invoice{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var line InvoiceLine
		if err := rows.Scan(&line.SKU, &line.Description, &line.Qty, &line.UnitPrice, &line.TaxCode, &line.TaxRate, &line.TaxAmount, &line.LineTotal); err != nil {
			return Invoice{}, err
		}
		inv.Lines = append(inv.Lines, line)
	}
	return inv, rows.Err()
}
