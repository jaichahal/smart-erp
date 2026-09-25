-- +goose Up
-- Approval engine (P1.7, R2.1 to R2.11).
-- Request versions, decisions, posting-token issues and token uses are immutable
-- tables created the same way as kit/immutable.CreateTableSQL: owner columns,
-- chain columns, UNIQUE (company_id, chain_seq), then erp.make_immutable.
-- Matrix, fraud thresholds, the actor directory and approver assignments are
-- mutable configuration (not the request or decision record).
--
-- Transitions cannot SELECT FOR UPDATE: erp_app has no UPDATE on these tables.
-- The service takes pg_advisory_xact_lock(7232, hashtext(request_id)) and inserts
-- the next state_version instead of updating the row.

CREATE TABLE erp.approval_matrix (
    company_id             uuid    NOT NULL,
    doc_type               text    NOT NULL,
    threshold_amount       text    NOT NULL,
    threshold_currency     text    NOT NULL DEFAULT 'AED',
    below_mode             text    NOT NULL DEFAULT 'any_one',
    above_mode             text    NOT NULL DEFAULT 'first_then_final',
    approver_roles_below   text[]  NOT NULL,
    first_approver_roles   text[]  NOT NULL,
    final_gate_role        text    NOT NULL,
    vote_n                 integer NOT NULL DEFAULT 0,
    requires_step_up_above boolean NOT NULL DEFAULT true,
    PRIMARY KEY (company_id, doc_type)
);

CREATE TABLE erp.approval_fraud_config (
    company_id                uuid PRIMARY KEY,
    variance_percent          text    NOT NULL DEFAULT '10',
    round_above               text    NOT NULL DEFAULT '10000',
    round_step                text    NOT NULL DEFAULT '1000',
    repeated_rejection_min    integer NOT NULL DEFAULT 3,
    corrections_per_month_min integer NOT NULL DEFAULT 3
);

CREATE TABLE erp.approval_actors (
    company_id uuid   NOT NULL,
    user_id    text   NOT NULL,
    name       text   NOT NULL,
    department text   NOT NULL DEFAULT '',
    roles      text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (company_id, user_id)
);

CREATE TABLE erp.approval_assignments (
    company_id  uuid NOT NULL,
    doc_type    text NOT NULL,
    slot        text NOT NULL,
    user_id     text NOT NULL,
    assigned_by text NOT NULL,
    PRIMARY KEY (company_id, doc_type, slot, user_id)
);

CREATE TABLE erp.approval_requests (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id            uuid        NOT NULL,
    request_id            text        NOT NULL,
    doc_id                text        NOT NULL,
    doc_type              text        NOT NULL,
    doc_number            text        NOT NULL DEFAULT '',
    party                 text,
    amount                text        NOT NULL,
    currency              text        NOT NULL,
    content_hash          text        NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    snapshot              jsonb       NOT NULL,
    canonical             text        NOT NULL,
    state                 text        NOT NULL,
    state_version         bigint      NOT NULL,
    stage                 text        NOT NULL,
    initiator_id          text        NOT NULL,
    initiator_name        text        NOT NULL,
    initiator_department  text        NOT NULL DEFAULT '',
    votes_needed          integer     NOT NULL DEFAULT 1,
    votes_have            integer     NOT NULL DEFAULT 0,
    delegate_user_id      text        NOT NULL DEFAULT '',
    delegate_until        timestamptz,
    threshold_crossed     boolean     NOT NULL DEFAULT false,
    step_up_required      boolean     NOT NULL DEFAULT false,
    fraud                 jsonb       NOT NULL DEFAULT '{}'::jsonb,
    severity              text        NOT NULL,
    waiting_since         timestamptz NOT NULL,
    chain_seq             bigint      NOT NULL,
    prev_hash             text        NOT NULL,
    hash                  text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq),
    UNIQUE (company_id, request_id, state_version)
);
CREATE INDEX approval_requests_idx1 ON erp.approval_requests (company_id, request_id, state_version DESC);
SELECT erp.make_immutable('erp.approval_requests');

CREATE TABLE erp.approval_decisions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    request_id   text        NOT NULL,
    actor_id     text        NOT NULL,
    actor_name   text        NOT NULL,
    decision     text        NOT NULL,
    reason       text        NOT NULL DEFAULT '',
    hash_seen    text        NOT NULL,
    canonical    text        NOT NULL,
    decided_at   timestamptz NOT NULL,
    chain_seq    bigint      NOT NULL,
    prev_hash    text        NOT NULL,
    hash         text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq)
);
CREATE INDEX approval_decisions_idx1 ON erp.approval_decisions (company_id, request_id, decided_at);
SELECT erp.make_immutable('erp.approval_decisions');

CREATE TABLE erp.approval_posting_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    request_id   text        NOT NULL,
    token_hash   text        NOT NULL,
    content_hash text        NOT NULL,
    expires_at   timestamptz NOT NULL,
    canonical    text        NOT NULL,
    chain_seq    bigint      NOT NULL,
    prev_hash    text        NOT NULL,
    hash         text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq),
    UNIQUE (token_hash)
);
SELECT erp.make_immutable('erp.approval_posting_tokens');

CREATE TABLE erp.approval_token_uses (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    request_id   text        NOT NULL,
    token_hash   text        NOT NULL,
    content_hash text        NOT NULL,
    used_at      timestamptz NOT NULL,
    canonical    text        NOT NULL,
    chain_seq    bigint      NOT NULL,
    prev_hash    text        NOT NULL,
    hash         text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq),
    UNIQUE (token_hash)
);
SELECT erp.make_immutable('erp.approval_token_uses');

ALTER TABLE erp.approval_matrix ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_fraud_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_actors ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_posting_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.approval_token_uses ENABLE ROW LEVEL SECURITY;

CREATE POLICY approval_matrix_tenant ON erp.approval_matrix
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_fraud_config_tenant ON erp.approval_fraud_config
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_actors_tenant ON erp.approval_actors
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_assignments_tenant ON erp.approval_assignments
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_requests_tenant ON erp.approval_requests
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_decisions_tenant ON erp.approval_decisions
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_posting_tokens_tenant ON erp.approval_posting_tokens
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());
CREATE POLICY approval_token_uses_tenant ON erp.approval_token_uses
    USING (company_id = erp.current_company()) WITH CHECK (company_id = erp.current_company());

-- +goose Down
DROP TABLE IF EXISTS erp.approval_token_uses;
DROP TABLE IF EXISTS erp.approval_posting_tokens;
DROP TABLE IF EXISTS erp.approval_decisions;
DROP TABLE IF EXISTS erp.approval_requests;
DROP TABLE IF EXISTS erp.approval_assignments;
DROP TABLE IF EXISTS erp.approval_actors;
DROP TABLE IF EXISTS erp.approval_fraud_config;
DROP TABLE IF EXISTS erp.approval_matrix;
DELETE FROM erp.immutable_tables WHERE table_name IN (
    'erp.approval_token_uses',
    'erp.approval_posting_tokens',
    'erp.approval_decisions',
    'erp.approval_requests'
);
