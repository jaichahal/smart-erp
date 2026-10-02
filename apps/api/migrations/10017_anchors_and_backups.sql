-- +goose Up
-- Anchors and verification runs are immutable (P1.6, 05 "External anchoring").
-- Backup, WAL, and restore bookkeeping is mutable so a run can move from
-- running to ok or failed. The manifest JSON is designed so P1.17 can copy the
-- same document onto an external drive without a second schema.

CREATE TABLE erp.anchors (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    chain_seq    bigint      NOT NULL,
    head_hash    text        NOT NULL CHECK (length(head_hash) = 64),
    row_count    bigint      NOT NULL,
    anchored_at  timestamptz NOT NULL,
    signature    text        NOT NULL,
    key_id       text        NOT NULL,
    payload      text        NOT NULL,
    UNIQUE (company_id, chain_seq)
);
SELECT erp.make_immutable('erp.anchors');

CREATE TABLE erp.verification_runs (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id             uuid        NOT NULL,
    intact                 boolean     NOT NULL,
    row_count              bigint      NOT NULL,
    first_break_seq        bigint,
    first_break_table      text        NOT NULL DEFAULT '',
    first_break_row_id     text        NOT NULL DEFAULT '',
    first_break_reason     text        NOT NULL DEFAULT '',
    anchored_head_matches  boolean     NOT NULL,
    head_hash              text        NOT NULL DEFAULT '',
    ran_at                 timestamptz NOT NULL
);
CREATE INDEX verification_runs_company_idx ON erp.verification_runs (company_id, ran_at);
SELECT erp.make_immutable('erp.verification_runs');

CREATE TABLE erp.backup_runs (
    id                text        PRIMARY KEY,
    started_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at       timestamptz,
    result            text        NOT NULL,
    detail            text        NOT NULL DEFAULT '',
    offsite_verified  boolean     NOT NULL DEFAULT false,
    site              text        NOT NULL,
    manifest_json     text        NOT NULL DEFAULT '',
    file_keys         text        NOT NULL DEFAULT '[]',
    pruned_at         timestamptz
);
CREATE INDEX backup_runs_finished_idx ON erp.backup_runs (finished_at DESC);

CREATE TABLE erp.wal_segments (
    name         text        PRIMARY KEY,
    written_at   timestamptz NOT NULL,
    archived_at  timestamptz,
    offsite_key  text        NOT NULL DEFAULT '',
    size_bytes   bigint      NOT NULL
);

CREATE TABLE erp.anchor_attempts (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid        NOT NULL,
    attempted_at  timestamptz NOT NULL,
    offsite_ok    boolean     NOT NULL,
    detail        text        NOT NULL DEFAULT ''
);
CREATE INDEX anchor_attempts_company_idx ON erp.anchor_attempts (company_id, attempted_at DESC);

CREATE TABLE erp.restore_approvals (
    token         uuid        PRIMARY KEY,
    backup_id     text        NOT NULL,
    approver_id   text        NOT NULL,
    approver_role text        NOT NULL,
    approved_at   timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.restore_log (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_id       text        NOT NULL,
    approval_token  text        NOT NULL DEFAULT '',
    approved_by     text        NOT NULL DEFAULT '',
    writes_enabled  boolean     NOT NULL,
    result          text        NOT NULL,
    refusal_code    text        NOT NULL DEFAULT '',
    detail          text        NOT NULL DEFAULT '',
    occurred_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

GRANT SELECT, INSERT, UPDATE ON erp.backup_runs, erp.wal_segments, erp.anchor_attempts, erp.restore_approvals, erp.restore_log TO erp_app;

-- +goose Down
DROP TABLE IF EXISTS erp.restore_log;
DROP TABLE IF EXISTS erp.restore_approvals;
DROP TABLE IF EXISTS erp.anchor_attempts;
DROP TABLE IF EXISTS erp.wal_segments;
DROP TABLE IF EXISTS erp.backup_runs;
DROP TABLE IF EXISTS erp.verification_runs;
DROP TABLE IF EXISTS erp.anchors;
DELETE FROM erp.immutable_tables WHERE table_name IN ('erp.anchors', 'erp.verification_runs');
