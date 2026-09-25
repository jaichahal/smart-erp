-- +goose Up
-- One erp.stock_reservations: stock columns and lifecycle, foreign keys on the
-- masters tables. 32100 already created the lifecycle with snapshot foreign keys.
-- This file repoints them at erp.companies, erp.skus, and erp.warehouses when
-- those tables exist, and restores any rows parked by 32000.

INSERT INTO erp.stock_reservations (
    id, company_id, order_line_id, sku_id, warehouse_id, qty, status, created_at)
SELECT h.id, h.company_id, 'migrated-' || h.id::text, h.sku_id, h.warehouse_id, h.qty,
       CASE
           WHEN h.status IN ('pending', 'active', 'released', 'consumed') THEN h.status
           ELSE 'active'
       END,
       h.created_at
FROM erp.stock_reservations_masters_hold h
WHERE EXISTS (SELECT 1 FROM erp.skus s WHERE s.id = h.sku_id)
  AND EXISTS (SELECT 1 FROM erp.warehouses w WHERE w.id = h.warehouse_id)
  AND EXISTS (SELECT 1 FROM erp.companies c WHERE c.id = h.company_id)
  AND NOT EXISTS (SELECT 1 FROM erp.stock_reservations r WHERE r.id = h.id);

DELETE FROM erp.stock_reservations_masters_hold h
WHERE EXISTS (SELECT 1 FROM erp.stock_reservations r WHERE r.id = h.id);

DROP TABLE IF EXISTS erp.stock_reservations_masters_hold;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('erp.skus') IS NULL
       OR to_regclass('erp.warehouses') IS NULL
       OR to_regclass('erp.companies') IS NULL
       OR to_regclass('erp.stock_reservations') IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (
        SELECT 1 FROM erp.stock_reservations r
        WHERE NOT EXISTS (SELECT 1 FROM erp.skus s WHERE s.id = r.sku_id)
           OR NOT EXISTS (SELECT 1 FROM erp.warehouses w WHERE w.id = r.warehouse_id)
           OR NOT EXISTS (SELECT 1 FROM erp.companies c WHERE c.id = r.company_id)
    ) THEN
        RETURN;
    END IF;

    ALTER TABLE erp.stock_reservations DROP CONSTRAINT IF EXISTS stock_reservations_company_id_sku_id_fkey;
    ALTER TABLE erp.stock_reservations DROP CONSTRAINT IF EXISTS stock_reservations_company_id_warehouse_id_fkey;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'erp.stock_reservations'::regclass
          AND conname = 'stock_reservations_company_id_fkey'
    ) THEN
        ALTER TABLE erp.stock_reservations
            ADD CONSTRAINT stock_reservations_company_id_fkey
            FOREIGN KEY (company_id) REFERENCES erp.companies (id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'erp.stock_reservations'::regclass
          AND conname = 'stock_reservations_sku_id_fkey'
    ) THEN
        ALTER TABLE erp.stock_reservations
            ADD CONSTRAINT stock_reservations_sku_id_fkey
            FOREIGN KEY (sku_id) REFERENCES erp.skus (id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'erp.stock_reservations'::regclass
          AND conname = 'stock_reservations_warehouse_id_fkey'
    ) THEN
        ALTER TABLE erp.stock_reservations
            ADD CONSTRAINT stock_reservations_warehouse_id_fkey
            FOREIGN KEY (warehouse_id) REFERENCES erp.warehouses (id);
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE erp.stock_reservations DROP CONSTRAINT IF EXISTS stock_reservations_company_id_fkey;
ALTER TABLE erp.stock_reservations DROP CONSTRAINT IF EXISTS stock_reservations_sku_id_fkey;
ALTER TABLE erp.stock_reservations DROP CONSTRAINT IF EXISTS stock_reservations_warehouse_id_fkey;

ALTER TABLE erp.stock_reservations
    ADD CONSTRAINT stock_reservations_company_id_sku_id_fkey
    FOREIGN KEY (company_id, sku_id) REFERENCES erp.stock_skus (company_id, sku_id);
ALTER TABLE erp.stock_reservations
    ADD CONSTRAINT stock_reservations_company_id_warehouse_id_fkey
    FOREIGN KEY (company_id, warehouse_id) REFERENCES erp.stock_warehouses (company_id, warehouse_id);
