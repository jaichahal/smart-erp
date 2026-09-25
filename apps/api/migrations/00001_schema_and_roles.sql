-- +goose Up
-- Runs as erp_migrator (owner). Roles erp_migrator and erp_app are created by
-- deploy/compose/postgres-init (dev) or deploy/nuc/bootstrap.sh (prod), never here.

CREATE SCHEMA IF NOT EXISTS erp;
GRANT USAGE ON SCHEMA erp TO erp_app;

-- Mutable tables created later by the migrator default to SELECT/INSERT/UPDATE for the app.
-- Immutable tables are narrowed by erp.make_immutable(). No DELETE anywhere by default.
ALTER DEFAULT PRIVILEGES FOR ROLE erp_migrator IN SCHEMA erp
    GRANT SELECT, INSERT, UPDATE ON TABLES TO erp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE erp_migrator IN SCHEMA erp
    GRANT USAGE, SELECT ON SEQUENCES TO erp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE erp_migrator IN SCHEMA erp
    GRANT EXECUTE ON FUNCTIONS TO erp_app;

-- River (job queue) creates its tables in public and needs full DML on them.
GRANT USAGE ON SCHEMA public TO erp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE erp_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO erp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE erp_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO erp_app;

-- Registry of immutable tables for the nightly presence check (R3.3).
CREATE TABLE erp.immutable_tables (
    table_name  text PRIMARY KEY,
    created_at  timestamptz NOT NULL DEFAULT clock_timestamp()
);
GRANT SELECT ON erp.immutable_tables TO erp_app;

-- Trigger body shared by every immutable table.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.immutable_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'immutable: rows in % cannot be updated or deleted', TG_TABLE_NAME
        USING ERRCODE = 'P0001', HINT = 'Corrections are new documents that reference the original.';
END;
$$;
-- +goose StatementEnd

-- Turn a table into an immutable one: grants, triggers, registry.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.make_immutable(tbl regclass) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s FROM erp_app', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %s TO erp_app', tbl);
    EXECUTE format('CREATE TRIGGER immutable_guard BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION erp.immutable_guard()', tbl);
    EXECUTE format('CREATE TRIGGER immutable_guard_truncate BEFORE TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION erp.immutable_guard()', tbl);
    INSERT INTO erp.immutable_tables(table_name) VALUES (tbl::text) ON CONFLICT DO NOTHING;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION erp.make_immutable(regclass) FROM erp_app, PUBLIC;

-- Lists registered immutable tables that have lost their guard trigger (nightly check).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.immutable_tables_missing_guard() RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT t.table_name
    FROM erp.immutable_tables t
    WHERE NOT EXISTS (
        SELECT 1 FROM pg_trigger tr
        WHERE tr.tgrelid = t.table_name::regclass AND tr.tgname = 'immutable_guard' AND NOT tr.tgisinternal
    );
$$;
-- +goose StatementEnd

-- Session principal accessors for RLS policies (set via SET LOCAL by kit/rls).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_company() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT nullif(current_setting('erp.company_id', true), '')::uuid $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_user_id() RETURNS text
LANGUAGE sql STABLE AS $$ SELECT nullif(current_setting('erp.user_id', true), '') $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_roles() RETURNS text[]
LANGUAGE sql STABLE AS $$ SELECT coalesce(string_to_array(nullif(current_setting('erp.roles', true), ''), ','), '{}'::text[]) $$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS erp.current_roles();
DROP FUNCTION IF EXISTS erp.current_user_id();
DROP FUNCTION IF EXISTS erp.current_company();
DROP FUNCTION IF EXISTS erp.immutable_tables_missing_guard();
DROP FUNCTION IF EXISTS erp.make_immutable(regclass);
DROP FUNCTION IF EXISTS erp.immutable_guard();
DROP TABLE IF EXISTS erp.immutable_tables;
DROP SCHEMA IF EXISTS erp;
