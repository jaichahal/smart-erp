-- +goose Up
-- Version 10040 sits above concurrent Wave 1 migrations so goose can apply it on a
-- shared dev template that already recorded other agents' files. CI applies it after 00003.
-- Company, fiscal years, periods, exceptions report, holiday calendar, and clocks.
-- These tables are mutable and versioned (state_version). Number allocations and voids
-- are the immutable tables in the next migration.
--
-- RLS: erp_app sees a row only when company_id (or companies.id) is the session
-- company, or the session role is system (scheduler). kit/rls sets those variables.

CREATE TABLE erp.companies (
    id            uuid        PRIMARY KEY,
    legal_name    text        NOT NULL,
    state_version bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at    timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.fiscal_years (
    company_id    uuid        NOT NULL REFERENCES erp.companies(id),
    year          int         NOT NULL,
    start_date    date        NOT NULL,
    end_date      date        NOT NULL,
    state_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (company_id, year),
    CHECK (end_date >= start_date)
);

CREATE TABLE erp.periods (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid        NOT NULL REFERENCES erp.companies(id),
    fiscal_year   int         NOT NULL,
    month         int         CHECK (month IS NULL OR month BETWEEN 1 AND 12),
    kind          text        NOT NULL CHECK (kind IN ('month', 'audit_adjustment')),
    status        text        NOT NULL CHECK (status IN ('open', 'soft_closed', 'hard_closed')),
    start_date    date        NOT NULL,
    end_date      date        NOT NULL,
    state_version bigint      NOT NULL DEFAULT 1,
    closed_at     timestamptz,
    closed_by     text,
    CHECK (end_date >= start_date),
    CHECK ((kind = 'month' AND month IS NOT NULL) OR (kind = 'audit_adjustment' AND month IS NULL)),
    FOREIGN KEY (company_id, fiscal_year) REFERENCES erp.fiscal_years(company_id, year)
);
CREATE UNIQUE INDEX periods_month_uidx ON erp.periods (company_id, fiscal_year, month) WHERE month IS NOT NULL;
CREATE UNIQUE INDEX periods_audit_uidx ON erp.periods (company_id, fiscal_year) WHERE kind = 'audit_adjustment';
CREATE INDEX periods_dates_idx ON erp.periods (company_id, start_date, end_date);

CREATE TABLE erp.ledger_exceptions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    uuid        NOT NULL REFERENCES erp.companies(id),
    kind          text        NOT NULL,
    period_id     uuid        NOT NULL REFERENCES erp.periods(id),
    posting_date  date        NOT NULL,
    doc_type      text        NOT NULL,
    doc_id        text        NOT NULL,
    actor_id      text        NOT NULL,
    reason        text        NOT NULL,
    occurred_at   timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX ledger_exceptions_month_idx ON erp.ledger_exceptions (company_id, occurred_at);

-- Mutable counter. Allocation takes SELECT ... FOR UPDATE on this row (R4.5, C2).
CREATE TABLE erp.number_sequences (
    company_id   uuid   NOT NULL REFERENCES erp.companies(id),
    doc_type     text   NOT NULL,
    fiscal_year  int    NOT NULL,
    next_number  bigint NOT NULL DEFAULT 1 CHECK (next_number >= 1),
    PRIMARY KEY (company_id, doc_type, fiscal_year)
);

-- Business-hours calendar (ADR-12, R13.8). One row per company. Holidays live in
-- the row so a replace does not need DELETE, which erp_app is not granted.
CREATE TABLE erp.business_calendars (
    company_id     uuid     PRIMARY KEY,
    timezone       text     NOT NULL,
    open_minute    smallint NOT NULL,
    close_minute   smallint NOT NULL,
    weekend_days   text[]   NOT NULL,
    holidays       jsonb    NOT NULL DEFAULT '[]'::jsonb,
    state_version  bigint   NOT NULL DEFAULT 1,
    CHECK (open_minute >= 0 AND open_minute < close_minute AND close_minute <= 1440),
    CHECK (cardinality(weekend_days) >= 1)
);

CREATE TABLE erp.clocks (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id       uuid        NOT NULL,
    doc_id           text        NOT NULL,
    doc_type         text        NOT NULL,
    kind             text        NOT NULL,
    started_at       timestamptz NOT NULL,
    due_at           timestamptz NOT NULL,
    window_seconds   bigint      NOT NULL CHECK (window_seconds >= 0),
    closed_at        timestamptz,
    closed_by_doc_id text,
    escalated_at     timestamptz,
    state_version    bigint      NOT NULL DEFAULT 1
);
CREATE INDEX clocks_due_idx ON erp.clocks (due_at) WHERE closed_at IS NULL AND escalated_at IS NULL;

GRANT SELECT, INSERT, UPDATE ON
    erp.companies, erp.fiscal_years, erp.periods, erp.ledger_exceptions,
    erp.number_sequences, erp.business_calendars, erp.clocks
TO erp_app;

ALTER TABLE erp.companies ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.fiscal_years ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.periods ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.ledger_exceptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.number_sequences ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.business_calendars ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.clocks ENABLE ROW LEVEL SECURITY;

CREATE POLICY companies_tenant ON erp.companies FOR ALL TO erp_app
    USING (id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY fiscal_years_tenant ON erp.fiscal_years FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY periods_tenant ON erp.periods FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY ledger_exceptions_tenant ON erp.ledger_exceptions FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY number_sequences_tenant ON erp.number_sequences FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY business_calendars_tenant ON erp.business_calendars FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

CREATE POLICY clocks_tenant ON erp.clocks FOR ALL TO erp_app
    USING (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()))
    WITH CHECK (company_id = erp.current_company() OR 'system' = ANY (erp.current_roles()));

-- +goose Down
DROP TABLE IF EXISTS erp.clocks;
DROP TABLE IF EXISTS erp.business_calendars;
DROP TABLE IF EXISTS erp.number_sequences;
DROP TABLE IF EXISTS erp.ledger_exceptions;
DROP TABLE IF EXISTS erp.periods;
DROP TABLE IF EXISTS erp.fiscal_years;
DROP TABLE IF EXISTS erp.companies;
