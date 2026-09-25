-- +goose Up
-- 31100 (applied on erp_masters) and 32100 (applied on erp_stock) both create
-- erp.stock_reservations, so neither file can be edited. This version sits
-- between them. When the short masters table is what exists, park its rows and
-- drop it so 32100 can create the stock lifecycle. A table that already has
-- order_line_id is the stock shape and is left alone.

CREATE TABLE IF NOT EXISTS erp.stock_reservations_masters_hold (
    id           uuid PRIMARY KEY,
    company_id   uuid        NOT NULL,
    sku_id       uuid        NOT NULL,
    warehouse_id uuid        NOT NULL,
    qty          numeric     NOT NULL,
    status       text        NOT NULL,
    created_at   timestamptz NOT NULL
);

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('erp.stock_reservations_keep') IS NOT NULL THEN
        IF to_regclass('erp.stock_reservations') IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'erp'
              AND table_name = 'stock_reservations'
              AND column_name = 'order_line_id'
        ) THEN
            INSERT INTO erp.stock_reservations_masters_hold (
                id, company_id, sku_id, warehouse_id, qty, status, created_at)
            SELECT id, company_id, sku_id, warehouse_id, qty, COALESCE(status, 'active'), created_at
            FROM erp.stock_reservations
            ON CONFLICT (id) DO NOTHING;
            DROP TABLE erp.stock_reservations;
        END IF;
        ALTER TABLE erp.stock_reservations_keep RENAME TO stock_reservations;
        RETURN;
    END IF;
    IF to_regclass('erp.stock_reservations') IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'erp'
          AND table_name = 'stock_reservations'
          AND column_name = 'order_line_id'
    ) THEN
        RETURN;
    END IF;
    INSERT INTO erp.stock_reservations_masters_hold (
        id, company_id, sku_id, warehouse_id, qty, status, created_at)
    SELECT id, company_id, sku_id, warehouse_id, qty, COALESCE(status, 'active'), created_at
    FROM erp.stock_reservations
    ON CONFLICT (id) DO NOTHING;
    DROP TABLE erp.stock_reservations;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS erp.stock_reservations_masters_hold;
