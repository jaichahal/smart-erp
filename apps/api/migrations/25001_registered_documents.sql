-- +goose Up
-- Version 25001. A registered document row exists only inside a successful
-- numbering registration (C1, R4.5). The row is mutable.
-- migrations: mutable-only

CREATE TABLE erp.registered_documents (
    company_id uuid   NOT NULL REFERENCES erp.companies(id),
    doc_type   text   NOT NULL,
    doc_id     text   NOT NULL,
    number     bigint NOT NULL CHECK (number >= 1),
    PRIMARY KEY (company_id, doc_type, doc_id)
);

GRANT SELECT, INSERT, UPDATE ON erp.registered_documents TO erp_app;

ALTER TABLE erp.registered_documents ENABLE ROW LEVEL SECURITY;

CREATE POLICY registered_documents_tenant ON erp.registered_documents FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose Down
DROP TABLE IF EXISTS erp.registered_documents;
