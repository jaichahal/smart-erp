-- +goose Up
-- Version 10041 follows 10040. Allocations and voids are immutable (immutable-change).
-- Gap-free document numbers (R4.5). Each allocation and each void is an append-only
-- row. The mutable counter they draw from is erp.number_sequences. The sequence never
-- rewinds: a failed registration keeps the allocation and adds a void.

CREATE TABLE erp.number_allocations (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    doc_type     text        NOT NULL,
    fiscal_year  int         NOT NULL,
    number       bigint      NOT NULL CHECK (number >= 1),
    doc_id       text        NOT NULL,
    allocated_at timestamptz NOT NULL,
    canonical    text        NOT NULL,
    chain_seq    bigint      NOT NULL,
    prev_hash    text        NOT NULL,
    hash         text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq),
    UNIQUE (company_id, doc_type, fiscal_year, number)
);
CREATE INDEX number_allocations_doc_idx ON erp.number_allocations (company_id, doc_type, fiscal_year);
SELECT erp.make_immutable('erp.number_allocations');

CREATE TABLE erp.number_voids (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  uuid        NOT NULL,
    doc_type    text        NOT NULL,
    fiscal_year int         NOT NULL,
    number      bigint      NOT NULL CHECK (number >= 1),
    doc_id      text        NOT NULL,
    reason      text        NOT NULL,
    voided_at   timestamptz NOT NULL,
    canonical   text        NOT NULL,
    chain_seq   bigint      NOT NULL,
    prev_hash   text        NOT NULL,
    hash        text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq),
    UNIQUE (company_id, doc_type, fiscal_year, number)
);
CREATE INDEX number_voids_doc_idx ON erp.number_voids (company_id, doc_type, fiscal_year);
SELECT erp.make_immutable('erp.number_voids');

-- +goose Down
DROP TABLE IF EXISTS erp.number_voids;
DROP TABLE IF EXISTS erp.number_allocations;
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.number_allocations', 'erp.number_voids');
