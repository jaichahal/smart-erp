-- +goose Up
-- P2.2 master data (migrations 31100-31199). Effective-dated versions, four-eyes
-- changes, and the vendor SKU approval list. erp.customers already exists for
-- row scope; this file adds its versions and the rest of the masters.
-- migrations: mutable-only

ALTER TABLE erp.customers
    ADD COLUMN status text NOT NULL DEFAULT 'approved',
    ADD COLUMN state_version bigint NOT NULL DEFAULT 1,
    ADD COLUMN current_version_id uuid,
    ADD COLUMN trn text NOT NULL DEFAULT '',
    ADD COLUMN approval_request_id text;

ALTER TABLE erp.customers DROP CONSTRAINT IF EXISTS customers_status_chk;
ALTER TABLE erp.customers ADD CONSTRAINT customers_status_chk
    CHECK (status IN ('pending_approval', 'approved', 'rejected'));

CREATE POLICY customers_insert ON erp.customers FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.current_user_id() IS NOT NULL);
CREATE POLICY customers_update ON erp.customers FOR UPDATE
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.current_user_id() IS NOT NULL)
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company());

CREATE TABLE erp.customer_versions (
    id                   uuid PRIMARY KEY,
    company_id           uuid        NOT NULL REFERENCES erp.companies (id),
    customer_id          uuid        NOT NULL REFERENCES erp.customers (id),
    version_no           integer     NOT NULL,
    valid_from           timestamptz,
    valid_to             timestamptz,
    approved_by          text,
    change_reason        text        NOT NULL DEFAULT '',
    approval_request_id  text,
    status               text        NOT NULL CHECK (status IN ('approved', 'rejected')),
    name                 text        NOT NULL,
    trn                  text        NOT NULL DEFAULT '',
    addresses            jsonb       NOT NULL DEFAULT '[]'::jsonb,
    contacts             jsonb       NOT NULL DEFAULT '[]'::jsonb,
    territory_id         uuid,
    customer_group       text        NOT NULL DEFAULT '',
    credit_limit         numeric(20, 4) NOT NULL,
    payment_terms_id     uuid,
    price_list_id        uuid,
    interest_rule        text        NOT NULL DEFAULT '',
    dunning_profile      text        NOT NULL DEFAULT '',
    on_hold              boolean     NOT NULL DEFAULT false,
    created_by           text        NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (customer_id, version_no)
);

CREATE TABLE erp.payment_terms (
    id                  uuid PRIMARY KEY,
    company_id          uuid    NOT NULL REFERENCES erp.companies (id),
    code                text    NOT NULL,
    name                text    NOT NULL,
    days                integer NOT NULL,
    status              text    NOT NULL,
    state_version       bigint  NOT NULL,
    current_version_id  uuid,
    approval_request_id text,
    created_by          text    NOT NULL,
    UNIQUE (company_id, code),
    CHECK (status IN ('pending_approval', 'approved', 'rejected'))
);

CREATE TABLE erp.payment_term_versions (
    id                  uuid PRIMARY KEY,
    company_id          uuid        NOT NULL REFERENCES erp.companies (id),
    payment_terms_id    uuid        NOT NULL REFERENCES erp.payment_terms (id),
    version_no          integer     NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text        NOT NULL DEFAULT '',
    approval_request_id text,
    code                text        NOT NULL,
    name                text        NOT NULL,
    days                integer     NOT NULL,
    created_by          text        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (payment_terms_id, version_no)
);

CREATE TABLE erp.price_lists (
    id                  uuid PRIMARY KEY,
    company_id          uuid NOT NULL REFERENCES erp.companies (id),
    name                text NOT NULL,
    currency            text NOT NULL,
    status              text NOT NULL,
    state_version       bigint NOT NULL,
    current_version_id  uuid,
    approval_request_id text,
    created_by          text NOT NULL,
    CHECK (status IN ('pending_approval', 'approved', 'rejected'))
);

