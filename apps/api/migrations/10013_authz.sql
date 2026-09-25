-- +goose Up
-- P1.3 roles, permissions, row-level security, SoD matrix, access review.
-- Mutable tables only: nothing here is passed to erp.make_immutable.
-- Roles are rows (R1.2). Policies read the session variables kit/rls sets
-- (erp.company_id, erp.user_id, erp.roles) and, when present, erp.territory_id,
-- erp.owner_id and erp.warehouse_id. Until the kit sets those three, territory
-- and ownership are taken from erp.users for the current user.

CREATE TABLE erp.personas (
    name             text PRIMARY KEY,
    least_privileged boolean NOT NULL DEFAULT false
);

CREATE TABLE erp.roles (
    name         text PRIMARY KEY,
    display_name text NOT NULL,
    persona      text NOT NULL REFERENCES erp.personas (name)
);

CREATE TABLE erp.permissions (
    code text PRIMARY KEY
);

CREATE TABLE erp.role_permissions (
    role_name  text NOT NULL REFERENCES erp.roles (name),
    permission text NOT NULL REFERENCES erp.permissions (code),
    scope      text NOT NULL CHECK (scope IN ('own', 'territory', 'warehouse', 'company')),
    PRIMARY KEY (role_name, permission)
);

CREATE TABLE erp.persona_tabs (
    persona    text    NOT NULL REFERENCES erp.personas (name),
    tab        text    NOT NULL,
    privileged boolean NOT NULL,
    PRIMARY KEY (persona, tab)
);

CREATE TABLE erp.endpoint_permissions (
    method     text    NOT NULL,
    path       text    NOT NULL,
    permission text    NOT NULL REFERENCES erp.permissions (code),
    privileged boolean NOT NULL,
    PRIMARY KEY (method, path)
);

CREATE TABLE erp.restricted_fields (
    field_name text PRIMARY KEY,
    permission text NOT NULL REFERENCES erp.permissions (code)
);

-- Users are disabled, never deleted (R1.9, A15).
CREATE TABLE erp.users (
    id            text        PRIMARY KEY,
    company_id    uuid        NOT NULL,
    name          text        NOT NULL,
    territory_id  uuid,
    warehouse_id  uuid,
    disabled_at   timestamptz,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    state_version bigint      NOT NULL DEFAULT 1
);

CREATE TABLE erp.user_roles (
    user_id              text        NOT NULL REFERENCES erp.users (id),
    role_name            text        NOT NULL REFERENCES erp.roles (name),
    active               boolean     NOT NULL DEFAULT true,
    override_approval_id text,
    granted_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, role_name)
);

CREATE TABLE erp.sod_rules (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id           uuid,
    kind                 text        NOT NULL CHECK (kind IN ('role_pair', 'action_pair')),
    left_code            text        NOT NULL,
    right_code           text        NOT NULL,
    state_version        bigint      NOT NULL DEFAULT 1,
    status               text        NOT NULL CHECK (status IN ('active', 'pending_approval')),
    approval_request_id  text,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, kind, left_code, right_code)
);

-- Named apart from erp.approval_matrix, which the approvals module owns.
CREATE TABLE erp.authz_approval_matrix (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id            uuid        NOT NULL,
    document_type         text        NOT NULL,
    threshold_amount      text        NOT NULL,
    threshold_currency    text        NOT NULL,
    below_threshold_role  text        NOT NULL,
    first_approver_role   text        NOT NULL,
    final_gate_role       text        NOT NULL,
    voting_any            integer,
    voting_of             integer,
    state_version         bigint      NOT NULL DEFAULT 1,
    status                text        NOT NULL CHECK (status IN ('active', 'pending_approval')),
    approval_request_id   text,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, document_type),
    CHECK (
        (voting_any IS NULL AND voting_of IS NULL)
        OR (voting_any IS NOT NULL AND voting_of IS NOT NULL AND voting_any >= 1 AND voting_any <= voting_of)
    ),
    CHECK (threshold_currency ~ '^[A-Z]{3}$'),
    CHECK (threshold_amount ~ '^-?[0-9]+(\.[0-9]+)?$')
);

