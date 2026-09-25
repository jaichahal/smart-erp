package purchase

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Started is a held payment and the release request the accountant prepared.
type Started struct {
	Document Document `json:"document"`
	Gate     Gate     `json:"gate"`
}

// CreateRequisition stores a requisition. A raw-material line needs SKU approval.
func (s *Service) CreateRequisition(ctx context.Context, p rls.Principal, in DocInput) (Document, error) {
	return s.createDoc(ctx, p, "requisition", "REQ", statusOpen, in)
}

// CreateQuote stores an unapproved quote against the vendor.
func (s *Service) CreateQuote(ctx context.Context, p rls.Principal, in DocInput) (Document, error) {
	return s.createDoc(ctx, p, "quote", "QTE", statusDraft, in)
}

// CreatePriceAgreement stores an unapproved price agreement.
func (s *Service) CreatePriceAgreement(ctx context.Context, p rls.Principal, in DocInput) (Document, error) {
	return s.createDoc(ctx, p, "price_agreement", "PA", statusDraft, in)
}

// CreateLPO stores an LPO. A blacklisted vendor is refused.
func (s *Service) CreateLPO(ctx context.Context, p rls.Principal, in DocInput) (Document, error) {
	return s.createDoc(ctx, p, "lpo", "LPO", statusOpen, in)
}

// CreateSupplierInvoice stores a supplier invoice. It is not posted yet.
func (s *Service) CreateSupplierInvoice(ctx context.Context, p rls.Principal, in DocInput) (Document, error) {
	return s.createDoc(ctx, p, "supplier_invoice", "SINV", statusOpen, in)
}

// ApproveCommercial marks a quote or price agreement approved when the vendor still passes the SKU rule.
func (s *Service) ApproveCommercial(ctx context.Context, p rls.Principal, id uuid.UUID) (Document, error) {
	var doc Document
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		doc, err = s.loadDoc(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if doc.DocType != "quote" && doc.DocType != "price_agreement" {
			return apierr.New(apierr.ValidationError, msg("validation"))
		}
		lines := make([]LineInput, len(doc.Lines))
		for i, l := range doc.Lines {
			lines[i] = LineInput{SKUID: l.SKUID, Qty: l.Qty, UnitPrice: l.UnitPrice}
		}
		if err := s.checkLines(ctx, tx, p, doc.VendorID, doc.DocType, lines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.purchase_docs SET status=$3 WHERE company_id=$1 AND id=$2`, p.CompanyID, id, statusApproved); err != nil {
			return err
		}
		doc, err = s.loadDoc(ctx, tx, p, id)
		return err
	})
	return doc, err
}

// ReceiveGoods records a receipt against an LPO and leaves the ledger gap explicit.
func (s *Service) ReceiveGoods(ctx context.Context, p rls.Principal, lpoID uuid.UUID, lines []LineInput) (Document, error) {
	in := DocInput{SourceID: &lpoID, Lines: lines, Currency: "AED"}
	var vendor uuid.UUID
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT vendor_id FROM erp.purchase_docs WHERE company_id=$1 AND id=$2 AND doc_type='lpo'`, p.CompanyID, lpoID).Scan(&vendor)
	})
	if err != nil {
		return Document{}, apierr.New(apierr.NotFound, msg("not.found"))
	}
	in.VendorID = vendor
	doc, err := s.createDoc(ctx, p, "goods_receipt", "GRN", statusOpen, in)
	if err != nil {
		return Document{}, err
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE erp.purchase_docs SET ledger_posted=false, posting_gap=$3 WHERE company_id=$1 AND id=$2`,
			p.CompanyID, doc.ID, PostingGap)
		return err
	})
	if err != nil {
		return Document{}, err
	}
	return s.GetDocument(ctx, p, doc.ID)
}

// PostInvoice runs the three-way match and duplicate check.
// A match that passes still does not write a ledger journal.
func (s *Service) PostInvoice(ctx context.Context, p rls.Principal, id uuid.UUID, confirmDuplicate bool) (Document, error) {
	var doc Document
	var blocked error
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		doc, err = s.loadDoc(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if doc.DocType != "supplier_invoice" {
			return apierr.New(apierr.ValidationError, msg("validation"))
		}
		if doc.Status == statusPosted {
			return nil
		}
		tol, window, err := s.settings(ctx, tx, p)
		if err != nil {
			return err
		}
		block := func(reason string, cause error) error {
			if _, err := tx.Exec(ctx, `UPDATE erp.purchase_docs SET status=$3 WHERE company_id=$1 AND id=$2`, p.CompanyID, id, statusBlocked); err != nil {
				return err
			}
			blocked = cause
			_ = reason
			return nil
		}
		if doc.SourceID == nil {
			return block("missing_lpo", apierr.New(apierr.Conflict, msg("match.blocked")).WithDetails(map[string]any{"reason": "missing_lpo"}))
		}
		if err := s.threeWay(ctx, tx, p, *doc.SourceID, doc.Lines, tol); err != nil {
			return block("three_way", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.purchase_docs
			WHERE company_id=$1 AND vendor_id=$2 AND doc_type='supplier_invoice' AND id<>$3
			AND ((supplier_number=$4 AND supplier_number<>'')
				OR (amount=$5::numeric AND created_at >= clock_timestamp() - make_interval(days => $6)))`,
			p.CompanyID, doc.VendorID, id, doc.SupplierNumber, doc.Amount, window).Scan(&n); err != nil {
			return err
		}
		if n > 0 && !confirmDuplicate {
			return block("duplicate", apierr.New(apierr.Conflict, msg("duplicate.invoice")).WithDetails(map[string]any{"reason": "duplicate"}))
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.purchase_docs
			SET status=$3, posted_at=clock_timestamp(), ledger_posted=false, posting_gap=$4, confirmed_duplicate=$5
			WHERE company_id=$1 AND id=$2`, p.CompanyID, id, statusPosted, PostingGap, confirmDuplicate); err != nil {
			return err
		}
		doc, err = s.loadDoc(ctx, tx, p, id)
		return err
	})
	if err != nil {
		return Document{}, err
	}
	doc, err = s.GetDocument(ctx, p, id)
	return doc, blocked
}

// StartPayment prepares a held payment. Paying a blacklisted vendor waits for a release.
func (s *Service) StartPayment(ctx context.Context, p rls.Principal, invoiceID uuid.UUID) (Started, error) {
	inv, err := s.GetDocument(ctx, p, invoiceID)
	if err != nil {
		return Started{}, err
	}
	payID := uuid.New()
	var number string
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		number, err = s.nextNumber(ctx, tx, p, "payment", "PAY")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.purchase_docs(
			id, company_id, doc_type, number, vendor_id, status, currency, amount, source_id, bank_account, prepared_by)
			VALUES ($1,$2,'payment',$3,$4,'held',$5,$6,$7,$8,$9)`,
			payID, p.CompanyID, number, inv.VendorID, inv.Currency, inv.Amount, invoiceID, inv.BankAccount, p.UserID)
		return err
	})
	if err != nil {
		return Started{}, err
	}
	snap := map[string]any{
		"kind": kindPayment, "payment_id": payID.String(), "vendor_id": inv.VendorID.String(), "invoice_id": invoiceID.String(),
	}
	gate, err := s.open(ctx, p, kindPayment, docPayRelease, inv.VendorID, inv.VendorID.String(), snap)
	if err != nil {
		return Started{}, err
	}
	doc, err := s.GetDocument(ctx, p, payID)
	return Started{Document: doc, Gate: gate}, err
}

