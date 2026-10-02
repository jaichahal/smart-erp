package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
)

// SalesLine is one invoice line. Unit prices may carry four decimal places.
// The invoice total rounds to the fils.
type SalesLine struct {
	Qty       string
	UnitPrice string
	Discount  string
	TaxCode   string
}

// SalesInvoice posts through the approved sales rule for the line tax code.
type SalesInvoice struct {
	DocID            string
	PostingDate      time.Time
	DocumentDiscount string
	Lines            []SalesLine
}

// PostedInvoice is the registered journal.
type PostedInvoice struct {
	JournalID uuid.UUID
}

// PrintedLine is one line of the stored tax invoice.
type PrintedLine struct {
	Discount  string
	Tax       string
	LineTotal string
}

// PrintedInvoice is the tax invoice. LedgerTotal is read from the posted receivable line.
type PrintedInvoice struct {
	Lines            []PrintedLine
	DocumentDiscount string
	Tax              string
	Rounding         string
	Total            string
	LedgerTotal      string
}

type computedLine struct {
	qty       string
	unitPrice string
	discount  fils
	net       fils
	tax       fils
	gross     fils
}

// PostSalesInvoice posts discounts to the rule's discount account and fils differences
// to the rule's rounding account, then stores the tax invoice.
func (s *Service) PostSalesInvoice(ctx context.Context, p rls.Principal, in SalesInvoice) (PostedInvoice, error) {
	if in.DocID == "" || len(in.Lines) == 0 {
		return PostedInvoice{}, apierr.New(apierr.ValidationError, "invoice is incomplete")
	}
	taxCode := in.Lines[0].TaxCode
	for _, line := range in.Lines {
		if line.TaxCode != taxCode {
			return PostedInvoice{}, apierr.New(apierr.ValidationError, "lines must share one tax code")
		}
	}
	rule, err := s.ResolvePostingRule(ctx, p, RuleMatch{DocType: "sales_invoice", TaxCode: taxCode})
	if err != nil {
		return PostedInvoice{}, err
	}
	if err := s.periods.CheckPosting(ctx, p, periods.Posting{
		PostingDate: in.PostingDate, DocType: "sales_invoice", DocID: in.DocID,
	}, "en"); err != nil {
		return PostedInvoice{}, err
	}
	lines, docDiscount, err := priceInvoice(ctx, s, p, in)
	if err != nil {
		return PostedInvoice{}, err
	}
	var sumNet, sumGross, sumLineTax, sumLineDiscount fils
	for _, line := range lines {
		sumNet += line.net
		sumGross += line.gross
		sumLineTax += line.tax
		sumLineDiscount += line.discount
	}
	invoiceNet := sumNet - docDiscount
	if invoiceNet < 0 {
		return PostedInvoice{}, apierr.New(apierr.ValidationError, "discount exceeds the invoice")
	}
	rate, err := s.taxRate(ctx, p, taxCode)
	if err != nil {
		return PostedInvoice{}, err
	}
	statutory, err := mulRate(invoiceNet, rate)
	if err != nil {
		return PostedInvoice{}, err
	}
	rounding := sumLineTax - statutory
	discountTotal := sumLineDiscount + docDiscount
	receivable := invoiceNet + sumLineTax
	if rule.DiscountAccountID == nil && discountTotal != 0 {
		return PostedInvoice{}, apierr.New(apierr.MissingConfig, "posting rule has no discount account")
	}
	if rule.RoundingAccountID == nil && rounding != 0 {
		return PostedInvoice{}, apierr.New(apierr.MissingConfig, "posting rule has no rounding account")
	}
	if rule.TaxAccountID == nil && (statutory != 0 || sumLineTax != 0) {
		return PostedInvoice{}, apierr.New(apierr.MissingConfig, "posting rule has no tax account")
	}

	ctx = rls.WithPrincipal(ctx, p)
	journalID := uuid.New()
	err = rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		periodID, err := openPeriodID(ctx, tx, p.CompanyID, in.PostingDate)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.journals
			(id, company_id, doc_type, doc_id, period_id, posting_date, kind, created_by, rule_version_id, description)
			VALUES ($1,$2,'sales_invoice',$3,$4,$5,'invoice',$6,$7,$3)`,
			journalID, p.CompanyID, in.DocID, periodID, in.PostingDate.UTC().Format("2006-01-02"), p.UserID, rule.ID); err != nil {
			return err
		}
		lineNo := 1
		if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, rule.DebitAccountID, receivable, 0); err != nil {
			return err
		}
		lineNo++
		if discountTotal != 0 {
			if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, *rule.DiscountAccountID, discountTotal, 0); err != nil {
				return err
			}
			lineNo++
		}
		if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, rule.CreditAccountID, 0, sumGross); err != nil {
			return err
		}
		lineNo++
		if statutory != 0 {
			if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, *rule.TaxAccountID, 0, statutory); err != nil {
				return err
			}
			lineNo++
		}
		if rounding > 0 {
			if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, *rule.RoundingAccountID, 0, rounding); err != nil {
				return err
			}
		} else if rounding < 0 {
			if err := insertMoneyLine(ctx, tx, journalID, p.CompanyID, lineNo, *rule.RoundingAccountID, -rounding, 0); err != nil {
				return err
			}
		}
		var invoiceID uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO erp.tax_invoices
			(company_id, journal_id, document_discount, tax, rounding, total)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			p.CompanyID, journalID, docDiscount.String(), signedFils(statutory).String(), signedFils(rounding).String(), receivable.String()).Scan(&invoiceID); err != nil {
			return err
		}
		for i, line := range lines {
			lineTotal := line.net - allocated(lines, docDiscount, i) + line.tax
			if _, err := tx.Exec(ctx, `INSERT INTO erp.tax_invoice_lines
				(company_id, invoice_id, line_no, qty, unit_price, line_discount, tax, line_total)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				p.CompanyID, invoiceID, i+1, line.qty, line.unitPrice, line.discount.String(), line.tax.String(), lineTotal.String()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PostedInvoice{}, mapPostErr(err)
	}
	return PostedInvoice{JournalID: journalID}, nil
}

