-- +goose Up
-- Chart, posted journals, and lines. Posted journals and lines are insert-only.
-- Balance is enforced in the following statements of this migration.

CREATE TABLE erp.accounts (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid        NOT NULL REFERENCES erp.companies(id),
    code          text        NOT NULL,
    name          text        NOT NULL,
    account_type  text        NOT NULL CHECK (account_type IN ('asset', 'liability', 'equity', 'income', 'expense')),
    account_group text        NOT NULL DEFAULT '',
    is_postable   boolean     NOT NULL DEFAULT true,
    currency      text        NOT NULL DEFAULT 'AED' CHECK (currency = 'AED'),
    control_type  text        CHECK (control_type IS NULL OR control_type IN (
        'receivable', 'payable', 'bank', 'cash', 'pdc_in', 'pdc_out', 'stock', 'grni',
        'vat_output', 'vat_input', 'retained_earnings', 'rounding', 'discount', 'variance')),
    state_version bigint      NOT NULL DEFAULT 1,
    status        text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending_approval', 'disabled')),
    UNIQUE (company_id, code)
);

CREATE TABLE erp.journals (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      uuid        NOT NULL REFERENCES erp.companies(id),
    doc_type        text        NOT NULL,
    doc_id          text        NOT NULL,
    period_id       uuid        NOT NULL REFERENCES erp.periods(id),
    posting_date    date        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    kind            text        NOT NULL CHECK (kind IN ('manual', 'system', 'accrual', 'prepayment', 'recurring', 'invoice', 'reversal')),
    reason          text        NOT NULL DEFAULT '',
    attachment_key  text        NOT NULL DEFAULT '',
    rule_version_id uuid,
    created_by      text        NOT NULL,
    reverses_journal_id uuid,
    CHECK (kind <> 'manual' OR (btrim(reason) <> '' AND btrim(attachment_key) <> ''))
);

CREATE TABLE erp.journal_lines (
    id          uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    journal_id  uuid          NOT NULL REFERENCES erp.journals(id),
    company_id  uuid          NOT NULL REFERENCES erp.companies(id),
    line_no     int           NOT NULL CHECK (line_no > 0),
    account_id  uuid          NOT NULL REFERENCES erp.accounts(id),
    debit       numeric(20,2) NOT NULL CHECK (debit >= 0),
    credit      numeric(20,2) NOT NULL CHECK (credit >= 0),
    currency    text          NOT NULL,
    fx_rate     numeric(20,8) NOT NULL DEFAULT 1,
    party_id    uuid,
    dimension_value_id uuid,
    CHECK (NOT (debit > 0 AND credit > 0)),
    CHECK (debit > 0 OR credit > 0),
    UNIQUE (journal_id, line_no)
);

GRANT SELECT, INSERT, UPDATE ON erp.accounts TO erp_app;
GRANT SELECT, INSERT ON erp.journals, erp.journal_lines TO erp_app;

ALTER TABLE erp.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.journals ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.journal_lines ENABLE ROW LEVEL SECURITY;

CREATE POLICY accounts_tenant ON erp.accounts FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY journals_tenant ON erp.journals FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY journal_lines_tenant ON erp.journal_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- Deferred so a journal is judged once, after every line of that currency is visible.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.journal_balance_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_journal uuid;
    bad int;
BEGIN
    v_journal := COALESCE(NEW.journal_id, OLD.journal_id);
    SELECT count(*) INTO bad FROM (
        SELECT currency
        FROM erp.journal_lines
        WHERE journal_id = v_journal
        GROUP BY currency
        HAVING sum(debit) <> sum(credit)
    ) unbalanced;
    IF bad > 0 THEN
        RAISE EXCEPTION 'journal debits must equal credits per currency'
            USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER journal_lines_balanced
    AFTER INSERT OR UPDATE OR DELETE ON erp.journal_lines
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION erp.journal_balance_guard();

SELECT erp.make_immutable('erp.journals');
SELECT erp.make_immutable('erp.journal_lines');

-- +goose Down
DROP TABLE IF EXISTS erp.journal_lines;
DROP TABLE IF EXISTS erp.journals;
DROP FUNCTION IF EXISTS erp.journal_balance_guard();
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.journals', 'erp.journal_lines');
DROP TABLE IF EXISTS erp.accounts;