CREATE TABLE erp.price_list_versions (
    id                  uuid PRIMARY KEY,
    company_id          uuid        NOT NULL REFERENCES erp.companies (id),
    price_list_id       uuid        NOT NULL REFERENCES erp.price_lists (id),
    version_no          integer     NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text        NOT NULL DEFAULT '',
    approval_request_id text,
    created_by          text        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (price_list_id, version_no)
);

CREATE TABLE erp.price_list_items (
    version_id uuid           NOT NULL REFERENCES erp.price_list_versions (id),
    sku_id     uuid           NOT NULL,
    price      numeric(20, 4) NOT NULL,
    PRIMARY KEY (version_id, sku_id)
);

CREATE TABLE erp.vendors (
    id                  uuid PRIMARY KEY,
    company_id          uuid NOT NULL REFERENCES erp.companies (id),
    name                text NOT NULL,
    trn                 text NOT NULL DEFAULT '',
    vendor_group        text NOT NULL DEFAULT '',
    currency            text NOT NULL DEFAULT 'AED',
    payment_terms_id    uuid,
    status              text NOT NULL,
    state_version       bigint NOT NULL,
    current_version_id  uuid,
    approval_request_id text,
    created_by          text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('pending_approval', 'approved', 'rejected', 'blacklisted')),
    CHECK (status <> 'approved' OR coalesce(approval_request_id, '') <> '')
);

CREATE TABLE erp.vendor_versions (
    id                  uuid PRIMARY KEY,
    company_id          uuid        NOT NULL REFERENCES erp.companies (id),
    vendor_id           uuid        NOT NULL REFERENCES erp.vendors (id),
    version_no          integer     NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text        NOT NULL DEFAULT '',
    approval_request_id text,
    name                text        NOT NULL,
    trn                 text        NOT NULL DEFAULT '',
    addresses           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    contacts            jsonb       NOT NULL DEFAULT '[]'::jsonb,
    vendor_group        text        NOT NULL DEFAULT '',
    currency            text        NOT NULL DEFAULT 'AED',
    payment_terms_id    uuid,
    created_by          text        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (vendor_id, version_no)
);

CREATE TABLE erp.vendor_bank_accounts (
    id                  uuid           NOT NULL,
    version_id          uuid           PRIMARY KEY,
    company_id          uuid           NOT NULL REFERENCES erp.companies (id),
    vendor_id           uuid           NOT NULL REFERENCES erp.vendors (id),
    version_no          integer        NOT NULL,
    holder              text           NOT NULL,
    bank_name           text           NOT NULL,
    iban                text           NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text           NOT NULL DEFAULT '',
    approval_request_id text,
    created_by          text           NOT NULL,
    created_at          timestamptz    NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (id, version_no)
);

CREATE TABLE erp.skus (
    id                  uuid PRIMARY KEY,
    company_id          uuid           NOT NULL REFERENCES erp.companies (id),
    code                text           NOT NULL,
    name                text           NOT NULL,
    item_class          text           NOT NULL,
    base_uom            text           NOT NULL,
    purchase_uom        text           NOT NULL,
    stock_uom           text           NOT NULL,
    production_uom      text           NOT NULL,
    sales_uom           text           NOT NULL,
    floor_price         numeric(20, 4) NOT NULL,
    min_margin          numeric(20, 4) NOT NULL DEFAULT 0,
    reorder_level       numeric(20, 6) NOT NULL DEFAULT 0,
    sku_group           text           NOT NULL DEFAULT '',
    status              text           NOT NULL,
    state_version       bigint         NOT NULL,
    current_version_id  uuid,
    approval_request_id text,
    created_by          text           NOT NULL,
    UNIQUE (company_id, code),
    CHECK (item_class IN ('raw_material', 'finished_goods', 'both')),
    CHECK (status IN ('pending_approval', 'approved', 'rejected'))
);