func signedFils(v fils) fils {
	if v < 0 {
		return -v
	}
	return v
}

func insertMoneyLine(ctx context.Context, tx pgx.Tx, journalID, company uuid.UUID, lineNo int, account uuid.UUID, debit, credit fils) error {
	_, err := tx.Exec(ctx, `INSERT INTO erp.journal_lines
		(journal_id, company_id, line_no, account_id, debit, credit, currency)
		VALUES ($1,$2,$3,$4,$5,$6,'AED')`,
		journalID, company, lineNo, account, debit.String(), credit.String())
	return err
}

func allocated(lines []computedLine, doc fils, index int) fils {
	if doc == 0 || len(lines) == 0 {
		return 0
	}
	var sum fils
	for _, line := range lines {
		sum += line.net
	}
	if sum == 0 {
		return 0
	}
	if index == len(lines)-1 {
		var used fils
		for i := 0; i < index; i++ {
			used += fils(divRound(int64(lines[i].net)*int64(doc), int64(sum)))
		}
		return doc - used
	}
	return fils(divRound(int64(lines[index].net)*int64(doc), int64(sum)))
}

func priceInvoice(ctx context.Context, s *Service, p rls.Principal, in SalesInvoice) ([]computedLine, fils, error) {
	docRaw := in.DocumentDiscount
	if docRaw == "" {
		docRaw = "0.00"
	}
	doc4, err := parseScale4(docRaw)
	if err != nil {
		return nil, 0, apierr.New(apierr.ValidationError, "document discount is invalid")
	}
	doc := roundToFils(doc4)
	var lines []computedLine
	var sumNet fils
	for _, line := range in.Lines {
		qty, err := parseScale4(line.Qty)
		if err != nil || qty <= 0 || qty%10000 != 0 {
			return nil, 0, apierr.New(apierr.ValidationError, "quantity is invalid")
		}
		price4, err := parseScale4(line.UnitPrice)
		if err != nil {
			return nil, 0, apierr.New(apierr.ValidationError, "unit price is invalid")
		}
		discountRaw := line.Discount
		if discountRaw == "" {
			discountRaw = "0.00"
		}
		discount4, err := parseScale4(discountRaw)
		if err != nil {
			return nil, 0, apierr.New(apierr.ValidationError, "line discount is invalid")
		}
		units := qty / 10000
		gross := roundToFils(units * price4)
		discount := roundToFils(discount4)
		if discount > gross {
			return nil, 0, apierr.New(apierr.ValidationError, "line discount exceeds the line")
		}
		lines = append(lines, computedLine{
			qty: line.Qty, unitPrice: line.UnitPrice, discount: discount, net: gross - discount, gross: gross,
		})
		sumNet += gross - discount
	}
	var allocatedSoFar fils
	for i := range lines {
		var share fils
		if i == len(lines)-1 {
			share = doc - allocatedSoFar
		} else if sumNet != 0 {
			share = fils(divRound(int64(lines[i].net)*int64(doc), int64(sumNet)))
			allocatedSoFar += share
		}
		if share > lines[i].net {
			return nil, 0, apierr.New(apierr.ValidationError, "discount exceeds the line")
		}
		rate, err := s.taxRate(ctx, p, in.Lines[i].TaxCode)
		if err != nil {
			return nil, 0, err
		}
		tax, err := mulRate(lines[i].net-share, rate)
		if err != nil {
			return nil, 0, err
		}
		lines[i].tax = tax
	}
	return lines, doc, nil
}