CREATE TABLE erp.access_reviews (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  uuid        NOT NULL,
    period      text        NOT NULL,
    opened_at   timestamptz NOT NULL,
    grace_until timestamptz NOT NULL,
    UNIQUE (company_id, period)
);

CREATE TABLE erp.access_review_items (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    review_id     uuid        NOT NULL REFERENCES erp.access_reviews (id),
    user_id       text        NOT NULL REFERENCES erp.users (id),
    roles         text[]      NOT NULL,
    last_login_at timestamptz,
    confirmed_at  timestamptz,
    confirmed_by  text,
    UNIQUE (review_id, user_id)
);

CREATE TABLE erp.exceptions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid        NOT NULL,
    month      date        NOT NULL,
    kind       text        NOT NULL,
    subject_id text        NOT NULL,
    detail     jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (company_id, month, kind, subject_id)
);

-- Territory-scoped customer rows for list and count (A12, R1.10, R1.11).
-- Minimal columns. Later master-data migrations add attributes; they keep the policy.
CREATE TABLE erp.customers (
    id           uuid PRIMARY KEY,
    company_id   uuid NOT NULL,
    territory_id uuid,
    owner_id     text,
    warehouse_id uuid,
    name         text NOT NULL
);
CREATE INDEX customers_scope_idx ON erp.customers (company_id, territory_id, id);

CREATE TABLE erp.authz_settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);

INSERT INTO erp.authz_settings (key, value) VALUES ('access_review_grace_days', '14');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.users_no_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'users are disabled, never deleted';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_no_delete BEFORE DELETE ON erp.users
    FOR EACH ROW EXECUTE FUNCTION erp.users_no_delete();
CREATE TRIGGER users_no_truncate BEFORE TRUNCATE ON erp.users
    FOR EACH STATEMENT EXECUTE FUNCTION erp.users_no_delete();
CREATE TRIGGER user_roles_no_delete BEFORE DELETE ON erp.user_roles
    FOR EACH ROW EXECUTE FUNCTION erp.users_no_delete();

REVOKE DELETE, TRUNCATE ON erp.users FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.user_roles FROM erp_app;
REVOKE DELETE, TRUNCATE, UPDATE ON erp.exceptions FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.customers FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.sod_rules FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.authz_approval_matrix FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.access_reviews FROM erp_app;
REVOKE DELETE, TRUNCATE ON erp.access_review_items FROM erp_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.setting_uuid(p_name text) RETURNS uuid
LANGUAGE plpgsql STABLE AS $$
DECLARE
    raw text;
BEGIN
    raw := nullif(current_setting(p_name, true), '');
    IF raw IS NULL OR raw !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
        RETURN NULL;
    END IF;
    RETURN raw::uuid;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_territory() RETURNS uuid
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
DECLARE
    from_setting uuid;
BEGIN
    from_setting := setting_uuid('erp.territory_id');
    IF from_setting IS NOT NULL THEN
        RETURN from_setting;
    END IF;
    RETURN (SELECT territory_id FROM users WHERE id = current_user_id());
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_warehouse() RETURNS uuid
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
DECLARE
    from_setting uuid;
BEGIN
    from_setting := setting_uuid('erp.warehouse_id');
    IF from_setting IS NOT NULL THEN
        RETURN from_setting;
    END IF;
    RETURN (SELECT warehouse_id FROM users WHERE id = current_user_id());
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.current_owner() RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
    SELECT COALESCE(nullif(current_setting('erp.owner_id', true), ''), current_user_id());
$$;
-- +goose StatementEnd

