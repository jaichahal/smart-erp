package masters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func applyDoc(ctx context.Context, tx pgx.Tx, p rls.Principal, docType string, subject uuid.UUID, requestID string, payload []byte, seen int64) error {
	switch docType {
	case docCustomer:
		return applyCustomer(ctx, tx, p, subject, requestID, payload, seen)
	case docVendor:
		return applyVendor(ctx, tx, p, subject, requestID, payload, seen)
	case docBank:
		return applyBank(ctx, tx, p, subject, requestID, payload, seen)
	case docVendorSKU:
		return applyVendorSKU(ctx, tx, p, subject, requestID, payload, seen)
	case docBlacklist:
		return applyBlacklist(ctx, tx, subject, seen)
	case docSKU:
		return applySKU(ctx, tx, p, subject, requestID, payload, seen)
	case docBOM:
		return applyBOM(ctx, tx, p, subject, requestID, payload, seen)
	case docAgreement:
		return applyAgreement(ctx, tx, p, subject, seen)
	default:
		return apierr.New(apierr.ValidationError, "unsupported master change")
	}
}

func applyCustomer(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in customerIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	credit, err := mustMoney(in.CreditLimit, "credit_limit")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.customer_versions SET valid_to = clock_timestamp() WHERE customer_id=$1 AND valid_to IS NULL`, id); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version_no),0)+1 FROM erp.customer_versions WHERE customer_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	version := uuid.New()
	// erp.customers SELECT policy uses row scope. Approvers often lack customer:read,
	// and Postgres requires the row to be selectable before an UPDATE can see it.
	if _, err := tx.Exec(ctx, `SELECT set_config('erp.roles', coalesce(current_setting('erp.roles', true), '') || ',system', true)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.customer_versions
		(id, company_id, customer_id, version_no, valid_from, approved_by, change_reason, approval_request_id, status,
		 name, trn, addresses, contacts, territory_id, customer_group, credit_limit, payment_terms_id, price_list_id,
		 interest_rule, dunning_profile, on_hold, created_by)
		VALUES ($1,$2,$3,$4,clock_timestamp(),$5,$6,$7,'approved',$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17,$18,$19,$20)`,
		version, p.CompanyID, id, n, p.UserID, in.ChangeReason, requestID, in.Name, in.TRN, jsonList(in.Addresses), jsonList(in.Contacts),
		nullUUID(in.TerritoryID), in.Group, credit, nullUUID(in.PaymentTermsID), nullUUID(in.PriceListID), in.InterestRule, in.DunningProfile, in.OnHold, p.UserID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE erp.customers
		SET status='approved', name=$3, trn=$4, territory_id=$5, current_version_id=$6, approval_request_id=coalesce(approval_request_id,$7),
		    state_version=state_version+1
		WHERE id=$1 AND state_version=$2`, id, seen, in.Name, in.TRN, nullUUID(in.TerritoryID), version, requestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "customer version changed")
	}
	return nil
}

