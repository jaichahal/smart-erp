-- +goose Up
-- erp_stock applied 32100 before 31100 was in the tree. 31100 cannot be edited
-- and it creates erp.stock_reservations. When the stock lifecycle table is
-- already present, rename it aside so that create can succeed. 32000 puts the
-- stock table back. A fresh database has no reservations table yet.

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('erp.stock_reservations') IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'erp'
          AND table_name = 'stock_reservations'
          AND column_name = 'order_line_id'
    ) THEN
        RETURN;
    END IF;
    ALTER TABLE erp.stock_reservations RENAME TO stock_reservations_keep;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('erp.stock_reservations_keep') IS NOT NULL
       AND to_regclass('erp.stock_reservations') IS NULL THEN
        ALTER TABLE erp.stock_reservations_keep RENAME TO stock_reservations;
    END IF;
END $$;
-- +goose StatementEnd
