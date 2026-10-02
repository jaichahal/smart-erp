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

type pinIn struct {
	DocType       string `json:"doc_type"`
	DocID         string `json:"doc_id"`
	CustomerID    string `json:"customer_id"`
	VendorID      string `json:"vendor_id"`
	SKUID         string `json:"sku_id"`
	BOMID         string `json:"bom_id"`
	BankVersionID string `json:"bank_version_id"`
}

type purchaseIn struct {
	Kind     string `json:"kind"`
	VendorID string `json:"vendor_id"`
	SKUID    string `json:"sku_id"`
	Posted   bool   `json:"posted"`
}

type paymentIn struct {
	VendorID  string `json:"vendor_id"`
	InvoiceID string `json:"invoice_id"`
	Amount    string `json:"amount"`
}

func (s *Service) Pin(ctx context.Context, p rls.Principal, in pinIn) (row, error) {
	if in.DocType == "" || in.DocID == "" {
		return nil, apierr.New(apierr.ValidationError, "doc_type and doc_id are required")
	}
	out := row{"doc_type": in.DocType, "doc_id": in.DocID}
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var customerVersion, vendorVersion, skuVersion, bomVersion, bank any
		if in.CustomerID != "" {
			var v string
			if err := tx.QueryRow(ctx, `SELECT coalesce(current_version_id::text,'') FROM erp.customers WHERE id=$1`, in.CustomerID).Scan(&v); err != nil || v == "" {
				return apierr.New(apierr.ValidationError, "customer has no approved version")
			}
			customerVersion = v
			out["customer_version_id"] = v
		}
		if in.VendorID != "" {
			var v string
			if err := tx.QueryRow(ctx, `SELECT coalesce(current_version_id::text,'') FROM erp.vendors WHERE id=$1`, in.VendorID).Scan(&v); err == nil && v != "" {
				vendorVersion = v
				out["vendor_version_id"] = v
			}
		}
		if in.BankVersionID != "" {
			bank = in.BankVersionID
			out["bank_version_id"] = in.BankVersionID
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.master_document_versions
			(company_id, doc_type, doc_id, customer_version_id, vendor_version_id, sku_version_id, bom_version_id, bank_version_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			p.CompanyID, in.DocType, in.DocID, customerVersion, vendorVersion, skuVersion, bomVersion, bank)
		return err
	})
	return out, err
}

func (s *Service) Document(ctx context.Context, p rls.Principal, docType, docID string) (row, error) {
	var customer, vendor, sku, bom, bank *string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT customer_version_id::text, vendor_version_id::text, sku_version_id::text, bom_version_id::text, bank_version_id::text
			FROM erp.master_document_versions WHERE company_id=$1 AND doc_type=$2 AND doc_id=$3`, p.CompanyID, docType, docID).
			Scan(&customer, &vendor, &sku, &bom, &bank)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "document version not found")
	}
	if err != nil {
		return nil, err
	}
	out := row{"doc_type": docType, "doc_id": docID}
	putPtr(out, "customer_version_id", customer)
	putPtr(out, "vendor_version_id", vendor)
	putPtr(out, "sku_version_id", sku)
	putPtr(out, "bom_version_id", bom)
	putPtr(out, "bank_version_id", bank)
	return out, nil
}

func (s *Service) Produce(ctx context.Context, p rls.Principal, bomID uuid.UUID, qty string) (row, error) {
	q, err := mustQty(qty, "qty")
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	var version string
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status, coalesce(current_version_id::text,'') FROM erp.boms WHERE id=$1`, bomID).Scan(&status, &version); err != nil {
			return apierr.New(apierr.NotFound, "bom not found")
		}
		if status != "approved" || version == "" {
			return apierr.New(apierr.ValidationError, "bom is not approved")
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.production_entries (id, company_id, bom_id, bom_version_id, qty, created_by)
			VALUES ($1,$2,$3,$4,$5::numeric,$6)`, id, p.CompanyID, bomID, version, q, p.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "bom_id": bomID.String(), "bom_version_id": version, "qty": q}, nil
}

func (s *Service) Production(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var bom, version, qty string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT bom_id::text, bom_version_id::text, qty::text FROM erp.production_entries WHERE id=$1`, id).Scan(&bom, &version, &qty)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "production entry not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "bom_id": bom, "bom_version_id": version, "qty": qty}, nil
}

