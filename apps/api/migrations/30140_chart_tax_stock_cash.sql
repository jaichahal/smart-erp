-- +goose Up
-- UAE chart, tax codes, dimensions, tax-invoice snapshots, and the stock and cash guards.
-- Account codes live in this migration. Go resolves roles, not codes.

ALTER TABLE erp.accounts ADD COLUMN posting_role text;
CREATE UNIQUE INDEX accounts_posting_role_uidx
    ON erp.accounts (company_id, posting_role) WHERE posting_role IS NOT NULL;

CREATE TABLE erp.account_versions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid        NOT NULL REFERENCES erp.companies(id),
    account_id    uuid        NOT NULL REFERENCES erp.accounts(id),
    name          text        NOT NULL,
    status        text        NOT NULL CHECK (status IN ('pending_approval', 'approved')),
    requested_by  text        NOT NULL,
    approved_by   text,
    approved_at   timestamptz,
    change_reason text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (status <> 'approved' OR (approved_by IS NOT NULL AND approved_by <> requested_by AND approved_at IS NOT NULL))
);

CREATE TABLE erp.tax_codes (
    id          uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  uuid          NOT NULL REFERENCES erp.companies(id),
    code        text          NOT NULL,
    name        text          NOT NULL,
    rate        numeric(10,4) NOT NULL CHECK (rate >= 0 AND rate <= 1),
    UNIQUE (company_id, code)
);

CREATE TABLE erp.dimension_definitions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  uuid NOT NULL REFERENCES erp.companies(id),
    code        text NOT NULL,
    name        text NOT NULL,
    UNIQUE (company_id, code)
);

CREATE TABLE erp.dimension_values (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      uuid NOT NULL REFERENCES erp.companies(id),
    definition_id   uuid NOT NULL REFERENCES erp.dimension_definitions(id),
    code            text NOT NULL,
    name            text NOT NULL,
    UNIQUE (definition_id, code)
);

CREATE TABLE erp.tax_invoices (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id          uuid          NOT NULL REFERENCES erp.companies(id),
    journal_id          uuid          NOT NULL UNIQUE REFERENCES erp.journals(id),
    document_discount   numeric(20,2) NOT NULL,
    tax                 numeric(20,2) NOT NULL,
    rounding            numeric(20,2) NOT NULL,
    total               numeric(20,2) NOT NULL
);

CREATE TABLE erp.tax_invoice_lines (
    id            uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid          NOT NULL REFERENCES erp.companies(id),
    invoice_id    uuid          NOT NULL REFERENCES erp.tax_invoices(id),
    line_no       int           NOT NULL CHECK (line_no > 0),
    qty           numeric(20,4) NOT NULL,
    unit_price    numeric(20,4) NOT NULL,
    line_discount numeric(20,2) NOT NULL,
    tax           numeric(20,2) NOT NULL,
    line_total    numeric(20,2) NOT NULL,
    UNIQUE (invoice_id, line_no)
);

