-- +goose Up
-- Accruals and prepayments schedule one reversal on the first day of the next period.
-- The source journal is unchanged. The reversal is a new journal.

CREATE TABLE erp.scheduled_reversals (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id           uuid        NOT NULL REFERENCES erp.companies(id),
    kind                 text        NOT NULL CHECK (kind IN ('accrual', 'prepayment')),
    source_journal_id    uuid        NOT NULL UNIQUE REFERENCES erp.journals(id),
    reversal_journal_id  uuid        UNIQUE REFERENCES erp.journals(id),
    reverse_on           date        NOT NULL
);

GRANT SELECT, INSERT, UPDATE ON erp.scheduled_reversals TO erp_app;
ALTER TABLE erp.scheduled_reversals ENABLE ROW LEVEL SECURITY;
CREATE POLICY scheduled_reversals_tenant ON erp.scheduled_reversals FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.scheduled_reversal_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.reversal_journal_id IS NOT NULL THEN
        RAISE EXCEPTION 'immutable: reversal % is already posted', OLD.id
            USING ERRCODE = 'P0001';
    END IF;
    IF NEW.company_id IS DISTINCT FROM OLD.company_id
        OR NEW.kind IS DISTINCT FROM OLD.kind
        OR NEW.source_journal_id IS DISTINCT FROM OLD.source_journal_id
        OR NEW.reverse_on IS DISTINCT FROM OLD.reverse_on
    THEN
        RAISE EXCEPTION 'immutable: scheduled reversal content cannot change'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER scheduled_reversal_guard
    BEFORE UPDATE ON erp.scheduled_reversals
    FOR EACH ROW
    EXECUTE FUNCTION erp.scheduled_reversal_guard();

-- +goose Down
DROP TRIGGER IF EXISTS scheduled_reversal_guard ON erp.scheduled_reversals;
DROP FUNCTION IF EXISTS erp.scheduled_reversal_guard();
DROP TABLE IF EXISTS erp.scheduled_reversals;
