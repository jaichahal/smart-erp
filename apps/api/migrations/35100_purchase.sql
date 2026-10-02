-- +goose Up
-- Phase 6 purchase documents and vendor control (P6.1 to P6.3, R22).
-- Ledger journals are not created here. This branch has period guards and no
-- inventory, received-not-billed, VAT input, or payable journal interface.
-- Goods receipt and supplier-invoice posting record that gap on the document.
--
-- An approved vendor, an approved SKU link, and a blacklist cannot be written
-- unless a posting token was consumed for that request and a CFO or Partner
-- approved it. Delegation of those requests is rejected by the trigger.

CREATE TABLE erp.purchase_titles (
    company_id uuid NOT NULL,
    user_id    text NOT NULL,
    title      text NOT NULL CHECK (title IN ('cfo', 'partner')),
    PRIMARY KEY (company_id, user_id)
);

CREATE TABLE erp.purchase_skus (
    company_id uuid NOT NULL,
    sku_id     uuid NOT NULL,
    code       text NOT NULL,
    item_class text NOT NULL CHECK (item_class IN ('raw_material', 'finished_goods', 'both')),
    PRIMARY KEY (company_id, sku_id)
);

CREATE TABLE erp.purchase_requests (
    id                  uuid        PRIMARY KEY,
    company_id          uuid        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN (
                            'onboarding', 'sku_add', 'blacklist', 'payment_release', 'bank_change')),
    vendor_id           uuid,
    snapshot            jsonb       NOT NULL,
    approval_request_id text        NOT NULL,
    prepared_by         text        NOT NULL,
    status              text        NOT NULL CHECK (status IN ('pending', 'registered')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.purchase_vendors (
    id                   uuid        PRIMARY KEY,
    company_id           uuid        NOT NULL,
    name                 text        NOT NULL,
    status               text        NOT NULL CHECK (status = 'approved'),
    blacklisted          boolean     NOT NULL DEFAULT false,
    bank_account         text        NOT NULL DEFAULT '',
    bank_request_id      text,
    trade_licence_key    text        NOT NULL DEFAULT '',
    prepared_by          text        NOT NULL,
    approval_request_id  text        NOT NULL,
    blacklist_request_id text,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, approval_request_id)
);

CREATE TABLE erp.purchase_vendor_skus (
    company_id          uuid NOT NULL,
    vendor_id           uuid NOT NULL REFERENCES erp.purchase_vendors (id),
    sku_id              uuid NOT NULL,
    approval_request_id text NOT NULL,
    PRIMARY KEY (company_id, vendor_id, sku_id)
);

CREATE TABLE erp.purchase_docs (
    id                uuid        PRIMARY KEY,
    company_id        uuid        NOT NULL,
    doc_type          text        NOT NULL CHECK (doc_type IN (
                          'requisition', 'quote', 'lpo', 'supplier_invoice',
                          'goods_receipt', 'payment', 'price_agreement')),
    number            text        NOT NULL,
    vendor_id         uuid        NOT NULL REFERENCES erp.purchase_vendors (id),
    status            text        NOT NULL,
    currency          text        NOT NULL DEFAULT 'AED',
    fx_rate           numeric,
    amount            numeric     NOT NULL,
    cost_centre       text        NOT NULL DEFAULT '',
    source_id         uuid,
    supplier_number   text        NOT NULL DEFAULT '',
    effective_from    date,
    effective_to      date,
    bank_account      text        NOT NULL DEFAULT '',
    prepared_by       text        NOT NULL,
    confirmed_duplicate boolean   NOT NULL DEFAULT false,
    ledger_posted     boolean     NOT NULL DEFAULT false,
    posting_gap       text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    posted_at         timestamptz
);
CREATE INDEX purchase_docs_vendor_idx ON erp.purchase_docs (company_id, vendor_id, doc_type, created_at);
CREATE UNIQUE INDEX purchase_docs_number_idx ON erp.purchase_docs (company_id, doc_type, number);