-- Widest scope wins. No matching row means no permission (fail closed, R1.12).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.permission_scope(p_permission text) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
    SELECT rp.scope
    FROM role_permissions rp
    WHERE rp.role_name = ANY (current_roles())
      AND rp.permission = p_permission
    ORDER BY CASE rp.scope
        WHEN 'company' THEN 1
        WHEN 'warehouse' THEN 2
        WHEN 'territory' THEN 3
        WHEN 'own' THEN 4
        ELSE 5
    END
    LIMIT 1;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.row_visible(
    p_company uuid,
    p_territory uuid,
    p_owner text,
    p_warehouse uuid,
    p_permission text
) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
    SELECT p_company IS NOT DISTINCT FROM current_company()
       AND CASE permission_scope(p_permission)
            WHEN 'company' THEN true
            WHEN 'territory' THEN p_territory IS NOT DISTINCT FROM current_territory()
            WHEN 'warehouse' THEN p_warehouse IS NOT DISTINCT FROM current_warehouse()
            WHEN 'own' THEN p_owner IS NOT DISTINCT FROM current_owner()
            ELSE false
           END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.sod_conflicts(p_roles text[])
RETURNS TABLE (kind text, left_code text, right_code text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
    SELECT r.kind, r.left_code, r.right_code
    FROM sod_rules r
    WHERE r.status = 'active'
      AND (r.company_id IS NULL OR r.company_id IS NOT DISTINCT FROM current_company())
      AND (
            (r.kind = 'role_pair'
                AND r.left_code = ANY (p_roles)
                AND r.right_code = ANY (p_roles)
                AND r.left_code <> r.right_code)
         OR (r.kind = 'action_pair'
                AND EXISTS (
                    SELECT 1 FROM role_permissions rp
                    WHERE rp.role_name = ANY (p_roles) AND rp.permission = r.left_code)
                AND EXISTS (
                    SELECT 1 FROM role_permissions rp
                    WHERE rp.role_name = ANY (p_roles) AND rp.permission = r.right_code))
          );
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION erp.user_can_authenticate(p_id text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = erp, pg_catalog AS $$
    SELECT EXISTS (
        SELECT 1 FROM users WHERE id = p_id AND disabled_at IS NULL
    );
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION erp.setting_uuid(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.current_territory() FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.current_warehouse() FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.current_owner() FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.permission_scope(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.row_visible(uuid, uuid, text, uuid, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.sod_conflicts(text[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.user_can_authenticate(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION erp.users_no_delete() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION erp.setting_uuid(text) TO erp_app;
GRANT EXECUTE ON FUNCTION erp.current_territory() TO erp_app;
GRANT EXECUTE ON FUNCTION erp.current_warehouse() TO erp_app;
GRANT EXECUTE ON FUNCTION erp.current_owner() TO erp_app;
GRANT EXECUTE ON FUNCTION erp.permission_scope(text) TO erp_app;
GRANT EXECUTE ON FUNCTION erp.row_visible(uuid, uuid, text, uuid, text) TO erp_app;
GRANT EXECUTE ON FUNCTION erp.sod_conflicts(text[]) TO erp_app;
GRANT EXECUTE ON FUNCTION erp.user_can_authenticate(text) TO erp_app;

ALTER TABLE erp.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.user_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.sod_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.authz_approval_matrix ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.access_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.access_review_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.exceptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.customers ENABLE ROW LEVEL SECURITY;

CREATE POLICY users_select ON erp.users FOR SELECT
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.current_user_id() IS NOT NULL);
CREATE POLICY users_insert ON erp.users FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('user:write') = 'company');
CREATE POLICY users_update ON erp.users FOR UPDATE
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('user:write') = 'company')
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company());

CREATE POLICY user_roles_select ON erp.user_roles FOR SELECT
    USING (EXISTS (
        SELECT 1 FROM erp.users u
        WHERE u.id = user_roles.user_id AND u.company_id IS NOT DISTINCT FROM erp.current_company()
    ));
CREATE POLICY user_roles_insert ON erp.user_roles FOR INSERT
    WITH CHECK (
        erp.permission_scope('user:write') = 'company'
        AND EXISTS (
            SELECT 1 FROM erp.users u
            WHERE u.id = user_roles.user_id AND u.company_id IS NOT DISTINCT FROM erp.current_company()
        )
    );
CREATE POLICY user_roles_update ON erp.user_roles FOR UPDATE
    USING (erp.permission_scope('user:write') = 'company')
    WITH CHECK (erp.permission_scope('user:write') = 'company');

CREATE POLICY sod_select ON erp.sod_rules FOR SELECT
    USING (
        (company_id IS NULL OR company_id IS NOT DISTINCT FROM erp.current_company())
        AND erp.permission_scope('sod:read') IS NOT NULL
    );
CREATE POLICY sod_insert ON erp.sod_rules FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('sod:write') = 'company');
CREATE POLICY sod_update ON erp.sod_rules FOR UPDATE
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('sod:write') = 'company')
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('sod:write') = 'company');

CREATE POLICY approval_matrix_select ON erp.authz_approval_matrix FOR SELECT
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('approval_matrix:read') IS NOT NULL);
CREATE POLICY approval_matrix_insert ON erp.authz_approval_matrix FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('approval_matrix:write') = 'company');
CREATE POLICY approval_matrix_update ON erp.authz_approval_matrix FOR UPDATE
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('approval_matrix:write') = 'company')
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('approval_matrix:write') = 'company');