// Pay executes a held payment. The vendor preparer cannot pay. A blacklisted vendor needs a release for this payment.
func (s *Service) Pay(ctx context.Context, p rls.Principal, paymentID uuid.UUID) (Document, error) {
	var doc Document
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		doc, err = s.loadDoc(ctx, tx, p, paymentID)
		if err != nil {
			return err
		}
		var prepared string
		var blacklisted bool
		if err := tx.QueryRow(ctx, `SELECT prepared_by, blacklisted FROM erp.purchase_vendors WHERE company_id=$1 AND id=$2`,
			p.CompanyID, doc.VendorID).Scan(&prepared, &blacklisted); err != nil {
			return err
		}
		if prepared == p.UserID {
			return apierr.New(apierr.SoDViolation, msg("payment.preparer"))
		}
		if blacklisted {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.purchase_payment_releases WHERE company_id=$1 AND payment_id=$2`,
				p.CompanyID, paymentID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return apierr.New(apierr.PermissionDenied, msg("payment.held"))
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE erp.purchase_docs SET status=$3 WHERE company_id=$1 AND id=$2`, p.CompanyID, paymentID, statusPaid); err != nil {
			return err
		}
		doc, err = s.loadDoc(ctx, tx, p, paymentID)
		return err
	})
	return doc, err
}

// GetDocument reads one purchase document, including a blacklist flag.
func (s *Service) GetDocument(ctx context.Context, p rls.Principal, id uuid.UUID) (Document, error) {
	var doc Document
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		doc, err = s.loadDoc(ctx, tx, p, id)
		return err
	})
	return doc, err
}

