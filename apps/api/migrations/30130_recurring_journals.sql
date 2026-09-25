-- +goose Up
-- Recurring journals post from an approved setup. The run log is insert-only,
-- so a schedule date cannot post twice.

CREATE TABLE erp.recurring_journal_setups (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id       uuid        NOT NULL REFERENCES erp.companies(id),
    description      text        NOT NULL,
    start_date       date        NOT NULL,
    end_date         date        NOT NULL,
    interval_months  int         NOT NULL CHECK (interval_months >= 1),
    next_run_on      date        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('pending_approval', 'approved', 'stopped')),
    requested_by     text        NOT NULL,
    approved_by      text,
    approved_at      timestamptz,
    state_version    bigint      NOT NULL DEFAULT 1,
    CHECK (end_date >= start_date),
    CHECK (status = 'pending_approval' OR (approved_by IS NOT NULL AND approved_by <> requested_by AND approved_at IS NOT NULL))
);

CREATE TABLE erp.recurring_journal_lines (
    id          uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    setup_id    uuid          NOT NULL REFERENCES erp.recurring_journal_setups(id),
    company_id  uuid          NOT NULL REFERENCES erp.companies(id),
    line_no     int           NOT NULL CHECK (line_no > 0),
    account_id  uuid          NOT NULL REFERENCES erp.accounts(id),
    debit       numeric(20,2) NOT NULL CHECK (debit >= 0),
    credit      numeric(20,2) NOT NULL CHECK (credit >= 0),
    currency    text          NOT NULL,
    CHECK (NOT (debit > 0 AND credit > 0)),
    CHECK (debit > 0 OR credit > 0),
    UNIQUE (setup_id, line_no)
);

CREATE TABLE erp.recurring_journal_runs (
    setup_id    uuid        NOT NULL REFERENCES erp.recurring_journal_setups(id),
    company_id  uuid        NOT NULL REFERENCES erp.companies(id),
    run_on      date        NOT NULL,
    journal_id  uuid        NOT NULL REFERENCES erp.journals(id),
    PRIMARY KEY (setup_id, run_on)
);

GRANT SELECT, INSERT, UPDATE ON erp.recurring_journal_setups TO erp_app;
GRANT SELECT, INSERT ON erp.recurring_journal_lines, erp.recurring_journal_runs TO erp_app;

ALTER TABLE erp.recurring_journal_setups ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.recurring_journal_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.recurring_journal_runs ENABLE ROW LEVEL SECURITY;

CREATE POLICY recurring_setups_tenant ON erp.recurring_journal_setups FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY recurring_lines_tenant ON erp.recurring_journal_lines FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));
CREATE POLICY recurring_runs_tenant ON erp.recurring_journal_runs FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

SELECT erp.make_immutable('erp.recurring_journal_lines');
SELECT erp.make_immutable('erp.recurring_journal_runs');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.recurring_setup_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.start_date IS DISTINCT FROM OLD.start_date
        OR NEW.end_date IS DISTINCT FROM OLD.end_date
        OR NEW.interval_months IS DISTINCT FROM OLD.interval_months
        OR NEW.description IS DISTINCT FROM OLD.description
        OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
        OR NEW.company_id IS DISTINCT FROM OLD.company_id
    THEN
        RAISE EXCEPTION 'immutable: recurring setup terms cannot change'
            USING ERRCODE = 'P0001';
    END IF;
    IF OLD.status = 'stopped' THEN
        RAISE EXCEPTION 'immutable: recurring setup is stopped'
            USING ERRCODE = 'P0001';
    END IF;
    IF OLD.status <> 'pending_approval' AND (
        NEW.approved_by IS DISTINCT FROM OLD.approved_by
        OR NEW.approved_at IS DISTINCT FROM OLD.approved_at
    ) THEN
        RAISE EXCEPTION 'immutable: recurring approval is already stored'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER recurring_setup_guard
    BEFORE UPDATE ON erp.recurring_journal_setups
    FOR EACH ROW
    EXECUTE FUNCTION erp.recurring_setup_guard();

-- +goose Down
DROP TRIGGER IF EXISTS recurring_setup_guard ON erp.recurring_journal_setups;
DROP FUNCTION IF EXISTS erp.recurring_setup_guard();
DROP TABLE IF EXISTS erp.recurring_journal_runs;
DROP TABLE IF EXISTS erp.recurring_journal_lines;
DROP TABLE IF EXISTS erp.recurring_journal_setups;
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.recurring_journal_lines', 'erp.recurring_journal_runs');
