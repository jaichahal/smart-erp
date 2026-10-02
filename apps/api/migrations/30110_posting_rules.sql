-- +goose Up
-- Versioned posting rules. An approved version's accounts do not change.
-- A later approved version is a new row. Documents keep the version they posted with.

CREATE TABLE erp.posting_rule_versions (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id           uuid        NOT NULL REFERENCES erp.companies(id),
    doc_type             text        NOT NULL,
    tax_code             text        NOT NULL DEFAULT '',
    item_class           text        NOT NULL DEFAULT '',
    dimension_value_id   uuid,
    party_group          text        NOT NULL DEFAULT '',
    debit_account_id     uuid        NOT NULL REFERENCES erp.accounts(id),
    credit_account_id    uuid        NOT NULL REFERENCES erp.accounts(id),
    tax_account_id       uuid        REFERENCES erp.accounts(id),
    discount_account_id  uuid        REFERENCES erp.accounts(id),
    rounding_account_id  uuid        REFERENCES erp.accounts(id),
    priority             int         NOT NULL DEFAULT 0,
    status               text        NOT NULL CHECK (status IN ('pending_approval', 'approved')),
    requested_by         text        NOT NULL,
    approved_by          text,
    approved_at          timestamptz,
    change_reason        text        NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (status <> 'approved' OR (approved_by IS NOT NULL AND approved_by <> requested_by AND approved_at IS NOT NULL))
);
CREATE INDEX posting_rule_versions_match_idx
    ON erp.posting_rule_versions (company_id, doc_type, status, created_at DESC);

ALTER TABLE erp.journals
    ADD CONSTRAINT journals_rule_version_fk
    FOREIGN KEY (rule_version_id) REFERENCES erp.posting_rule_versions(id);

GRANT SELECT, INSERT, UPDATE ON erp.posting_rule_versions TO erp_app;
ALTER TABLE erp.posting_rule_versions ENABLE ROW LEVEL SECURITY;
CREATE POLICY posting_rule_versions_tenant ON erp.posting_rule_versions FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.posting_rule_version_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'approved' THEN
        RAISE EXCEPTION 'immutable: posting rule version % is approved', OLD.id
            USING ERRCODE = 'P0001';
    END IF;
    IF NEW.debit_account_id IS DISTINCT FROM OLD.debit_account_id
        OR NEW.credit_account_id IS DISTINCT FROM OLD.credit_account_id
        OR NEW.tax_account_id IS DISTINCT FROM OLD.tax_account_id
        OR NEW.discount_account_id IS DISTINCT FROM OLD.discount_account_id
        OR NEW.rounding_account_id IS DISTINCT FROM OLD.rounding_account_id
        OR NEW.doc_type IS DISTINCT FROM OLD.doc_type
        OR NEW.tax_code IS DISTINCT FROM OLD.tax_code
        OR NEW.item_class IS DISTINCT FROM OLD.item_class
        OR NEW.dimension_value_id IS DISTINCT FROM OLD.dimension_value_id
        OR NEW.party_group IS DISTINCT FROM OLD.party_group
        OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
        OR NEW.company_id IS DISTINCT FROM OLD.company_id
        OR NEW.change_reason IS DISTINCT FROM OLD.change_reason
        OR NEW.priority IS DISTINCT FROM OLD.priority
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'immutable: posting rule version content cannot change'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER posting_rule_version_guard
    BEFORE UPDATE ON erp.posting_rule_versions
    FOR EACH ROW
    EXECUTE FUNCTION erp.posting_rule_version_guard();

-- +goose Down
DROP TRIGGER IF EXISTS posting_rule_version_guard ON erp.posting_rule_versions;
DROP FUNCTION IF EXISTS erp.posting_rule_version_guard();
ALTER TABLE erp.journals DROP CONSTRAINT IF EXISTS journals_rule_version_fk;
DROP TABLE IF EXISTS erp.posting_rule_versions;
