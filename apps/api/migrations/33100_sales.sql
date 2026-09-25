-- +goose Up
-- Sales documents (P3). Draft rows are mutable. Registered snapshots, stored PDFs,
-- overrides, and outbound events are append-only. An order does not post a journal.

CREATE TABLE erp.sales_suppliers (
    company_id uuid PRIMARY KEY,
    name       text NOT NULL DEFAULT '',
    address    text NOT NULL DEFAULT '',
    trn        text NOT NULL DEFAULT ''
);

CREATE TABLE erp.sales_customers (
    company_id          uuid NOT NULL,
    id                  text NOT NULL,
    name                text NOT NULL,
    address             text NOT NULL DEFAULT '',
    trn                 text NOT NULL DEFAULT '',
    vat_registered      boolean NOT NULL DEFAULT true,
    credit_limit        text NOT NULL,
    payment_terms_days  int NOT NULL DEFAULT 0,
    discount_days       int NOT NULL DEFAULT 0,
    discount_rate       text NOT NULL DEFAULT '0',
    version_id          text NOT NULL,
    cash_sale           boolean NOT NULL DEFAULT false,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_skus (
    company_id           uuid NOT NULL,
    sku                  text NOT NULL,
    description          text NOT NULL,
    uom                  text NOT NULL DEFAULT 'ea',
    floor_price          text NOT NULL,
    tax_code             text NOT NULL,
    tax_code_version_id  text NOT NULL,
    tax_rate             text NOT NULL,
    PRIMARY KEY (company_id, sku)
);

CREATE TABLE erp.sales_agreements (
    company_id  uuid NOT NULL,
    id          text NOT NULL,
    customer_id text NOT NULL,
    sku         text NOT NULL,
    qty_min     text NOT NULL DEFAULT '0',
    price       text NOT NULL,
    valid_from  date NOT NULL,
    valid_to    date NOT NULL,
    version_id  text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_stock (
    company_id   uuid NOT NULL,
    sku          text NOT NULL,
    warehouse_id text NOT NULL,
    on_hand      text NOT NULL,
    value        text NOT NULL,
    PRIMARY KEY (company_id, sku, warehouse_id)
);

CREATE TABLE erp.sales_orders (
    company_id       uuid NOT NULL,
    id               text NOT NULL,
    customer_id      text NOT NULL,
    warehouse_id     text NOT NULL,
    status           text NOT NULL,
    currency         text NOT NULL,
    total            text NOT NULL,
    order_date       date NOT NULL,
    offline          boolean NOT NULL DEFAULT false,
    agent_notice     text NOT NULL DEFAULT '',
    holds            text[] NOT NULL DEFAULT '{}',
    idempotency_key  uuid,
    state_version    bigint NOT NULL DEFAULT 1,
    created_by       text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, id)
);
CREATE UNIQUE INDEX sales_orders_idem_idx ON erp.sales_orders (company_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE erp.sales_order_lines (
    company_id   uuid NOT NULL,
    id           text NOT NULL,
    order_id     text NOT NULL,
    line_no      int NOT NULL,
    sku          text NOT NULL,
    qty          text NOT NULL,
    uom          text NOT NULL,
    unit_price   text NOT NULL,
    tax_code     text NOT NULL,
    tax_rate     text NOT NULL,
    agreement_id text NOT NULL DEFAULT '',
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_reservations (
    company_id    uuid NOT NULL,
    id            text NOT NULL,
    order_id      text NOT NULL,
    order_line_id text NOT NULL,
    sku           text NOT NULL,
    warehouse_id  text NOT NULL,
    qty           text NOT NULL,
    status        text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_invoices (
    company_id            uuid NOT NULL,
    id                    text NOT NULL,
    order_id              text NOT NULL,
    customer_id           text NOT NULL,
    status                text NOT NULL,
    currency              text NOT NULL,
    invoice_date          date NOT NULL,
    supply_date           date NOT NULL,
    due_date              date NOT NULL,
    document_title        text NOT NULL DEFAULT 'Tax Invoice',
    gross                 text NOT NULL,
    discount              text NOT NULL,
    tax                   text NOT NULL,
    rounding              text NOT NULL,
    total                 text NOT NULL,
    number                text NOT NULL DEFAULT '',
    content_hash          text NOT NULL DEFAULT '',
    approved_hash         text NOT NULL DEFAULT '',
    print_enabled         boolean NOT NULL DEFAULT false,
    gate_pass_enabled     boolean NOT NULL DEFAULT false,
    customer_version_id   text NOT NULL DEFAULT '',
    price_version_id      text NOT NULL DEFAULT '',
    tax_code_version_id   text NOT NULL DEFAULT '',
    cash_sale             boolean NOT NULL DEFAULT false,
    discount_days         int NOT NULL DEFAULT 0,
    discount_rate         text NOT NULL DEFAULT '0',
    warnings              text[] NOT NULL DEFAULT '{}',
    state_version         bigint NOT NULL DEFAULT 1,
    created_by            text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_invoice_lines (
    company_id   uuid NOT NULL,
    id           text NOT NULL,
    invoice_id   text NOT NULL,
    line_no      int NOT NULL,
    sku          text NOT NULL,
    description  text NOT NULL,
    qty          text NOT NULL,
    uom          text NOT NULL,
    unit_price   text NOT NULL,
    discount     text NOT NULL DEFAULT '0.00',
    tax_code     text NOT NULL,
    tax_rate     text NOT NULL,
    tax_amount   text NOT NULL,
    line_total   text NOT NULL,
    agreement_id text NOT NULL DEFAULT '',
    PRIMARY KEY (company_id, id)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.sales_freeze_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'registered' THEN
        RAISE EXCEPTION 'immutable: registered sales invoice' USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sales_freeze_invoice
    BEFORE UPDATE ON erp.sales_invoices
    FOR EACH ROW EXECUTE FUNCTION erp.sales_freeze_invoice();

CREATE TABLE erp.sales_registrations (
    company_id           uuid NOT NULL,
    invoice_id           text NOT NULL,
    doc_type             text NOT NULL,
    fiscal_year          int NOT NULL,
    number               bigint NOT NULL,
    content_hash         text NOT NULL,
    customer_version_id  text NOT NULL,
    price_version_id     text NOT NULL,
    tax_code_version_id  text NOT NULL,
    canonical            text NOT NULL,
    pdf_hash             text NOT NULL,
    registered_by        text NOT NULL,
    registered_at        timestamptz NOT NULL,
    PRIMARY KEY (company_id, invoice_id),
    UNIQUE (company_id, doc_type, fiscal_year, number)
);

CREATE TABLE erp.sales_pdfs (
    company_id uuid NOT NULL,
    doc_type   text NOT NULL,
    doc_id     text NOT NULL,
    pdf        bytea NOT NULL,
    sha256     text NOT NULL,
    PRIMARY KEY (company_id, doc_type, doc_id)
);

CREATE TABLE erp.sales_sequences (
    company_id  uuid NOT NULL,
    doc_type    text NOT NULL,
    fiscal_year int NOT NULL,
    next_number bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, doc_type, fiscal_year)
);

CREATE TABLE erp.sales_gate_passes (
    company_id       uuid NOT NULL,
    id               text NOT NULL,
    invoice_id       text NOT NULL,
    delivery_id      text NOT NULL,
    status           text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_deliveries (
    company_id      uuid NOT NULL,
    id              text NOT NULL,
    invoice_id      text NOT NULL,
    order_id        text NOT NULL,
    gate_pass_id    text NOT NULL,
    status          text NOT NULL,
    proof_hash      text NOT NULL DEFAULT '',
    confirmed_at    timestamptz,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_delivery_lines (
    company_id  uuid NOT NULL,
    id          text NOT NULL,
    delivery_id text NOT NULL,
    sku         text NOT NULL,
    qty         text NOT NULL,
    unit_cost   text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_backorders (
    company_id uuid NOT NULL,
    id         text NOT NULL,
    order_id   text NOT NULL,
    sku        text NOT NULL,
    qty        text NOT NULL,
    status     text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_clocks (
    company_id   uuid NOT NULL,
    id           text NOT NULL,
    doc_id       text NOT NULL,
    doc_type     text NOT NULL,
    kind         text NOT NULL,
    started_at   timestamptz NOT NULL,
    due_at       timestamptz NOT NULL,
    closed_at    timestamptz,
    escalated_at timestamptz,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_credit_notes (
    company_id           uuid NOT NULL,
    id                   text NOT NULL,
    invoice_id           text NOT NULL,
    status               text NOT NULL,
    number               text NOT NULL DEFAULT '',
    document_title       text NOT NULL DEFAULT 'Tax Credit Note',
    total                text NOT NULL,
    tax                  text NOT NULL,
    restore_stock        boolean NOT NULL DEFAULT false,
    state_version        bigint NOT NULL DEFAULT 1,
    created_by           text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_credit_lines (
    company_id     uuid NOT NULL,
    id             text NOT NULL,
    credit_note_id text NOT NULL,
    sku            text NOT NULL,
    qty            text NOT NULL,
    unit_price     text NOT NULL,
    tax_amount     text NOT NULL,
    unit_cost      text NOT NULL DEFAULT '0.00',
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_receipts (
    company_id  uuid NOT NULL,
    id          text NOT NULL,
    invoice_id  text NOT NULL,
    amount      text NOT NULL,
    discount    text NOT NULL DEFAULT '0.00',
    paid_on     date NOT NULL,
    created_by  text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_postings (
    company_id uuid NOT NULL,
    id         text NOT NULL,
    doc_type   text NOT NULL,
    doc_id     text NOT NULL,
    line_no    int NOT NULL,
    role       text NOT NULL,
    debit      text NOT NULL,
    credit     text NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_events (
    company_id  uuid NOT NULL,
    id          text NOT NULL,
    type        text NOT NULL,
    doc_type    text NOT NULL,
    doc_id      text NOT NULL,
    occurred_at timestamptz NOT NULL,
    payload     jsonb NOT NULL,
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_tolerances (
    company_id uuid NOT NULL,
    user_id    text NOT NULL,
    max_amount text NOT NULL,
    PRIMARY KEY (company_id, user_id)
);

CREATE TABLE erp.sales_rules (
    company_id uuid NOT NULL,
    id         text NOT NULL,
    doc_type   text NOT NULL,
    name       text NOT NULL,
    mode       text NOT NULL,
    field      text NOT NULL,
    op         text NOT NULL,
    value      text NOT NULL DEFAULT '',
    PRIMARY KEY (company_id, id)
);

CREATE TABLE erp.sales_targets (
    company_id       uuid NOT NULL,
    agent_id         text NOT NULL,
    period           text NOT NULL,
    target_amount    text NOT NULL,
    commission_rate  text NOT NULL,
    PRIMARY KEY (company_id, agent_id, period)
);

CREATE TABLE erp.sales_overrides (
    company_id uuid NOT NULL,
    id         text NOT NULL,
    order_id   text NOT NULL,
    hold       text NOT NULL,
    actor_id   text NOT NULL,
    actor_role text NOT NULL,
    reason     text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (company_id, id)
);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'erp.sales_suppliers','erp.sales_customers','erp.sales_skus','erp.sales_agreements','erp.sales_stock',
        'erp.sales_orders','erp.sales_order_lines','erp.sales_reservations','erp.sales_invoices','erp.sales_invoice_lines',
        'erp.sales_registrations','erp.sales_pdfs','erp.sales_sequences','erp.sales_gate_passes','erp.sales_deliveries',
        'erp.sales_delivery_lines','erp.sales_backorders','erp.sales_clocks','erp.sales_credit_notes','erp.sales_credit_lines',
        'erp.sales_receipts','erp.sales_postings','erp.sales_events','erp.sales_tolerances','erp.sales_rules',
        'erp.sales_targets','erp.sales_overrides'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format($f$CREATE POLICY sales_tenant ON %s FOR ALL TO erp_app
            USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
            WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))$f$, t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %s TO erp_app', t);
    END LOOP;
END $$;
-- +goose StatementEnd

SELECT erp.make_immutable('erp.sales_registrations');
SELECT erp.make_immutable('erp.sales_pdfs');
SELECT erp.make_immutable('erp.sales_events');
SELECT erp.make_immutable('erp.sales_overrides');

-- +goose Down
DROP TRIGGER IF EXISTS sales_freeze_invoice ON erp.sales_invoices;
DROP FUNCTION IF EXISTS erp.sales_freeze_invoice();
DROP TABLE IF EXISTS erp.sales_overrides;
DROP TABLE IF EXISTS erp.sales_targets;
DROP TABLE IF EXISTS erp.sales_rules;
DROP TABLE IF EXISTS erp.sales_tolerances;
DROP TABLE IF EXISTS erp.sales_events;
DROP TABLE IF EXISTS erp.sales_postings;
DROP TABLE IF EXISTS erp.sales_receipts;
DROP TABLE IF EXISTS erp.sales_credit_lines;
DROP TABLE IF EXISTS erp.sales_credit_notes;
DROP TABLE IF EXISTS erp.sales_clocks;
DROP TABLE IF EXISTS erp.sales_backorders;
DROP TABLE IF EXISTS erp.sales_delivery_lines;
DROP TABLE IF EXISTS erp.sales_deliveries;
DROP TABLE IF EXISTS erp.sales_gate_passes;
DROP TABLE IF EXISTS erp.sales_sequences;
DROP TABLE IF EXISTS erp.sales_pdfs;
DROP TABLE IF EXISTS erp.sales_registrations;
DROP TABLE IF EXISTS erp.sales_invoice_lines;
DROP TABLE IF EXISTS erp.sales_invoices;
DROP TABLE IF EXISTS erp.sales_reservations;
DROP TABLE IF EXISTS erp.sales_order_lines;
DROP TABLE IF EXISTS erp.sales_orders;
DROP TABLE IF EXISTS erp.sales_stock;
DROP TABLE IF EXISTS erp.sales_agreements;
DROP TABLE IF EXISTS erp.sales_skus;
DROP TABLE IF EXISTS erp.sales_customers;
DROP TABLE IF EXISTS erp.sales_suppliers;
DELETE FROM erp.immutable_tables WHERE table_name IN (
    'erp.sales_registrations','erp.sales_pdfs','erp.sales_events','erp.sales_overrides'
);
