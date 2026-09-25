package stock

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// Item classes from the domain model. Sales sees finished goods and both.
const (
	ClassRaw      = "raw_material"
	ClassFinished = "finished_goods"
	ClassBoth     = "both"
)

// Item is the SKU fact stock needs. Price, floor, and BOM stay in masters.
type Item struct {
	ID        uuid.UUID
	SKU       string
	ItemClass string
	UOM       string
}

// Warehouse is the location fact stock needs.
type Warehouse struct {
	ID   uuid.UUID
	Code string
}

// Catalog loads item and warehouse facts inside the caller's transaction.
// The default implementation is SnapshotCatalog. Masters P2.2 should replace it.
type Catalog interface {
	Item(ctx context.Context, tx pgx.Tx, companyID, skuID uuid.UUID) (Item, error)
	Warehouse(ctx context.Context, tx pgx.Tx, companyID, warehouseID uuid.UUID) (Warehouse, error)
}

// SnapshotCatalog reads erp.stock_skus and erp.stock_warehouses.
type SnapshotCatalog struct{}

func (SnapshotCatalog) Item(ctx context.Context, tx pgx.Tx, companyID, skuID uuid.UUID) (Item, error) {
	var it Item
	err := tx.QueryRow(ctx, `
		SELECT sku_id, sku, item_class, uom
		FROM erp.stock_skus WHERE company_id = $1 AND sku_id = $2`, companyID, skuID).
		Scan(&it.ID, &it.SKU, &it.ItemClass, &it.UOM)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, apierr.New(apierr.NotFound, "sku is not registered")
	}
	if err != nil {
		return Item{}, err
	}
	return it, nil
}

func (SnapshotCatalog) Warehouse(ctx context.Context, tx pgx.Tx, companyID, warehouseID uuid.UUID) (Warehouse, error) {
	var wh Warehouse
	err := tx.QueryRow(ctx, `
		SELECT warehouse_id, code
		FROM erp.stock_warehouses WHERE company_id = $1 AND warehouse_id = $2`, companyID, warehouseID).
		Scan(&wh.ID, &wh.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return Warehouse{}, apierr.New(apierr.NotFound, "warehouse is not registered")
	}
	if err != nil {
		return Warehouse{}, err
	}
	return wh, nil
}