func (s *Service) taxRate(ctx context.Context, p rls.Principal, code string) (string, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var rate string
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT rate::text FROM erp.tax_codes WHERE company_id = $1 AND code = $2`, p.CompanyID, code).Scan(&rate)
	})
	if err != nil {
		return "", apierr.New(apierr.MissingConfig, "tax code is not configured")
	}
	return rate, nil
}

// TaxInvoice reprints the stored invoice and reads the ledger receivable total from the journal.
func (s *Service) TaxInvoice(ctx context.Context, p rls.Principal, journalID uuid.UUID) (PrintedInvoice, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out PrintedInvoice
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var invoiceID uuid.UUID
		var rounding string
		if err := tx.QueryRow(ctx, `SELECT id, document_discount::text, tax::text, rounding::text, total::text
			FROM erp.tax_invoices WHERE journal_id = $1 AND company_id = $2`, journalID, p.CompanyID).
			Scan(&invoiceID, &out.DocumentDiscount, &out.Tax, &rounding, &out.Total); err != nil {
			return apierr.New(apierr.NotFound, "tax invoice not found")
		}
		out.Rounding = rounding
		if rounding == "0.00" || rounding == "0" {
			out.Rounding = "0.00"
		}
		rows, err := tx.Query(ctx, `SELECT line_discount::text, tax::text, line_total::text
			FROM erp.tax_invoice_lines WHERE invoice_id = $1 ORDER BY line_no`, invoiceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line PrintedLine
			if err := rows.Scan(&line.Discount, &line.Tax, &line.LineTotal); err != nil {
				return err
			}
			out.Lines = append(out.Lines, line)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT coalesce(sum(jl.debit), 0)::text
			FROM erp.journal_lines jl
			JOIN erp.journals j ON j.id = jl.journal_id
			JOIN erp.posting_rule_versions v ON v.id = j.rule_version_id
			WHERE jl.journal_id = $1 AND jl.account_id = v.debit_account_id`, journalID).Scan(&out.LedgerTotal)
	})
	return out, err
}