// ListPayables lists supplier invoices and whether each one is flagged.
func (s *Service) ListPayables(ctx context.Context, p rls.Principal) ([]Document, error) {
	var ids []uuid.UUID
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM erp.purchase_docs WHERE company_id=$1 AND doc_type='supplier_invoice' ORDER BY created_at`, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(ids))
	for _, id := range ids {
		doc, err := s.GetDocument(ctx, p, id)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

func (s *Service) createDoc(ctx context.Context, p rls.Principal, docType, prefix, status string, in DocInput) (Document, error) {
	if in.Currency == "" {
		in.Currency = "AED"
	}
	if len(in.Currency) != 3 {
		return Document{}, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "currency"})
	}
	amount, err := sumLines(in.Lines)
	if err != nil {
		return Document{}, err
	}
	id := uuid.New()
	var doc Document
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if err := s.checkLines(ctx, tx, p, in.VendorID, docType, in.Lines); err != nil {
			return err
		}
		var bank string
		if err := tx.QueryRow(ctx, `SELECT bank_account FROM erp.purchase_vendors WHERE company_id=$1 AND id=$2`, p.CompanyID, in.VendorID).Scan(&bank); err != nil {
			return apierr.New(apierr.NotFound, msg("not.found"))
		}
		number, err := s.nextNumber(ctx, tx, p, docType, prefix)
		if err != nil {
			return err
		}
		from, err := parseDate(in.EffectiveFrom)
		if err != nil {
			return err
		}
		to, err := parseDate(in.EffectiveTo)
		if err != nil {
			return err
		}
		var rate any
		if in.FXRate != "" {
			if _, ok := new(big.Rat).SetString(in.FXRate); !ok {
				return apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "fx_rate"})
			}
			rate = in.FXRate
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_docs(
			id, company_id, doc_type, number, vendor_id, status, currency, fx_rate, amount, cost_centre, source_id,
			supplier_number, effective_from, effective_to, bank_account, prepared_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			id, p.CompanyID, docType, number, in.VendorID, status, in.Currency, rate, amount, in.CostCentre, in.SourceID,
			in.SupplierNumber, from, to, bank, p.UserID); err != nil {
			return err
		}
		for i, line := range in.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_lines(company_id, doc_id, line_no, sku_id, qty, unit_price)
				VALUES ($1,$2,$3,$4,$5,$6)`, p.CompanyID, id, i+1, line.SKUID, line.Qty, line.UnitPrice); err != nil {
				return err
			}
		}
		doc, err = s.loadDoc(ctx, tx, p, id)
		return err
	})
	return doc, err
}