CREATE POLICY access_reviews_select ON erp.access_reviews FOR SELECT
    USING (
        company_id IS NOT DISTINCT FROM erp.current_company()
        AND (
            erp.permission_scope('access_review:read') IS NOT NULL
            OR erp.permission_scope('access_review:write') IS NOT NULL
            OR erp.permission_scope('access_review:confirm') IS NOT NULL
        )
    );
CREATE POLICY access_reviews_insert ON erp.access_reviews FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('access_review:write') = 'company');

CREATE POLICY access_review_items_select ON erp.access_review_items FOR SELECT
    USING (
        (
            erp.permission_scope('access_review:read') IS NOT NULL
            OR erp.permission_scope('access_review:write') IS NOT NULL
            OR erp.permission_scope('access_review:confirm') IS NOT NULL
        )
        AND EXISTS (
            SELECT 1 FROM erp.access_reviews r
            WHERE r.id = access_review_items.review_id
              AND r.company_id IS NOT DISTINCT FROM erp.current_company()
        )
    );
CREATE POLICY access_review_items_insert ON erp.access_review_items FOR INSERT
    WITH CHECK (
        erp.permission_scope('access_review:write') = 'company'
        AND EXISTS (
            SELECT 1 FROM erp.access_reviews r
            WHERE r.id = access_review_items.review_id
              AND r.company_id IS NOT DISTINCT FROM erp.current_company()
        )
    );
CREATE POLICY access_review_items_update ON erp.access_review_items FOR UPDATE
    USING (
        (
            erp.permission_scope('access_review:confirm') IS NOT NULL
            OR erp.permission_scope('access_review:write') = 'company'
        )
        AND EXISTS (
            SELECT 1 FROM erp.access_reviews r
            WHERE r.id = access_review_items.review_id
              AND r.company_id IS NOT DISTINCT FROM erp.current_company()
        )
    )
    WITH CHECK (
        erp.permission_scope('access_review:confirm') IS NOT NULL
        OR erp.permission_scope('access_review:write') = 'company'
    );

CREATE POLICY exceptions_select ON erp.exceptions FOR SELECT
    USING (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('exception:read') IS NOT NULL);
CREATE POLICY exceptions_insert ON erp.exceptions FOR INSERT
    WITH CHECK (company_id IS NOT DISTINCT FROM erp.current_company() AND erp.permission_scope('exception:write') = 'company');

CREATE POLICY customers_select ON erp.customers FOR SELECT
    USING (erp.row_visible(company_id, territory_id, owner_id, warehouse_id, 'customer:read'));

