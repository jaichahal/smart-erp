-- +goose Up
-- DDL guard (05 "Immutable tables"). Event triggers require superuser, so this
-- directory is applied by cmd/migrate using ERP_ADMIN_DATABASE_URL against the erp
-- database, after the erp_migrator migrations. Idempotent.

CREATE SCHEMA IF NOT EXISTS ops;
GRANT USAGE ON SCHEMA ops TO erp_app, erp_migrator;

CREATE TABLE IF NOT EXISTS ops.ddl_log (
    id              bigserial   PRIMARY KEY,
    at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    command_tag     text        NOT NULL,
    object_type     text,
    object_identity text,
    role_name       text        NOT NULL,
    client_addr     inet,
    application     text,
    query           text
);
GRANT SELECT ON ops.ddl_log TO erp_app, erp_migrator;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ops.log_ddl() RETURNS event_trigger
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE r record;
BEGIN
    IF TG_EVENT = 'ddl_command_end' THEN
        FOR r IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
            INSERT INTO ops.ddl_log(command_tag, object_type, object_identity, role_name, client_addr, application, query)
            VALUES (r.command_tag, r.object_type, r.object_identity, current_user, inet_client_addr(),
                    current_setting('application_name', true), current_query());
        END LOOP;
    ELSIF TG_EVENT = 'sql_drop' THEN
        FOR r IN SELECT * FROM pg_event_trigger_dropped_objects() LOOP
            INSERT INTO ops.ddl_log(command_tag, object_type, object_identity, role_name, client_addr, application, query)
            VALUES (TG_TAG, r.object_type, r.object_identity, current_user, inet_client_addr(),
                    current_setting('application_name', true), current_query());
        END LOOP;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = 'ops_ddl_end') THEN
        CREATE EVENT TRIGGER ops_ddl_end ON ddl_command_end EXECUTE FUNCTION ops.log_ddl();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = 'ops_sql_drop') THEN
        CREATE EVENT TRIGGER ops_sql_drop ON sql_drop EXECUTE FUNCTION ops.log_ddl();
    END IF;
END;
$$;
-- +goose StatementEnd

-- The log itself is append-only at the database layer.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'ddl_log_immutable' AND tgrelid = 'ops.ddl_log'::regclass) THEN
        CREATE TRIGGER ddl_log_immutable BEFORE UPDATE OR DELETE ON ops.ddl_log
            FOR EACH ROW EXECUTE FUNCTION erp.immutable_guard();
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP EVENT TRIGGER IF EXISTS ops_sql_drop;
DROP EVENT TRIGGER IF EXISTS ops_ddl_end;
DROP FUNCTION IF EXISTS ops.log_ddl();
DROP TABLE IF EXISTS ops.ddl_log;
DROP SCHEMA IF EXISTS ops;