func (s *Service) checkLines(ctx context.Context, tx pgx.Tx, p rls.Principal, vendor uuid.UUID, docType string, lines []LineInput) error {
	if len(lines) == 0 && docType != "payment" {
		return apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "lines"})
	}
	var blacklisted bool
	var status string
	if err := tx.QueryRow(ctx, `SELECT status, blacklisted FROM erp.purchase_vendors WHERE company_id=$1 AND id=$2`, p.CompanyID, vendor).Scan(&status, &blacklisted); err != nil {
		return apierr.New(apierr.NotFound, msg("vendor.required"))
	}
	if status != statusApproved {
		return apierr.New(apierr.PermissionDenied, msg("vendor.required"))
	}
	if blacklisted && (docType == "lpo" || docType == "supplier_invoice") {
		return apierr.New(apierr.PermissionDenied, msg("vendor.blacklisted"))
	}
	for _, line := range lines {
		var class string
		if err := tx.QueryRow(ctx, `SELECT item_class FROM erp.purchase_skus WHERE company_id=$1 AND sku_id=$2`, p.CompanyID, line.SKUID).Scan(&class); err != nil {
			return apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "sku_id"})
		}
		if class != classRaw {
			continue
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.purchase_vendor_skus WHERE company_id=$1 AND vendor_id=$2 AND sku_id=$3`,
			p.CompanyID, vendor, line.SKUID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return apierr.New(apierr.ValidationError, msg("sku.required")).WithDetails(map[string]any{"sku_id": line.SKUID.String()})
		}
	}
	return nil
}

func (s *Service) threeWay(ctx context.Context, tx pgx.Tx, p rls.Principal, lpo uuid.UUID, lines []Line, tol string) error {
	prices := map[uuid.UUID]string{}
	rows, err := tx.Query(ctx, `SELECT sku_id, unit_price::text FROM erp.purchase_lines WHERE company_id=$1 AND doc_id=$2`, p.CompanyID, lpo)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var price string
		if err := rows.Scan(&id, &price); err != nil {
			rows.Close()
			return err
		}
		prices[id] = price
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	received := map[uuid.UUID]string{}
	rows, err = tx.Query(ctx, `SELECT l.sku_id, sum(l.qty)::text
		FROM erp.purchase_lines l
		JOIN erp.purchase_docs d ON d.id=l.doc_id
		WHERE d.company_id=$1 AND d.doc_type='goods_receipt' AND d.source_id=$2
		GROUP BY l.sku_id`, p.CompanyID, lpo)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var qty string
		if err := rows.Scan(&id, &qty); err != nil {
			rows.Close()
			return err
		}
		received[id] = qty
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, line := range lines {
		price, ok := prices[line.SKUID]
		if !ok || over(line.UnitPrice, price, tol) || over(line.Qty, received[line.SKUID], tol) {
			return apierr.New(apierr.Conflict, msg("match.blocked")).WithDetails(map[string]any{
				"reason": "three_way", "sku_id": line.SKUID.String(),
			})
		}
	}
	return nil
}

func (s *Service) settings(ctx context.Context, tx pgx.Tx, p rls.Principal) (string, int, error) {
	tol := "0"
	window := 7
	err := tx.QueryRow(ctx, `SELECT match_tolerance_pct::text, duplicate_window_days FROM erp.purchase_settings WHERE company_id=$1`, p.CompanyID).Scan(&tol, &window)
	if errors.Is(err, pgx.ErrNoRows) {
		return "0", 7, nil
	}
	if err != nil {
		return "", 0, err
	}
	return tol, window, nil
}

func (s *Service) nextNumber(ctx context.Context, tx pgx.Tx, p rls.Principal, docType, prefix string) (string, error) {
	var n int64
	err := tx.QueryRow(ctx, `INSERT INTO erp.purchase_counters(company_id, doc_type, next_number) VALUES ($1,$2,1)
		ON CONFLICT (company_id, doc_type) DO UPDATE SET next_number = erp.purchase_counters.next_number + 1
		RETURNING next_number`, p.CompanyID, docType).Scan(&n)
	if err != nil {
		return "", err
	}
	return prefix + "-" + itoa(n), nil
}

func (s *Service) loadDoc(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID) (Document, error) {
	var doc Document
	var source *uuid.UUID
	var from, to *time.Time
	err := tx.QueryRow(ctx, `SELECT id, doc_type, number, status, vendor_id, amount::text, currency,
		coalesce(fx_rate::text, ''), bank_account, supplier_number, ledger_posted, posting_gap, source_id, effective_from, effective_to,
		EXISTS (SELECT 1 FROM erp.purchase_invoice_flags f WHERE f.invoice_id=erp.purchase_docs.id)
		FROM erp.purchase_docs WHERE company_id=$1 AND id=$2`, p.CompanyID, id).
		Scan(&doc.ID, &doc.DocType, &doc.Number, &doc.Status, &doc.VendorID, &doc.Amount, &doc.Currency,
			&doc.FXRate, &doc.BankAccount, &doc.SupplierNumber, &doc.LedgerPosted, &doc.PostingGap, &source, &from, &to, &doc.Flagged)
	if err != nil {
		return Document{}, apierr.New(apierr.NotFound, msg("not.found"))
	}
	doc.SourceID = source
	if from != nil {
		doc.EffectiveFrom = from.Format("2006-01-02")
	}
	if to != nil {
		doc.EffectiveTo = to.Format("2006-01-02")
	}
	rows, err := tx.Query(ctx, `SELECT sku_id, qty::text, unit_price::text FROM erp.purchase_lines WHERE company_id=$1 AND doc_id=$2 ORDER BY line_no`, p.CompanyID, id)
	if err != nil {
		return Document{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var line Line
		if err := rows.Scan(&line.SKUID, &line.Qty, &line.UnitPrice); err != nil {
			return Document{}, err
		}
		doc.Lines = append(doc.Lines, line)
	}
	if doc.Lines == nil {
		doc.Lines = []Line{}
	}
	return doc, rows.Err()
}

func sumLines(lines []LineInput) (string, error) {
	sum := new(big.Rat)
	for _, line := range lines {
		q, ok := new(big.Rat).SetString(line.Qty)
		p, ok2 := new(big.Rat).SetString(line.UnitPrice)
		if !ok || !ok2 {
			return "", apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "lines"})
		}
		sum.Add(sum, new(big.Rat).Mul(q, p))
	}
	return sum.FloatString(2), nil
}

func over(got, want, pct string) bool {
	g, ok1 := new(big.Rat).SetString(got)
	w, ok2 := new(big.Rat).SetString(want)
	if !ok1 || !ok2 {
		return true
	}
	p, ok := new(big.Rat).SetString(pct)
	if !ok {
		p = new(big.Rat)
	}
	p.Quo(p, big.NewRat(100, 1))
	diff := new(big.Rat).Abs(new(big.Rat).Sub(g, w))
	if p.Sign() == 0 {
		return diff.Sign() != 0
	}
	return diff.Cmp(new(big.Rat).Mul(w, p)) > 0
}

func parseDate(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "effective"})
	}
	return t, nil
}

func itoa(n int64) string {
	return big.NewRat(n, 1).RatString()
}
