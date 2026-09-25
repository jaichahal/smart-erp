-- +goose Up
-- Notifications (P1.8, R13). Device tokens, preferences, alert rules, the inbox,
-- the digest queue, and the business-write witness are mutable. The delivery log
-- is immutable (one row per attempt) and keeps its own chain. It must not call
-- erp.chain_append: that sequence belongs to erp.audit_events, and a gap there
-- breaks chain verification.

CREATE TABLE erp.device_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   text        NOT NULL,
    user_id      text        NOT NULL,
    device_id    text        NOT NULL,
    token        text        NOT NULL,
    platform     text        NOT NULL CHECK (platform IN ('android', 'ios', 'console')),
    app_version  text        NOT NULL,
    last_seen_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (user_id, device_id),
    UNIQUE (token)
);
CREATE INDEX device_tokens_last_seen_idx ON erp.device_tokens (last_seen_at);
GRANT DELETE ON erp.device_tokens TO erp_app;

CREATE TABLE erp.notification_preferences (
    company_id  text        NOT NULL,
    user_id     text        NOT NULL,
    event_type  text        NOT NULL DEFAULT '*',
    channel     text        NOT NULL,
    device_id   text        NOT NULL DEFAULT '*',
    enabled     boolean     NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, user_id, event_type, channel, device_id)
);

CREATE TABLE erp.notification_quiet_hours (
    company_id text NOT NULL,
    user_id    text NOT NULL,
    start_min  int  NOT NULL CHECK (start_min BETWEEN 0 AND 1440),
    end_min    int  NOT NULL CHECK (end_min BETWEEN 0 AND 1440),
    zone       text NOT NULL,
    PRIMARY KEY (company_id, user_id)
);

CREATE TABLE erp.alert_rules (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      text        NOT NULL,
    document_type   text        NOT NULL,
    condition       jsonb       NOT NULL,
    recipient_roles text[]      NOT NULL,
    channel         text        NOT NULL,
    severity        text        NOT NULL,
    mode            text        NOT NULL CHECK (mode IN ('blocking', 'advisory')),
    enabled         boolean     NOT NULL DEFAULT true,
    builtin         boolean     NOT NULL DEFAULT false,
    state_version   int         NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX alert_rules_doc_idx ON erp.alert_rules (company_id, document_type);

CREATE TABLE erp.notifications (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id text        NOT NULL,
    user_id    text        NOT NULL,
    event_id   text        NOT NULL,
    event_type text        NOT NULL,
    severity   text        NOT NULL,
    grp        text        NOT NULL CHECK (grp IN ('needs_me', 'waiting', 'fyi')),
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    cleared_at timestamptz,
    acked_at   timestamptz,
    UNIQUE (company_id, user_id, event_id)
);
CREATE INDEX notifications_user_idx ON erp.notifications (company_id, user_id, grp, created_at);

CREATE TABLE erp.notification_digest_items (
    company_id text        NOT NULL,
    user_id    text        NOT NULL,
    event_id   text        NOT NULL,
    payload    jsonb       NOT NULL,
    queued_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    flushed_at timestamptz,
    PRIMARY KEY (company_id, user_id, event_id)
);

CREATE TABLE erp.notification_dedupe (
    event_id    text        PRIMARY KEY,
    company_id  text        NOT NULL,
    consumed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Witness row for the business write that commits with its outbox job (E1, E11).
CREATE TABLE erp.notification_submissions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    text        NOT NULL,
    document_type text        NOT NULL,
    document_id   text        NOT NULL,
    state         text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Immutable delivery log. chain_seq is local to this table, not erp.chain_head_log.
CREATE TABLE erp.delivery_log (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id        text        NOT NULL,
    event_id          text        NOT NULL,
    channel           text        NOT NULL,
    recipient_user_id text        NOT NULL,
    status            text        NOT NULL CHECK (status IN ('sent', 'failed')),
    attempt           int         NOT NULL,
    error             text        NOT NULL DEFAULT '',
    occurred_at       timestamptz NOT NULL,
    canonical         text        NOT NULL,
    chain_seq         bigint      NOT NULL,
    prev_hash         text        NOT NULL,
    hash              text        NOT NULL CHECK (length(hash) = 64),
    UNIQUE (company_id, chain_seq)
);
CREATE INDEX delivery_log_event_idx ON erp.delivery_log (company_id, event_id, channel, recipient_user_id);
SELECT erp.make_immutable('erp.delivery_log');

-- Per-user rows. A worker principal carries the system role and may see every row.
ALTER TABLE erp.device_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY device_tokens_self ON erp.device_tokens
    USING (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notification_preferences ENABLE ROW LEVEL SECURITY;
CREATE POLICY notification_preferences_self ON erp.notification_preferences
    USING (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notification_quiet_hours ENABLE ROW LEVEL SECURITY;
CREATE POLICY notification_quiet_hours_self ON erp.notification_quiet_hours
    USING (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY notifications_self ON erp.notifications
    USING (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notification_digest_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY notification_digest_items_self ON erp.notification_digest_items
    USING (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.alert_rules ENABLE ROW LEVEL SECURITY;
CREATE POLICY alert_rules_company ON erp.alert_rules
    USING (company_id = erp.current_company()::text OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company()::text OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notification_submissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY notification_submissions_company ON erp.notification_submissions
    USING (company_id = erp.current_company()::text OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company()::text OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.delivery_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY delivery_log_self ON erp.delivery_log
    USING (recipient_user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (recipient_user_id = erp.current_user_id() OR 'system' = ANY (erp.current_roles()));

ALTER TABLE erp.notification_dedupe ENABLE ROW LEVEL SECURITY;
CREATE POLICY notification_dedupe_system ON erp.notification_dedupe
    USING ('system' = ANY (erp.current_roles()))
    WITH CHECK ('system' = ANY (erp.current_roles()));

-- +goose Down
DROP POLICY IF EXISTS notification_dedupe_system ON erp.notification_dedupe;
DROP POLICY IF EXISTS delivery_log_self ON erp.delivery_log;
DROP POLICY IF EXISTS notification_submissions_company ON erp.notification_submissions;
DROP POLICY IF EXISTS alert_rules_company ON erp.alert_rules;
DROP POLICY IF EXISTS notification_digest_items_self ON erp.notification_digest_items;
DROP POLICY IF EXISTS notifications_self ON erp.notifications;
DROP POLICY IF EXISTS notification_quiet_hours_self ON erp.notification_quiet_hours;
DROP POLICY IF EXISTS notification_preferences_self ON erp.notification_preferences;
DROP POLICY IF EXISTS device_tokens_self ON erp.device_tokens;
DROP TABLE IF EXISTS erp.delivery_log;
DELETE FROM erp.immutable_tables WHERE table_name = 'erp.delivery_log';
DROP TABLE IF EXISTS erp.notification_submissions;
DROP TABLE IF EXISTS erp.notification_dedupe;
DROP TABLE IF EXISTS erp.notification_digest_items;
DROP TABLE IF EXISTS erp.notifications;
DROP TABLE IF EXISTS erp.alert_rules;
DROP TABLE IF EXISTS erp.notification_quiet_hours;
DROP TABLE IF EXISTS erp.notification_preferences;
DROP TABLE IF EXISTS erp.device_tokens;