CREATE TABLE erp.sku_versions (
    id                  uuid PRIMARY KEY,
    company_id          uuid           NOT NULL REFERENCES erp.companies (id),
    sku_id              uuid           NOT NULL REFERENCES erp.skus (id),
    version_no          integer        NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text           NOT NULL DEFAULT '',
    approval_request_id text,
    code                text           NOT NULL,
    name                text           NOT NULL,
    item_class          text           NOT NULL,
    base_uom            text           NOT NULL,
    floor_price         numeric(20, 4) NOT NULL,
    min_margin          numeric(20, 4) NOT NULL,
    reorder_level       numeric(20, 6) NOT NULL,
    sku_group           text           NOT NULL DEFAULT '',
    created_by          text           NOT NULL,
    created_at          timestamptz    NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (sku_id, version_no),
    CHECK (item_class IN ('raw_material', 'finished_goods', 'both'))
);

CREATE TABLE erp.uom_conversions (
    company_id      uuid            NOT NULL REFERENCES erp.companies (id),
    sku_id          uuid            NOT NULL REFERENCES erp.skus (id),
    uom             text            NOT NULL,
    factor_to_base  numeric(30, 12) NOT NULL CHECK (factor_to_base > 0),
    PRIMARY KEY (company_id, sku_id, uom)
);

CREATE TABLE erp.vendor_sku_approvals (
    id                  uuid PRIMARY KEY,
    company_id          uuid           NOT NULL REFERENCES erp.companies (id),
    vendor_id           uuid           NOT NULL REFERENCES erp.vendors (id),
    sku_id              uuid           NOT NULL REFERENCES erp.skus (id),
    status              text           NOT NULL,
    price               numeric(20, 4),
    currency            text           NOT NULL DEFAULT 'AED',
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    approval_request_id text,
    created_by          text           NOT NULL,
    created_at          timestamptz    NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('pending_approval', 'approved', 'rejected'))
);

CREATE TABLE erp.boms (
    id                  uuid PRIMARY KEY,
    company_id          uuid NOT NULL REFERENCES erp.companies (id),
    finished_sku_id     uuid NOT NULL REFERENCES erp.skus (id),
    status              text NOT NULL,
    state_version       bigint NOT NULL,
    current_version_id  uuid,
    approval_request_id text,
    created_by          text NOT NULL,
    CHECK (status IN ('pending_approval', 'approved', 'rejected'))
);

CREATE TABLE erp.bom_versions (
    id                  uuid PRIMARY KEY,
    company_id          uuid        NOT NULL REFERENCES erp.companies (id),
    bom_id              uuid        NOT NULL REFERENCES erp.boms (id),
    version_no          integer     NOT NULL,
    valid_from          timestamptz,
    valid_to            timestamptz,
    approved_by         text,
    change_reason       text        NOT NULL DEFAULT '',
    approval_request_id text,
    created_by          text        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (bom_id, version_no)
);

CREATE TABLE erp.bom_lines (
    version_id     uuid           NOT NULL REFERENCES erp.bom_versions (id),
    line_no        integer        NOT NULL,
    raw_sku_id     uuid           NOT NULL REFERENCES erp.skus (id),
    qty_per_unit   numeric(20, 6) NOT NULL,
    uom            text           NOT NULL,
    wastage_pct    numeric(20, 4) NOT NULL,
    PRIMARY KEY (version_id, line_no)
);

CREATE TABLE erp.price_agreements (
    id                  uuid PRIMARY KEY,
    company_id          uuid           NOT NULL REFERENCES erp.companies (id),
    customer_id         uuid           NOT NULL REFERENCES erp.customers (id),
    sku_id              uuid           NOT NULL REFERENCES erp.skus (id),
    valid_from          date           NOT NULL,
    valid_to            date           NOT NULL,
    min_qty             numeric(20, 6) NOT NULL,
    max_qty             numeric(20, 6) NOT NULL,
    price               numeric(20, 4) NOT NULL,
    currency            text           NOT NULL,
    status              text           NOT NULL,
    state_version       bigint         NOT NULL,
    version_no          integer        NOT NULL,
    approved_by         text,
    change_reason       text           NOT NULL DEFAULT '',
    approval_request_id text,
    created_by          text           NOT NULL,
    created_at          timestamptz    NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('pending_approval', 'approved', 'rejected')),
    CHECK (valid_to >= valid_from)
);