INSERT INTO erp.personas (name, least_privileged) VALUES
    ('least_privileged', true),
    ('sales_agent', false),
    ('collection_agent', false),
    ('delivery_driver', false),
    ('accountant', false),
    ('approver', false),
    ('credit_controller', false),
    ('stock_counter', false),
    ('production_supervisor', false),
    ('petty_cash_custodian', false),
    ('auditor', false),
    ('stakeholder', false),
    ('system_manager', false),
    ('system', false);

INSERT INTO erp.roles (name, display_name, persona) VALUES
    ('sales_agent', 'Sales Agent', 'sales_agent'),
    ('collection_agent', 'Collection Agent', 'collection_agent'),
    ('delivery_driver', 'Delivery Driver', 'delivery_driver'),
    ('accountant', 'Accountant', 'accountant'),
    ('approver', 'Approver', 'approver'),
    ('credit_controller', 'Credit Controller', 'credit_controller'),
    ('stock_counter', 'Stock Counter', 'stock_counter'),
    ('production_supervisor', 'Production Supervisor', 'production_supervisor'),
    ('petty_cash_custodian', 'Petty Cash Custodian', 'petty_cash_custodian'),
    ('auditor', 'Auditor', 'auditor'),
    ('stakeholder', 'Stakeholder', 'stakeholder'),
    ('system_manager', 'System Manager', 'system_manager'),
    ('system', 'system', 'system');

INSERT INTO erp.permissions (code) VALUES
    ('customer:read'),
    ('document:enter'),
    ('document:approve'),
    ('goods:receive'),
    ('stock:count'),
    ('vendor:create'),
    ('vendor:pay'),
    ('correction:request'),
    ('correction:approve'),
    ('field:cost:read'),
    ('field:margin:read'),
    ('journal:read'),
    ('receivable:read'),
    ('credit:override'),
    ('production:post'),
    ('petty_cash:write'),
    ('user:write'),
    ('sod:read'),
    ('sod:write'),
    ('approval_matrix:read'),
    ('approval_matrix:write'),
    ('access_review:read'),
    ('access_review:write'),
    ('access_review:confirm'),
    ('exception:read'),
    ('exception:write'),
    ('status:read');

INSERT INTO erp.role_permissions (role_name, permission, scope) VALUES
    ('sales_agent', 'customer:read', 'territory'),
    ('sales_agent', 'document:enter', 'own'),
    ('sales_agent', 'correction:request', 'own'),
    ('collection_agent', 'customer:read', 'territory'),
    ('collection_agent', 'receivable:read', 'territory'),
    ('delivery_driver', 'goods:receive', 'own'),
    ('accountant', 'customer:read', 'company'),
    ('accountant', 'field:cost:read', 'company'),
    ('accountant', 'field:margin:read', 'company'),
    ('accountant', 'vendor:create', 'company'),
    ('accountant', 'journal:read', 'company'),
    ('approver', 'document:approve', 'company'),
    ('approver', 'correction:approve', 'company'),
    ('approver', 'approval_matrix:read', 'company'),
    ('credit_controller', 'customer:read', 'company'),
    ('credit_controller', 'credit:override', 'company'),
    ('stock_counter', 'stock:count', 'warehouse'),
    ('production_supervisor', 'production:post', 'company'),
    ('petty_cash_custodian', 'petty_cash:write', 'own'),
    ('auditor', 'customer:read', 'company'),
    ('auditor', 'field:cost:read', 'company'),
    ('auditor', 'field:margin:read', 'company'),
    ('auditor', 'journal:read', 'company'),
    ('auditor', 'sod:read', 'company'),
    ('auditor', 'exception:read', 'company'),
    ('auditor', 'access_review:read', 'company'),
    ('stakeholder', 'customer:read', 'company'),
    ('stakeholder', 'field:cost:read', 'company'),
    ('stakeholder', 'field:margin:read', 'company'),
    ('stakeholder', 'access_review:read', 'company'),
    ('stakeholder', 'access_review:confirm', 'company'),
    ('stakeholder', 'exception:read', 'company'),
    ('system_manager', 'customer:read', 'company'),
    ('system_manager', 'field:cost:read', 'company'),
    ('system_manager', 'field:margin:read', 'company'),
    ('system_manager', 'user:write', 'company'),
    ('system_manager', 'sod:read', 'company'),
    ('system_manager', 'sod:write', 'company'),
    ('system_manager', 'approval_matrix:read', 'company'),
    ('system_manager', 'approval_matrix:write', 'company'),
    ('system_manager', 'access_review:read', 'company'),
    ('system_manager', 'exception:read', 'company'),
    ('system_manager', 'status:read', 'company'),
    ('system_manager', 'vendor:pay', 'company'),
    ('system', 'customer:read', 'company'),
    ('system', 'access_review:read', 'company'),
    ('system', 'access_review:write', 'company'),
    ('system', 'exception:read', 'company'),
    ('system', 'exception:write', 'company');

