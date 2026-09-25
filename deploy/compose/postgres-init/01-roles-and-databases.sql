-- smart-erp PostgreSQL bootstrap. Runs once, on first initialisation of the
-- data volume, as the `postgres` superuser connected to database `postgres`
-- (docker-entrypoint-initdb.d, psql -v ON_ERROR_STOP=1).
--
-- Passwords come from the container environment through psql's \getenv so the
-- same file serves dev (defaults in docker-compose.yml) and prod (.env secrets).
--
-- Roles:
--   erp_migrator  owns every schema object; runs migrations; CREATEDB so it can
--                 create erp_template and per-agent erp_test_<n> databases
--   erp_app       runtime role for api/worker/scheduler; no DDL, no CREATEDB;
--                 receives table-level grants from migrations (immutable tables
--                 get INSERT/SELECT only, never UPDATE/DELETE)
--   zitadel       owns the zitadel database; zitadel runs its own migrations.
--                 `zitadel start-from-init` unconditionally issues CREATE USER
--                 and CREATE DATABASE and only ignores "already exists", so
--                 the role needs CREATEDB and CREATEROLE (PostgreSQL 16
--                 CREATEROLE cannot touch roles it did not create, so erp_*
--                 are out of its reach). This keeps the superuser password
--                 out of the zitadel container.
--
-- Databases: erp (owner erp_migrator), zitadel (owner zitadel).

\set ON_ERROR_STOP on
\getenv migrator_password ERP_MIGRATOR_PASSWORD
\getenv app_password ERP_APP_PASSWORD
\getenv zitadel_password ZITADEL_DB_PASSWORD

CREATE ROLE erp_migrator LOGIN PASSWORD :'migrator_password' CREATEDB NOSUPERUSER NOCREATEROLE;
CREATE ROLE erp_app      LOGIN PASSWORD :'app_password'      NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE zitadel      LOGIN PASSWORD :'zitadel_password'  NOSUPERUSER CREATEDB CREATEROLE;

COMMENT ON ROLE erp_migrator IS 'smart-erp: schema owner and migration runner';
COMMENT ON ROLE erp_app      IS 'smart-erp: runtime role for api, worker, scheduler (RLS-scoped)';
COMMENT ON ROLE zitadel      IS 'smart-erp: identity provider database owner';

CREATE DATABASE erp     OWNER erp_migrator ENCODING 'UTF8' TEMPLATE template0;
CREATE DATABASE zitadel OWNER zitadel      ENCODING 'UTF8' TEMPLATE template0;

-- Nobody but the owning roles may even connect to the two databases.
REVOKE CONNECT ON DATABASE erp     FROM PUBLIC;
REVOKE CONNECT ON DATABASE zitadel FROM PUBLIC;
GRANT  CONNECT ON DATABASE erp     TO erp_migrator, erp_app;
GRANT  CONNECT ON DATABASE zitadel TO zitadel;

-- Lock down the public schema of erp: PostgreSQL 16 already withholds CREATE
-- from PUBLIC, this also removes USAGE, then re-grants USAGE to the app role.
-- erp_migrator, as database owner, is pg_database_owner and owns `public`.
-- Table-level grants to erp_app are the migrations' job; do NOT add ALTER
-- DEFAULT PRIVILEGES here, it would silently give erp_app UPDATE/DELETE on
-- immutable tables.
\connect erp
REVOKE ALL   ON SCHEMA public FROM PUBLIC;
GRANT  USAGE ON SCHEMA public TO erp_app;
