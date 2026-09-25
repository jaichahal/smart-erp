package masters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func head(ctx context.Context, tx pgx.Tx, docType string, id uuid.UUID) (row, error) {
	switch docType {
	case docCustomer:
		return customerHead(ctx, tx, id)
	case docVendor, docVendorEdit, docBank, docVendorSKU, docBlacklist:
		return vendorHead(ctx, tx, id)
	case docSKU:
		return skuHead(ctx, tx, id)
	case docBOM:
		return bomHead(ctx, tx, id)
	case docAgreement:
		return agreementHead(ctx, tx, id)
	default:
		return nil, apierr.New(apierr.NotFound, "master record not found")
	}
}

func (s *Service) view(ctx context.Context, p rls.Principal, docType string, id uuid.UUID) (row, error) {
	switch docType {
	case docCustomer:
		return s.Customer(ctx, p, id)
	case docVendor, docVendorEdit, docBank, docVendorSKU, docBlacklist:
		return s.Vendor(ctx, p, id)
	case docSKU:
		return s.SKU(ctx, p, id)
	case docBOM:
		return s.BOM(ctx, p, id)
	case docAgreement:
		return s.Agreement(ctx, p, id)
	default:
		return nil, apierr.New(apierr.NotFound, "master record not found")
	}
}

func customerHead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (row, error) {
	var status, version, credit string
	var state int64
	err := tx.QueryRow(ctx, `SELECT c.status, c.state_version, coalesce(c.current_version_id::text,''), coalesce(v.credit_limit::text,'0.0000')
		FROM erp.customers c
		LEFT JOIN erp.customer_versions v ON v.id = c.current_version_id
		WHERE c.id=$1`, id).Scan(&status, &state, &version, &credit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "customer not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version, "credit_limit": credit}, nil
}

func vendorHead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (row, error) {
	var status, version, bank string
	var state int64
	err := tx.QueryRow(ctx, `SELECT v.status, v.state_version, coalesce(v.current_version_id::text,''), coalesce(b.version_id::text,'')
		FROM erp.vendors v
		LEFT JOIN LATERAL (
			SELECT version_id FROM erp.vendor_bank_accounts
			WHERE vendor_id=v.id AND valid_to IS NULL
			ORDER BY version_no DESC LIMIT 1
		) b ON true
		WHERE v.id=$1`, id).Scan(&status, &state, &version, &bank)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "vendor not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version, "bank_version_id": bank}, nil
}

func skuHead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (row, error) {
	var status, class, version, floor string
	var state int64
	err := tx.QueryRow(ctx, `SELECT status, state_version, item_class, coalesce(current_version_id::text,''), floor_price::text
		FROM erp.skus WHERE id=$1`, id).Scan(&status, &state, &class, &version, &floor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "sku not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "item_class": class, "version_id": version, "floor_price": floor}, nil
}

func bomHead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (row, error) {
	var status, version string
	var state int64
	err := tx.QueryRow(ctx, `SELECT status, state_version, coalesce(current_version_id::text,'') FROM erp.boms WHERE id=$1`, id).
		Scan(&status, &state, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "bom not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version}, nil
}

func agreementHead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (row, error) {
	var status string
	var state int64
	err := tx.QueryRow(ctx, `SELECT status, state_version FROM erp.price_agreements WHERE id=$1`, id).Scan(&status, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "price agreement not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": id.String()}, nil
}

func (s *Service) Customer(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var status, version, credit string
	var state int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.status, c.state_version, coalesce(c.current_version_id::text,''), coalesce(v.credit_limit::text,'0.0000')
			FROM erp.customers c
			LEFT JOIN erp.customer_versions v ON v.id = c.current_version_id
			WHERE c.id=$1`, id).Scan(&status, &state, &version, &credit)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "customer not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version, "credit_limit": credit}, nil
}

func (s *Service) Vendor(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var status, version, bank string
	var state int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT v.status, v.state_version, coalesce(v.current_version_id::text,''), coalesce(b.version_id::text,'')
			FROM erp.vendors v
			LEFT JOIN LATERAL (
				SELECT version_id FROM erp.vendor_bank_accounts
				WHERE vendor_id=v.id AND valid_to IS NULL
				ORDER BY version_no DESC LIMIT 1
			) b ON true
			WHERE v.id=$1`, id).Scan(&status, &state, &version, &bank)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "vendor not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version, "bank_version_id": bank}, nil
}

func (s *Service) SKU(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var status, class, version, floor string
	var state int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, state_version, item_class, coalesce(current_version_id::text,''), floor_price::text
			FROM erp.skus WHERE id=$1`, id).Scan(&status, &state, &class, &version, &floor)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "sku not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "item_class": class, "version_id": version, "floor_price": floor}, nil
}

func (s *Service) BOM(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var status, version string
	var state int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, state_version, coalesce(current_version_id::text,'') FROM erp.boms WHERE id=$1`, id).
			Scan(&status, &state, &version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "bom not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": version}, nil
}

func (s *Service) Agreement(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var status string
	var state int64
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, state_version FROM erp.price_agreements WHERE id=$1`, id).Scan(&status, &state)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "price agreement not found")
	}
	if err != nil {
		return nil, err
	}
	return row{"id": id.String(), "status": status, "state_version": state, "version_id": id.String()}, nil
}

func (s *Service) BOMVersions(ctx context.Context, p rls.Principal, id uuid.UUID) (row, error) {
	var rows []row
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		list, err := tx.Query(ctx, `SELECT id::text, version_no, coalesce(change_reason,'') FROM erp.bom_versions WHERE bom_id=$1 ORDER BY version_no`, id)
		if err != nil {
			return err
		}
		defer list.Close()
		for list.Next() {
			var vid, reason string
			var n int
			if err := list.Scan(&vid, &n, &reason); err != nil {
				return err
			}
			rows = append(rows, row{"id": vid, "version_no": n, "change_reason": reason})
		}
		return list.Err()
	})
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []row{}
	}
	return row{"rows": rows}, nil
}
