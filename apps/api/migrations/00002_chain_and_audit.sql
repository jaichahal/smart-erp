-- +goose Up
-- Hash chain (05 "Hash chain"). One chain per company. The head is an append-only
-- log; the current head is the row with the highest chain_seq. Appends are
-- serialised with a transaction-scoped advisory lock so the chain cannot fork (B5).

CREATE TABLE erp.chain_head_log (
    company_id  uuid        NOT NULL,
    chain_seq   bigint      NOT NULL,
    head_hash   text        NOT NULL CHECK (length(head_hash) = 64),
    appended_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, chain_seq)
);
SELECT erp.make_immutable('erp.chain_head_log');

CREATE VIEW erp.chain_head AS
    SELECT DISTINCT ON (company_id) company_id, chain_seq, head_hash, appended_at
    FROM erp.chain_head_log
    ORDER BY company_id, chain_seq DESC;
GRANT SELECT ON erp.chain_head TO erp_app;

-- Append one row's canonical payload to the company chain.
-- hash = sha256(canonical || prev_hash), hex. Returns the allocated sequence and both hashes.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.chain_append(p_company uuid, p_canonical text)
RETURNS TABLE (chain_seq bigint, prev_hash text, hash text)
LANGUAGE plpgsql AS $$
DECLARE
    v_seq  bigint;
    v_prev text;
    v_hash text;
BEGIN
    PERFORM pg_advisory_xact_lock(7231, hashtext(p_company::text));
    SELECT h.chain_seq, h.head_hash INTO v_seq, v_prev
        FROM erp.chain_head h WHERE h.company_id = p_company;
    IF NOT FOUND THEN
        v_seq := 0;
        v_prev := '';
    END IF;
    v_hash := encode(sha256(convert_to(p_canonical || v_prev, 'UTF8')), 'hex');
    INSERT INTO erp.chain_head_log(company_id, chain_seq, head_hash) VALUES (p_company, v_seq + 1, v_hash);
    RETURN QUERY SELECT v_seq + 1, v_prev, v_hash;
END;
$$;
-- +goose StatementEnd

-- Audit events (R2.7, R3.12). Canonical payload is built by kit/audit from the row
-- fields excluding chain_seq, prev_hash, hash; occurred_at is read from the database
-- clock in the same transaction, never supplied by the client (B8).
CREATE TABLE erp.audit_events (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      uuid        NOT NULL,
    chain_seq       bigint      NOT NULL,
    event_type      text        NOT NULL,
    actor_id        text        NOT NULL,
    occurred_at     timestamptz NOT NULL,
    reference_type  text        NOT NULL DEFAULT '',
    reference_id    text        NOT NULL DEFAULT '',
    before_state    jsonb,
    after_state     jsonb,
    reason          text        NOT NULL DEFAULT '',
    canonical       text        NOT NULL,
    prev_hash       text        NOT NULL,
    hash            text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq)
);
CREATE INDEX audit_events_ref_idx ON erp.audit_events (company_id, reference_type, reference_id);
CREATE INDEX audit_events_occurred_idx ON erp.audit_events (company_id, occurred_at);
SELECT erp.make_immutable('erp.audit_events');

-- +goose Down
DROP TABLE IF EXISTS erp.audit_events;
DROP FUNCTION IF EXISTS erp.chain_append(uuid, text);
DROP VIEW IF EXISTS erp.chain_head;
DROP TABLE IF EXISTS erp.chain_head_log;
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.chain_head_log', 'erp.audit_events');