func (s *Service) Purchase(ctx context.Context, p rls.Principal, in purchaseIn) (row, error) {
	switch in.Kind {
	case "requisition", "quote", "lpo", "supplier_invoice":
	default:
		return nil, apierr.New(apierr.ValidationError, "kind is not a purchase document")
	}
	id := uuid.New()
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var vendorStatus, vendorVersion string
		if err := tx.QueryRow(ctx, `SELECT status, coalesce(current_version_id::text,'') FROM erp.vendors WHERE id=$1`, in.VendorID).
			Scan(&vendorStatus, &vendorVersion); err != nil {
			return apierr.New(apierr.NotFound, "vendor not found")
		}
		var class, skuStatus string
		if err := tx.QueryRow(ctx, `SELECT item_class, status FROM erp.skus WHERE id=$1`, in.SKUID).Scan(&class, &skuStatus); err != nil {
			return apierr.New(apierr.NotFound, "sku not found")
		}
		if skuStatus != "approved" {
			return apierr.New(apierr.ValidationError, "sku is not approved")
		}
		if (in.Kind == "lpo" || in.Kind == "supplier_invoice") && vendorStatus == "blacklisted" {
			return apierr.New(apierr.PermissionDenied, "blacklisted vendor cannot be used on a new LPO or supplier invoice")
		}
		if class == "finished_goods" {
			return apierr.New(apierr.ValidationError, "finished goods are not purchasable without the both flag")
		}
		if vendorStatus != "approved" {
			return apierr.New(apierr.ValidationError, "vendor is not approved")
		}
		if class == "raw_material" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.vendor_sku_approvals
				WHERE vendor_id=$1 AND sku_id=$2 AND status='approved' AND valid_to IS NULL`, in.VendorID, in.SKUID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return apierr.New(apierr.ValidationError, "vendor is not approved for this SKU")
			}
		}
		var version any
		if vendorVersion != "" {
			version = vendorVersion
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.purchase_documents
			(id, company_id, kind, vendor_id, vendor_version_id, sku_id, posted, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, p.CompanyID, in.Kind, in.VendorID, version, in.SKUID, in.Posted, p.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "kind": in.Kind, "posted": in.Posted, "flagged": false}, nil
}

func (s *Service) PurchaseDoc(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var kind string
	var posted, flagged bool
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT kind, posted, flagged FROM erp.purchase_documents WHERE id=$1`, id).Scan(&kind, &posted, &flagged)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "purchase document not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "kind": kind, "posted": posted, "flagged": flagged}, nil
}

func (s *Service) Pay(ctx context.Context, p rls.Principal, in paymentIn) (row, error) {
	amount, err := mustMoney(in.Amount, "amount")
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	var held bool
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM erp.vendors WHERE id=$1`, in.VendorID).Scan(&status); err != nil {
			return apierr.New(apierr.NotFound, "vendor not found")
		}
		payStatus := "posted"
		if status == "blacklisted" {
			payStatus = "held"
			held = true
		}
		_, err := tx.Exec(ctx, `INSERT INTO erp.vendor_payments (id, company_id, vendor_id, invoice_id, amount, status, created_by)
			VALUES ($1,$2,$3,$4,$5::numeric,$6,$7)`, id, p.CompanyID, in.VendorID, in.InvoiceID, amount, payStatus, p.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if held {
		return nil, apierr.New(apierr.PermissionDenied, "blacklisted vendor payment requires a CFO or Partner release").WithDetails(row{"payment_id": id.String(), "status": "held"})
	}
	return row{"id": id.String(), "status": "posted"}, nil
}

func (s *Service) ReleasePayment(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	ok, err := s.isExecutive(ctx, p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.New(apierr.PermissionDenied, "only a CFO or Partner can release this payment")
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE erp.vendor_payments SET status='released' WHERE id=$1 AND status='held'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apierr.New(apierr.NotFound, "held payment not found")
		}
		_, err = audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "payment.released", ReferenceType: "vendor_payment", ReferenceID: id.String(), Reason: "blacklist release",
			After: row{"status": "released"},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": "released"}, nil
}

func (s *Service) BestPrice(ctx context.Context, p rls.Principal, sku string) (row, error) {
	var vendor, price string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT a.vendor_id::text, a.price::text
			FROM erp.vendor_sku_approvals a
			JOIN erp.vendors v ON v.id = a.vendor_id
			WHERE a.company_id=$1 AND a.sku_id=$2 AND a.status='approved' AND a.valid_to IS NULL
			  AND v.status='approved' AND a.price IS NOT NULL
			ORDER BY a.price, a.vendor_id
			LIMIT 1`, p.CompanyID, sku).Scan(&vendor, &price)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "no approved price for this SKU")
	}
	if err != nil {
		return nil, err
	}
	formatted, err := mustMoney(price, "price")
	if err != nil {
		return nil, err
	}
	return row{"vendor_id": vendor, "sku_id": sku, "price": formatted}, nil
}

func putPtr(m row, key string, v *string) {
	if v != nil {
		m[key] = *v
	}
}