CREATE TABLE erp.master_changes (
    id                  uuid PRIMARY KEY,
    company_id          uuid        NOT NULL REFERENCES erp.companies (id),
    doc_type            text        NOT NULL,
    subject_id          uuid        NOT NULL,
    approval_request_id text        NOT NULL,
    status              text        NOT NULL CHECK (status IN ('pending_approval', 'applied', 'rejected')),
    payload             jsonb       NOT NULL,
    created_by          text        NOT NULL,
    state_version       bigint      NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, approval_request_id)
);
CREATE INDEX master_changes_pending_idx ON erp.master_changes (company_id, subject_id) WHERE status = 'pending_approval';

CREATE TABLE erp.executive_titles (
    company_id uuid        NOT NULL REFERENCES erp.companies (id),
    user_id    text        NOT NULL,
    title      text        NOT NULL CHECK (title IN ('cfo', 'partner')),
    active     boolean     NOT NULL DEFAULT true,
    granted_by text        NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, user_id, title)
);

CREATE TABLE erp.warehouses (
    id         uuid PRIMARY KEY,
    company_id uuid NOT NULL REFERENCES erp.companies (id),
    code       text NOT NULL,
    name       text NOT NULL,
    UNIQUE (company_id, code)
);

CREATE TABLE erp.stock_ledger (
    id            uuid PRIMARY KEY,
    company_id    uuid           NOT NULL REFERENCES erp.companies (id),
    sku_id        uuid           NOT NULL REFERENCES erp.skus (id),
    warehouse_id  uuid           NOT NULL REFERENCES erp.warehouses (id),
    qty_delta     numeric(20, 6) NOT NULL,
    unit_cost     numeric(20, 4) NOT NULL,
    value_delta   numeric(20, 4) NOT NULL,
    movement_type text           NOT NULL,
    source_doc_id text           NOT NULL,
    created_at    timestamptz    NOT NULL DEFAULT clock_timestamp()
);
SELECT erp.make_immutable('erp.stock_ledger');

CREATE TABLE erp.stock_reservations (
    id           uuid PRIMARY KEY,
    company_id   uuid           NOT NULL REFERENCES erp.companies (id),
    sku_id       uuid           NOT NULL REFERENCES erp.skus (id),
    warehouse_id uuid           NOT NULL REFERENCES erp.warehouses (id),
    qty          numeric(20, 6) NOT NULL CHECK (qty > 0),
    status       text           NOT NULL DEFAULT 'active',
    created_at   timestamptz    NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.commercial_orders (
    id                   uuid PRIMARY KEY,
    company_id           uuid           NOT NULL REFERENCES erp.companies (id),
    customer_id          uuid           NOT NULL REFERENCES erp.customers (id),
    customer_version_id  uuid           NOT NULL,
    status               text           NOT NULL,
    total                numeric(20, 4) NOT NULL,
    state_version        bigint         NOT NULL,
    created_by           text           NOT NULL,
    created_at           timestamptz    NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('open', 'held_credit', 'held_floor', 'overridden'))
);

CREATE TABLE erp.commercial_order_lines (
    order_id       uuid           NOT NULL REFERENCES erp.commercial_orders (id),
    line_no        integer        NOT NULL,
    sku_id         uuid           NOT NULL REFERENCES erp.skus (id),
    sku_version_id uuid           NOT NULL,
    qty            numeric(20, 6) NOT NULL,
    uom            text           NOT NULL,
    unit_price     numeric(20, 4) NOT NULL,
    PRIMARY KEY (order_id, line_no)
);