INSERT INTO erp.persona_tabs (persona, tab, privileged) VALUES
    ('sales_agent', 'customers', false),
    ('sales_agent', 'orders', false),
    ('accountant', 'ledger', true),
    ('approver', 'approvals', true),
    ('system_manager', 'settings', true),
    ('system_manager', 'users', true),
    ('stakeholder', 'access_review', true),
    ('auditor', 'audit', true);

INSERT INTO erp.endpoint_permissions (method, path, permission, privileged) VALUES
    ('GET', '/customers', 'customer:read', false),
    ('GET', '/sod-matrix', 'sod:read', true),
    ('POST', '/sod-matrix', 'sod:write', true),
    ('GET', '/approval-matrix', 'approval_matrix:read', true),
    ('POST', '/approval-matrix', 'approval_matrix:write', true),
    ('GET', '/status', 'status:read', true);

INSERT INTO erp.restricted_fields (field_name, permission) VALUES
    ('cost', 'field:cost:read'),
    ('unit_cost', 'field:cost:read'),
    ('total_cost', 'field:cost:read'),
    ('line_cost', 'field:cost:read'),
    ('margin', 'field:margin:read'),
    ('unit_margin', 'field:margin:read'),
    ('margin_amount', 'field:margin:read'),
    ('margin_percent', 'field:margin:read'),
    ('gross_margin', 'field:margin:read');

-- Active global matrix. Company-scoped edits land as pending_approval (04 Masters).
INSERT INTO erp.sod_rules (company_id, kind, left_code, right_code, status) VALUES
    (NULL, 'role_pair', 'sales_agent', 'approver', 'active'),
    (NULL, 'role_pair', 'delivery_driver', 'stock_counter', 'active'),
    (NULL, 'action_pair', 'document:enter', 'document:approve', 'active'),
    (NULL, 'action_pair', 'goods:receive', 'stock:count', 'active'),
    (NULL, 'action_pair', 'vendor:create', 'vendor:pay', 'active'),
    (NULL, 'action_pair', 'correction:request', 'correction:approve', 'active');

-- +goose Down
DROP TABLE IF EXISTS erp.access_review_items, erp.access_reviews, erp.exceptions, erp.customers,
    erp.authz_approval_matrix, erp.sod_rules, erp.user_roles, erp.users, erp.endpoint_permissions,
    erp.restricted_fields, erp.role_permissions, erp.persona_tabs, erp.roles, erp.personas,
    erp.permissions, erp.authz_settings CASCADE;
DROP FUNCTION IF EXISTS erp.user_can_authenticate(text);
DROP FUNCTION IF EXISTS erp.sod_conflicts(text[]);
DROP FUNCTION IF EXISTS erp.row_visible(uuid, uuid, text, uuid, text);
DROP FUNCTION IF EXISTS erp.permission_scope(text);
DROP FUNCTION IF EXISTS erp.current_owner();
DROP FUNCTION IF EXISTS erp.current_warehouse();
DROP FUNCTION IF EXISTS erp.current_territory();
DROP FUNCTION IF EXISTS erp.setting_uuid(text);
DROP FUNCTION IF EXISTS erp.users_no_delete();