CREATE TABLE erp.stock_qty_movements (
    id             uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id     uuid          NOT NULL REFERENCES erp.companies(id),
    sku_id         uuid          NOT NULL,
    warehouse_id   uuid          NOT NULL,
    qty_delta      numeric(20,4) NOT NULL,
    source_doc_id  text          NOT NULL,
    posted_at      timestamptz   NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX stock_qty_movements_sku_idx ON erp.stock_qty_movements (company_id, sku_id, warehouse_id);

GRANT SELECT, INSERT, UPDATE ON
    erp.account_versions, erp.tax_codes, erp.dimension_definitions, erp.dimension_values
TO erp_app;
GRANT SELECT, INSERT ON erp.tax_invoices, erp.tax_invoice_lines, erp.stock_qty_movements TO erp_app;

ALTER TABLE erp.account_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.tax_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.dimension_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.dimension_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.tax_invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.tax_invoice_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.stock_qty_movements ENABLE ROW LEVEL SECURITY;

CREATE POLICY account_versions_tenant ON erp.account_versions FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY tax_codes_tenant ON erp.tax_codes FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY dimension_definitions_tenant ON erp.dimension_definitions FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY dimension_values_tenant ON erp.dimension_values FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY tax_invoices_tenant ON erp.tax_invoices FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY tax_invoice_lines_tenant ON erp.tax_invoice_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY stock_qty_movements_tenant ON erp.stock_qty_movements FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

SELECT erp.make_immutable('erp.tax_invoices');
SELECT erp.make_immutable('erp.tax_invoice_lines');
SELECT erp.make_immutable('erp.stock_qty_movements');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.account_version_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'approved' THEN
        RAISE EXCEPTION 'immutable: account version % is approved', OLD.id USING ERRCODE = 'P0001';
    END IF;
    IF NEW.name IS DISTINCT FROM OLD.name
        OR NEW.account_id IS DISTINCT FROM OLD.account_id
        OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
        OR NEW.company_id IS DISTINCT FROM OLD.company_id
        OR NEW.change_reason IS DISTINCT FROM OLD.change_reason
    THEN
        RAISE EXCEPTION 'immutable: account version content cannot change' USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER account_version_guard
    BEFORE UPDATE ON erp.account_versions
    FOR EACH ROW EXECUTE FUNCTION erp.account_version_guard();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.stock_non_negative() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    on_hand numeric;
BEGIN
    SELECT coalesce(sum(qty_delta), 0) INTO on_hand
    FROM erp.stock_qty_movements
    WHERE company_id = NEW.company_id AND sku_id = NEW.sku_id AND warehouse_id = NEW.warehouse_id;
    IF on_hand < 0 THEN
        RAISE EXCEPTION 'NEGATIVE_STOCK' USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER stock_non_negative
    AFTER INSERT ON erp.stock_qty_movements
    FOR EACH ROW EXECUTE FUNCTION erp.stock_non_negative();

-- A cash balance is judged per currency after the whole journal is visible.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.cash_non_negative() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_account uuid;
    bad int;
BEGIN
    v_account := COALESCE(NEW.account_id, OLD.account_id);
    IF NOT EXISTS (SELECT 1 FROM erp.accounts WHERE id = v_account AND control_type = 'cash') THEN
        RETURN NULL;
    END IF;
    SELECT count(*) INTO bad FROM (
        SELECT currency
        FROM erp.journal_lines
        WHERE account_id = v_account
        GROUP BY currency
        HAVING sum(debit - credit) < 0
    ) negative_cash;
    IF bad > 0 THEN
        RAISE EXCEPTION 'cash account cannot be negative' USING ERRCODE = 'P0001';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER cash_non_negative
    AFTER INSERT OR UPDATE OR DELETE ON erp.journal_lines
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION erp.cash_non_negative();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.seed_uae_trading_chart(p_company uuid)
RETURNS TABLE (posting_role text, account_id uuid)
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO erp.accounts (company_id, code, name, account_type, control_type, posting_role) VALUES
        (p_company, '1000', 'Cash on hand', 'asset', 'cash', 'cash'),
        (p_company, '1010', 'Bank', 'asset', 'bank', 'bank'),
        (p_company, '1100', 'Accounts receivable', 'asset', 'receivable', 'receivable'),
        (p_company, '1200', 'Inventory', 'asset', 'stock', 'stock'),
        (p_company, '1300', 'Prepayments', 'asset', NULL, 'prepayment'),
        (p_company, '1500', 'VAT input', 'asset', 'vat_input', 'vat_input'),
        (p_company, '2000', 'Accounts payable', 'liability', 'payable', 'payable'),
        (p_company, '2100', 'Accrued liabilities', 'liability', NULL, 'accrual'),
        (p_company, '2200', 'VAT output', 'liability', 'vat_output', 'vat_output'),
        (p_company, '3000', 'Retained earnings', 'equity', 'retained_earnings', 'retained_earnings'),
        (p_company, '4000', 'Sales revenue', 'income', NULL, 'revenue'),
        (p_company, '5100', 'Discount allowed', 'expense', 'discount', 'discount'),
        (p_company, '5200', 'Rounding', 'expense', 'rounding', 'rounding'),
        (p_company, '5500', 'General expense', 'expense', NULL, 'expense')
    ON CONFLICT (company_id, code) DO UPDATE SET posting_role = EXCLUDED.posting_role;

    INSERT INTO erp.tax_codes (company_id, code, name, rate) VALUES
        (p_company, 'standard', 'Standard rated', 0.05),
        (p_company, 'zero_rated', 'Zero rated', 0),
        (p_company, 'exempt', 'Exempt', 0),
        (p_company, 'reverse_charge', 'Reverse charge', 0.05),
        (p_company, 'out_of_scope', 'Out of scope', 0)
    ON CONFLICT (company_id, code) DO NOTHING;

    INSERT INTO erp.dimension_definitions (company_id, code, name)
    VALUES (p_company, 'cost_centre', 'Cost centre')
    ON CONFLICT (company_id, code) DO NOTHING;

    INSERT INTO erp.dimension_values (company_id, definition_id, code, name)
    SELECT p_company, d.id, 'ADMIN', 'Administration'
    FROM erp.dimension_definitions d
    WHERE d.company_id = p_company AND d.code = 'cost_centre'
    ON CONFLICT (definition_id, code) DO NOTHING;

    RETURN QUERY
        SELECT a.posting_role, a.id FROM erp.accounts a
        WHERE a.company_id = p_company AND a.posting_role IS NOT NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS cash_non_negative ON erp.journal_lines;
DROP FUNCTION IF EXISTS erp.cash_non_negative();
DROP TRIGGER IF EXISTS stock_non_negative ON erp.stock_qty_movements;
DROP FUNCTION IF EXISTS erp.stock_non_negative();
DROP FUNCTION IF EXISTS erp.seed_uae_trading_chart(uuid);
DROP TRIGGER IF EXISTS account_version_guard ON erp.account_versions;
DROP FUNCTION IF EXISTS erp.account_version_guard();
DROP TABLE IF EXISTS erp.stock_qty_movements;
DROP TABLE IF EXISTS erp.tax_invoice_lines;
DROP TABLE IF EXISTS erp.tax_invoices;
DROP TABLE IF EXISTS erp.dimension_values;
DROP TABLE IF EXISTS erp.dimension_definitions;
DROP TABLE IF EXISTS erp.tax_codes;
DROP TABLE IF EXISTS erp.account_versions;
ALTER TABLE erp.accounts DROP COLUMN IF EXISTS posting_role;
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.tax_invoices', 'erp.tax_invoice_lines', 'erp.stock_qty_movements');
