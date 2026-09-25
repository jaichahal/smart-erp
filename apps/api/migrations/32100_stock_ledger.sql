-- +goose Up
-- P2.3 stock ledger. Moving average is value / quantity per SKU per warehouse.
-- Issues cost at that average. There are no receipt layers and no FIFO.
-- Masters (P2.2) are not merged: sku and warehouse ids are opaque. Item class
-- and UOM live in snapshot tables filled through the stock Catalog interface.

CREATE TABLE erp.stock_skus (
    company_id  uuid NOT NULL REFERENCES erp.companies(id),
    sku_id      uuid NOT NULL,
    sku         text NOT NULL,
    item_class  text NOT NULL CHECK (item_class IN ('raw_material', 'finished_goods', 'both')),
    uom         text NOT NULL,
    PRIMARY KEY (company_id, sku_id)
);

CREATE TABLE erp.stock_warehouses (
    company_id   uuid NOT NULL REFERENCES erp.companies(id),
    warehouse_id uuid NOT NULL,
    code         text NOT NULL,
    PRIMARY KEY (company_id, warehouse_id)
);

CREATE TABLE erp.stock_balances (
    company_id   uuid NOT NULL,
    sku_id       uuid NOT NULL,
    warehouse_id uuid NOT NULL,
    qty          numeric NOT NULL DEFAULT 0 CHECK (qty >= 0),
    value        numeric NOT NULL DEFAULT 0 CHECK (value >= 0),
    PRIMARY KEY (company_id, sku_id, warehouse_id),
    FOREIGN KEY (company_id, sku_id) REFERENCES erp.stock_skus(company_id, sku_id),
    FOREIGN KEY (company_id, warehouse_id) REFERENCES erp.stock_warehouses(company_id, warehouse_id)
);

CREATE TABLE erp.stock_ledger_lines (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id     uuid        NOT NULL,
    sku_id         uuid        NOT NULL,
    warehouse_id   uuid        NOT NULL,
    qty_delta      numeric     NOT NULL CHECK (qty_delta <> 0),
    unit_cost      numeric     NOT NULL CHECK (unit_cost >= 0),
    value_delta    numeric     NOT NULL,
    movement_type  text        NOT NULL CHECK (movement_type IN (
        'receipt', 'delivery', 'production_consume', 'production_produce',
        'adjustment', 'write_off', 'job_out', 'job_in', 'opening')),
    source_doc_id  text        NOT NULL,
    created_by     text        NOT NULL,
    occurred_at    timestamptz NOT NULL,
    canonical      text        NOT NULL,
    chain_seq      bigint      NOT NULL,
    prev_hash      text        NOT NULL,
    hash           text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq)
);
CREATE INDEX stock_ledger_lines_sku_idx
    ON erp.stock_ledger_lines (company_id, sku_id, warehouse_id, occurred_at);

CREATE TABLE erp.stock_reservations (
    id               uuid PRIMARY KEY,
    company_id       uuid    NOT NULL,
    order_line_id    text    NOT NULL,
    sku_id           uuid    NOT NULL,
    warehouse_id     uuid    NOT NULL,
    qty              numeric NOT NULL CHECK (qty > 0),
    status           text    NOT NULL CHECK (status IN ('pending', 'active', 'released', 'consumed')),
    delivery_note_id text,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, order_line_id),
    FOREIGN KEY (company_id, sku_id) REFERENCES erp.stock_skus(company_id, sku_id),
    FOREIGN KEY (company_id, warehouse_id) REFERENCES erp.stock_warehouses(company_id, warehouse_id)
);
CREATE INDEX stock_reservations_active_idx
    ON erp.stock_reservations (company_id, sku_id, warehouse_id)
    WHERE status = 'active';

SELECT erp.make_immutable('erp.stock_ledger_lines');

GRANT SELECT, INSERT, UPDATE ON
    erp.stock_skus, erp.stock_warehouses, erp.stock_balances, erp.stock_reservations
TO erp_app;

ALTER TABLE erp.stock_skus ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.stock_warehouses ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.stock_balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.stock_ledger_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.stock_reservations ENABLE ROW LEVEL SECURITY;

CREATE POLICY stock_skus_tenant ON erp.stock_skus FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY stock_warehouses_tenant ON erp.stock_warehouses FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY stock_balances_tenant ON erp.stock_balances FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY stock_ledger_lines_tenant ON erp.stock_ledger_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY stock_reservations_tenant ON erp.stock_reservations FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose Down
DROP TABLE IF EXISTS erp.stock_reservations;
DROP TABLE IF EXISTS erp.stock_ledger_lines;
DROP TABLE IF EXISTS erp.stock_balances;
DROP TABLE IF EXISTS erp.stock_warehouses;
DROP TABLE IF EXISTS erp.stock_skus;
DELETE FROM erp.immutable_tables WHERE table_name = 'erp.stock_ledger_lines';