CREATE TABLE erp.master_overrides (
    id         uuid        PRIMARY KEY,
    company_id uuid        NOT NULL REFERENCES erp.companies (id),
    kind       text        NOT NULL,
    subject_id text        NOT NULL,
    actor_id   text        NOT NULL,
    reason     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
SELECT erp.make_immutable('erp.master_overrides');

CREATE TABLE erp.master_document_versions (
    company_id          uuid NOT NULL REFERENCES erp.companies (id),
    doc_type            text NOT NULL,
    doc_id              text NOT NULL,
    customer_version_id uuid,
    vendor_version_id   uuid,
    sku_version_id      uuid,
    bom_version_id      uuid,
    bank_version_id     uuid,
    PRIMARY KEY (company_id, doc_type, doc_id)
);
SELECT erp.make_immutable('erp.master_document_versions');

CREATE TABLE erp.production_entries (
    id             uuid           PRIMARY KEY,
    company_id     uuid           NOT NULL REFERENCES erp.companies (id),
    bom_id         uuid           NOT NULL REFERENCES erp.boms (id),
    bom_version_id uuid           NOT NULL REFERENCES erp.bom_versions (id),
    qty            numeric(20, 6) NOT NULL,
    created_by     text           NOT NULL,
    created_at     timestamptz    NOT NULL DEFAULT clock_timestamp()
);
SELECT erp.make_immutable('erp.production_entries');

CREATE TABLE erp.purchase_documents (
    id                uuid        PRIMARY KEY,
    company_id        uuid        NOT NULL REFERENCES erp.companies (id),
    kind              text        NOT NULL,
    vendor_id         uuid        NOT NULL REFERENCES erp.vendors (id),
    vendor_version_id uuid,
    sku_id            uuid        NOT NULL REFERENCES erp.skus (id),
    posted            boolean     NOT NULL DEFAULT false,
    flagged           boolean     NOT NULL DEFAULT false,
    created_by        text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (kind IN ('requisition', 'quote', 'lpo', 'supplier_invoice'))
);

CREATE TABLE erp.vendor_payments (
    id         uuid           PRIMARY KEY,
    company_id uuid           NOT NULL REFERENCES erp.companies (id),
    vendor_id  uuid           NOT NULL REFERENCES erp.vendors (id),
    invoice_id uuid           NOT NULL,
    amount     numeric(20, 4) NOT NULL,
    status     text           NOT NULL,
    created_by text           NOT NULL,
    created_at timestamptz    NOT NULL DEFAULT clock_timestamp(),
    CHECK (status IN ('held', 'released', 'posted'))
);

-- RLS: tenant scope for erp_app. The migrator owns the tables and bypasses RLS.
-- +goose StatementBegin
DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY[
        'customer_versions', 'payment_terms', 'payment_term_versions', 'price_lists',
        'price_list_versions', 'vendors', 'vendor_versions',
        'vendor_bank_accounts', 'skus', 'sku_versions', 'uom_conversions',
        'vendor_sku_approvals', 'boms', 'bom_versions', 'price_agreements',
        'master_changes', 'executive_titles', 'warehouses', 'stock_ledger',
        'stock_reservations', 'commercial_orders',
        'master_overrides', 'master_document_versions', 'production_entries',
        'purchase_documents', 'vendor_payments'
    ]
    LOOP
        EXECUTE format('ALTER TABLE erp.%I ENABLE ROW LEVEL SECURITY', tbl);
        EXECUTE format(
            'CREATE POLICY %I ON erp.%I FOR ALL TO erp_app USING (company_id IS NOT DISTINCT FROM erp.current_company()) WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company())',
            tbl || '_tenant', tbl);
        EXECUTE format('REVOKE DELETE, TRUNCATE ON erp.%I FROM erp_app', tbl);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Child lines carry no company_id. Visibility follows the parent version or order.
ALTER TABLE erp.price_list_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.bom_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.commercial_order_lines ENABLE ROW LEVEL SECURITY;
REVOKE DELETE, TRUNCATE ON erp.price_list_items, erp.bom_lines, erp.commercial_order_lines FROM erp_app;

CREATE POLICY price_list_items_tenant ON erp.price_list_items FOR ALL TO erp_app
    USING (EXISTS (
        SELECT 1 FROM erp.price_list_versions v
        WHERE v.id = price_list_items.version_id
          AND v.company_id IS NOT DISTINCT FROM erp.current_company()
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM erp.price_list_versions v
        WHERE v.id = price_list_items.version_id
          AND v.company_id IS NOT DISTINCT FROM erp.current_company()
    ));

CREATE POLICY bom_lines_tenant ON erp.bom_lines FOR ALL TO erp_app
    USING (EXISTS (
        SELECT 1 FROM erp.bom_versions v
        WHERE v.id = bom_lines.version_id
          AND v.company_id IS NOT DISTINCT FROM erp.current_company()
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM erp.bom_versions v
        WHERE v.id = bom_lines.version_id
          AND v.company_id IS NOT DISTINCT FROM erp.current_company()
    ));

CREATE POLICY commercial_order_lines_tenant ON erp.commercial_order_lines FOR ALL TO erp_app
    USING (EXISTS (
        SELECT 1 FROM erp.commercial_orders o
        WHERE o.id = commercial_order_lines.order_id
          AND o.company_id IS NOT DISTINCT FROM erp.current_company()
    ))
    WITH CHECK (EXISTS (
        SELECT 1 FROM erp.commercial_orders o
        WHERE o.id = commercial_order_lines.order_id
          AND o.company_id IS NOT DISTINCT FROM erp.current_company()
    ));

-- +goose Down
DROP TABLE IF EXISTS erp.vendor_payments;
DROP TABLE IF EXISTS erp.purchase_documents;
DROP TABLE IF EXISTS erp.production_entries;
DROP TABLE IF EXISTS erp.master_document_versions;
DROP TABLE IF EXISTS erp.master_overrides;
DROP TABLE IF EXISTS erp.commercial_order_lines;
DROP TABLE IF EXISTS erp.commercial_orders;
DROP TABLE IF EXISTS erp.stock_reservations;
DROP TABLE IF EXISTS erp.stock_ledger;
DROP TABLE IF EXISTS erp.warehouses;
DROP TABLE IF EXISTS erp.executive_titles;
DROP TABLE IF EXISTS erp.master_changes;
DROP TABLE IF EXISTS erp.price_agreements;
DROP TABLE IF EXISTS erp.bom_lines;
DROP TABLE IF EXISTS erp.bom_versions;
DROP TABLE IF EXISTS erp.boms;
DROP TABLE IF EXISTS erp.vendor_sku_approvals;
DROP TABLE IF EXISTS erp.uom_conversions;
DROP TABLE IF EXISTS erp.sku_versions;
DROP TABLE IF EXISTS erp.skus;
DROP TABLE IF EXISTS erp.vendor_bank_accounts;
DROP TABLE IF EXISTS erp.vendor_versions;
DROP TABLE IF EXISTS erp.vendors;
DROP TABLE IF EXISTS erp.price_list_items;
DROP TABLE IF EXISTS erp.price_list_versions;
DROP TABLE IF EXISTS erp.price_lists;
DROP TABLE IF EXISTS erp.payment_term_versions;
DROP TABLE IF EXISTS erp.payment_terms;
DROP TABLE IF EXISTS erp.customer_versions;
ALTER TABLE erp.customers DROP CONSTRAINT IF EXISTS customers_status_chk;
ALTER TABLE erp.customers DROP COLUMN IF EXISTS approval_request_id;
ALTER TABLE erp.customers DROP COLUMN IF EXISTS trn;
ALTER TABLE erp.customers DROP COLUMN IF EXISTS current_version_id;
ALTER TABLE erp.customers DROP COLUMN IF EXISTS state_version;
ALTER TABLE erp.customers DROP COLUMN IF EXISTS status;
DELETE FROM erp.immutable_tables WHERE table_name IN (
    'erp.stock_ledger', 'erp.master_overrides', 'erp.master_document_versions', 'erp.production_entries'
);