CREATE TABLE erp.purchase_lines (
    company_id uuid    NOT NULL,
    doc_id     uuid    NOT NULL REFERENCES erp.purchase_docs (id),
    line_no    int     NOT NULL,
    sku_id     uuid    NOT NULL,
    qty        numeric NOT NULL,
    unit_price numeric NOT NULL,
    PRIMARY KEY (doc_id, line_no)
);

CREATE TABLE erp.purchase_invoice_flags (
    id         uuid        PRIMARY KEY,
    company_id uuid        NOT NULL,
    invoice_id uuid        NOT NULL REFERENCES erp.purchase_docs (id),
    vendor_id  uuid        NOT NULL,
    reason     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (invoice_id, reason)
);

CREATE TABLE erp.purchase_payment_releases (
    payment_id  uuid        PRIMARY KEY REFERENCES erp.purchase_docs (id),
    company_id  uuid        NOT NULL,
    vendor_id   uuid        NOT NULL,
    released_by text        NOT NULL,
    request_id  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.purchase_counters (
    company_id  uuid   NOT NULL,
    doc_type    text   NOT NULL,
    next_number bigint NOT NULL,
    PRIMARY KEY (company_id, doc_type)
);

CREATE TABLE erp.purchase_settings (
    company_id            uuid PRIMARY KEY,
    match_tolerance_pct   numeric NOT NULL DEFAULT 0,
    duplicate_window_days int     NOT NULL DEFAULT 7
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.purchase_request_posted(p_company uuid, p_request text, p_doc_types text[])
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT p_request IS NOT NULL AND EXISTS (
        SELECT 1
        FROM erp.approval_token_uses u
        JOIN erp.approval_requests r
          ON r.company_id = u.company_id
         AND r.request_id = u.request_id
         AND r.doc_type = ANY (p_doc_types)
        WHERE u.company_id = p_company
          AND u.request_id = p_request
    );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.purchase_final_gate_used(p_company uuid, p_request text, p_doc_types text[])
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT erp.purchase_request_posted(p_company, p_request, p_doc_types)
       AND EXISTS (
            SELECT 1
            FROM erp.approval_decisions d
            JOIN erp.purchase_titles t
              ON t.company_id = p_company
             AND t.user_id = d.actor_id
             AND t.title IN ('cfo', 'partner')
            WHERE d.company_id = p_company
              AND d.request_id = p_request
              AND d.decision = 'approved'
       )
       AND NOT EXISTS (
            SELECT 1
            FROM erp.approval_decisions x
            WHERE x.company_id = p_company
              AND x.request_id = p_request
              AND x.decision = 'delegated'
       );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.purchase_guard_vendor() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NOT erp.purchase_final_gate_used(NEW.company_id, NEW.approval_request_id, ARRAY['vendor_onboarding']) THEN
            RAISE EXCEPTION 'vendor insert skipped the approval request'
                USING ERRCODE = '42501';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.blacklisted IS DISTINCT FROM OLD.blacklisted THEN
        IF NOT NEW.blacklisted THEN
            RAISE EXCEPTION 'blacklist cannot be cleared by a payment release'
                USING ERRCODE = '42501';
        END IF;
        IF NOT erp.purchase_final_gate_used(NEW.company_id, NEW.blacklist_request_id, ARRAY['vendor_blacklist']) THEN
            RAISE EXCEPTION 'blacklist skipped the approval request'
                USING ERRCODE = '42501';
        END IF;
    END IF;
    IF NEW.bank_account IS DISTINCT FROM OLD.bank_account THEN
        IF NOT erp.purchase_request_posted(NEW.company_id, NEW.bank_request_id, ARRAY['vendor_bank_change']) THEN
            RAISE EXCEPTION 'bank change skipped the approval request'
                USING ERRCODE = '42501';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER purchase_guard_vendor
    BEFORE INSERT OR UPDATE ON erp.purchase_vendors
    FOR EACH ROW EXECUTE FUNCTION erp.purchase_guard_vendor();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.purchase_guard_vendor_sku() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT erp.purchase_final_gate_used(NEW.company_id, NEW.approval_request_id, ARRAY['vendor_onboarding', 'vendor_sku']) THEN
        RAISE EXCEPTION 'approved sku skipped the approval request'
            USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER purchase_guard_vendor_sku
    BEFORE INSERT ON erp.purchase_vendor_skus
    FOR EACH ROW EXECUTE FUNCTION erp.purchase_guard_vendor_sku();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.purchase_guard_posted_doc() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('posted', 'closed') AND (
        NEW.status IS DISTINCT FROM OLD.status
        OR NEW.number IS DISTINCT FROM OLD.number
        OR NEW.amount IS DISTINCT FROM OLD.amount
        OR NEW.vendor_id IS DISTINCT FROM OLD.vendor_id
    ) THEN
        RAISE EXCEPTION 'posted purchase document cannot be rewritten'
            USING ERRCODE = 'P0001';
    END IF;
    IF OLD.number IS DISTINCT FROM NEW.number THEN
        RAISE EXCEPTION 'purchase document number cannot be rewritten'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER purchase_guard_posted_doc
    BEFORE UPDATE ON erp.purchase_docs
    FOR EACH ROW EXECUTE FUNCTION erp.purchase_guard_posted_doc();

GRANT SELECT, INSERT, UPDATE ON
    erp.purchase_titles, erp.purchase_skus, erp.purchase_requests, erp.purchase_vendors,
    erp.purchase_vendor_skus, erp.purchase_docs, erp.purchase_lines, erp.purchase_payment_releases,
    erp.purchase_counters, erp.purchase_settings
TO erp_app;
GRANT SELECT, INSERT ON erp.purchase_invoice_flags TO erp_app;
REVOKE UPDATE, DELETE, TRUNCATE ON erp.purchase_invoice_flags FROM erp_app;

ALTER TABLE erp.purchase_titles ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_skus ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_vendors ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_vendor_skus ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_docs ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_invoice_flags ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_payment_releases ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.purchase_settings ENABLE ROW LEVEL SECURITY;

CREATE POLICY purchase_titles_tenant ON erp.purchase_titles
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_skus_tenant ON erp.purchase_skus
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_requests_tenant ON erp.purchase_requests
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_vendors_tenant ON erp.purchase_vendors
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_vendor_skus_tenant ON erp.purchase_vendor_skus
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_docs_tenant ON erp.purchase_docs
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_lines_tenant ON erp.purchase_lines
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_invoice_flags_tenant ON erp.purchase_invoice_flags
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_payment_releases_tenant ON erp.purchase_payment_releases
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_counters_tenant ON erp.purchase_counters
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY purchase_settings_tenant ON erp.purchase_settings
    FOR ALL TO erp_app USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());

-- +goose Down
DROP TRIGGER IF EXISTS purchase_guard_posted_doc ON erp.purchase_docs;
DROP TRIGGER IF EXISTS purchase_guard_vendor_sku ON erp.purchase_vendor_skus;
DROP TRIGGER IF EXISTS purchase_guard_vendor ON erp.purchase_vendors;
DROP FUNCTION IF EXISTS erp.purchase_guard_posted_doc();
DROP FUNCTION IF EXISTS erp.purchase_guard_vendor_sku();
DROP FUNCTION IF EXISTS erp.purchase_guard_vendor();
DROP FUNCTION IF EXISTS erp.purchase_final_gate_used(uuid, text, text[]);
DROP FUNCTION IF EXISTS erp.purchase_request_posted(uuid, text, text[]);
DROP TABLE IF EXISTS erp.purchase_settings;
DROP TABLE IF EXISTS erp.purchase_counters;
DROP TABLE IF EXISTS erp.purchase_payment_releases;
DROP TABLE IF EXISTS erp.purchase_invoice_flags;
DROP TABLE IF EXISTS erp.purchase_lines;
DROP TABLE IF EXISTS erp.purchase_docs;
DROP TABLE IF EXISTS erp.purchase_vendor_skus;
DROP TABLE IF EXISTS erp.purchase_vendors;
DROP TABLE IF EXISTS erp.purchase_requests;
DROP TABLE IF EXISTS erp.purchase_skus;
DROP TABLE IF EXISTS erp.purchase_titles;