func applyVendor(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in vendorIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.vendor_versions SET valid_to = clock_timestamp() WHERE vendor_id=$1 AND valid_to IS NULL`, id); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version_no),0)+1 FROM erp.vendor_versions WHERE vendor_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	version := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO erp.vendor_versions
		(id, company_id, vendor_id, version_no, valid_from, approved_by, change_reason, approval_request_id,
		 name, trn, addresses, contacts, vendor_group, currency, payment_terms_id, created_by)
		VALUES ($1,$2,$3,$4,clock_timestamp(),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		version, p.CompanyID, id, n, p.UserID, in.ChangeReason, requestID, in.Name, in.TRN, jsonList(in.Addresses), jsonList(in.Contacts),
		in.Group, currency(in.Currency), nullUUID(in.PaymentTermsID), p.UserID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE erp.vendors
		SET status='approved', name=$3, trn=$4, vendor_group=$5, currency=$6, current_version_id=$7,
		    approval_request_id=coalesce(approval_request_id,$8), state_version=state_version+1
		WHERE id=$1 AND state_version=$2`, id, seen, in.Name, in.TRN, in.Group, currency(in.Currency), version, requestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "vendor version changed")
	}
	if in.Bank == nil {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO erp.vendor_bank_accounts
		(id, version_id, company_id, vendor_id, version_no, holder, bank_name, iban, valid_from, approved_by, change_reason, approval_request_id, created_by)
		VALUES ($1,$2,$3,$4,1,$5,$6,$7,clock_timestamp(),$8,$9,$10,$11)`,
		uuid.New(), uuid.New(), p.CompanyID, id, in.Bank.Holder, in.Bank.BankName, in.Bank.IBAN, p.UserID, in.ChangeReason, requestID, p.UserID)
	return err
}

func applyBank(ctx context.Context, tx pgx.Tx, p rls.Principal, vendor uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in bankIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	var account uuid.UUID
	var next int
	err := tx.QueryRow(ctx, `SELECT id, version_no+1 FROM erp.vendor_bank_accounts
		WHERE vendor_id=$1 AND valid_to IS NULL ORDER BY version_no DESC LIMIT 1`, vendor).Scan(&account, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		account = uuid.New()
		next = 1
	} else if err != nil {
		return err
	} else if _, err := tx.Exec(ctx, `UPDATE erp.vendor_bank_accounts SET valid_to=clock_timestamp() WHERE vendor_id=$1 AND valid_to IS NULL`, vendor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.vendor_bank_accounts
		(id, version_id, company_id, vendor_id, version_no, holder, bank_name, iban, valid_from, approved_by, change_reason, approval_request_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp(),$9,$10,$11,$12)`,
		account, uuid.New(), p.CompanyID, vendor, next, in.Holder, in.BankName, in.IBAN, p.UserID, in.ChangeReason, requestID, p.UserID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE erp.vendors SET state_version=state_version+1 WHERE id=$1 AND state_version=$2`, vendor, seen)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "vendor version changed")
	}
	return nil
}

func applyVendorSKU(ctx context.Context, tx pgx.Tx, p rls.Principal, vendor uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in skuApprovalIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	price := any(nil)
	if in.Price != "" {
		formatted, err := mustMoney(in.Price, "price")
		if err != nil {
			return err
		}
		price = formatted
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.vendor_sku_approvals
		(id, company_id, vendor_id, sku_id, status, price, currency, valid_from, approved_by, approval_request_id, created_by)
		VALUES ($1,$2,$3,$4,'approved',$5::numeric,$6,clock_timestamp(),$7,$8,$9)`,
		uuid.New(), p.CompanyID, vendor, in.SKUID, price, currency(in.Currency), p.UserID, requestID, p.UserID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE erp.vendors SET state_version=state_version+1 WHERE id=$1 AND state_version=$2`, vendor, seen)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "vendor version changed")
	}
	return nil
}

func applyBlacklist(ctx context.Context, tx pgx.Tx, vendor uuid.UUID, seen int64) error {
	tag, err := tx.Exec(ctx, `UPDATE erp.vendors SET status='blacklisted', state_version=state_version+1 WHERE id=$1 AND state_version=$2`, vendor, seen)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "vendor version changed")
	}
	_, err = tx.Exec(ctx, `UPDATE erp.purchase_documents SET flagged=true WHERE vendor_id=$1 AND kind='supplier_invoice'`, vendor)
	return err
}

func applySKU(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in skuIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	floor, err := mustMoney(in.FloorPrice, "floor_price")
	if err != nil {
		return err
	}
	margin, err := mustMoney(zeroMoney(in.MinMargin), "min_margin")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.sku_versions SET valid_to=clock_timestamp() WHERE sku_id=$1 AND valid_to IS NULL`, id); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version_no),0)+1 FROM erp.sku_versions WHERE sku_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	version := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO erp.sku_versions
		(id, company_id, sku_id, version_no, valid_from, approved_by, change_reason, approval_request_id,
		 code, name, item_class, base_uom, floor_price, min_margin, reorder_level, sku_group, created_by)
		VALUES ($1,$2,$3,$4,clock_timestamp(),$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13::numeric,$14::numeric,$15,$16)`,
		version, p.CompanyID, id, n, p.UserID, in.ChangeReason, requestID, in.Code, in.Name, in.ItemClass, in.BaseUOM,
		floor, margin, zeroQty(in.ReorderLevel), in.Group, p.UserID); err != nil {
		return err
	}
	for _, u := range in.Units {
		factor, ok := rat(u.Factor)
		if !ok || factor.Sign() <= 0 {
			return apierr.New(apierr.ValidationError, "unit factor must be positive")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.uom_conversions (company_id, sku_id, uom, factor_to_base)
			VALUES ($1,$2,$3,$4::numeric)
			ON CONFLICT (company_id, sku_id, uom) DO UPDATE SET factor_to_base=EXCLUDED.factor_to_base`,
			p.CompanyID, id, u.UOM, quantize(factor, 12)); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE erp.skus
		SET status='approved', code=$3, name=$4, item_class=$5, base_uom=$6, purchase_uom=$7, stock_uom=$8, production_uom=$9, sales_uom=$10,
		    floor_price=$11::numeric, min_margin=$12::numeric, reorder_level=$13::numeric, sku_group=$14, current_version_id=$15,
		    state_version=state_version+1
		WHERE id=$1 AND state_version=$2`,
		id, seen, in.Code, in.Name, in.ItemClass, in.BaseUOM, in.PurchaseUOM, in.StockUOM, in.ProductionUOM, in.SalesUOM,
		floor, margin, zeroQty(in.ReorderLevel), in.Group, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "sku version changed")
	}
	return nil
}

func applyBOM(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID, requestID string, raw []byte, seen int64) error {
	var in bomIn
	if err := decodePayload(raw, &in); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.bom_versions SET valid_to=clock_timestamp() WHERE bom_id=$1 AND valid_to IS NULL`, id); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version_no),0)+1 FROM erp.bom_versions WHERE bom_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	version := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO erp.bom_versions
		(id, company_id, bom_id, version_no, valid_from, approved_by, change_reason, approval_request_id, created_by)
		VALUES ($1,$2,$3,$4,clock_timestamp(),$5,$6,$7,$8)`,
		version, p.CompanyID, id, n, p.UserID, in.ChangeReason, requestID, p.UserID); err != nil {
		return err
	}
	for i, line := range in.Lines {
		qty, err := mustQty(line.QtyPerUnit, "qty_per_unit")
		if err != nil {
			return err
		}
		waste, err := mustMoney(zeroMoney(line.WastagePct), "wastage_pct")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.bom_lines (version_id, line_no, raw_sku_id, qty_per_unit, uom, wastage_pct)
			VALUES ($1,$2,$3,$4::numeric,$5,$6::numeric)`, version, i+1, line.RawSKUID, qty, line.UOM, waste); err != nil {
			return err
		}
	}
	finished := nullUUID(in.FinishedSKUID)
	tag, err := tx.Exec(ctx, `UPDATE erp.boms
		SET status='approved', finished_sku_id=coalesce($3, finished_sku_id), current_version_id=$4, state_version=state_version+1
		WHERE id=$1 AND state_version=$2`, id, seen, finished, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "bom version changed")
	}
	return nil
}

func applyAgreement(ctx context.Context, tx pgx.Tx, p rls.Principal, id uuid.UUID, seen int64) error {
	tag, err := tx.Exec(ctx, `UPDATE erp.price_agreements
		SET status='approved', approved_by=$3, state_version=state_version+1
		WHERE id=$1 AND state_version=$2`, id, seen, p.UserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "price agreement changed")
	}
	return nil
}
