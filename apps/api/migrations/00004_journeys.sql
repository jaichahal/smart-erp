-- +goose Up
-- Journey definitions, instances, and append-only transitions (P1.14).
-- None of these tables use erp.make_immutable. Transitions are append-only
-- by grant only: the application role may insert and select, not update or delete.
-- Persona visibility is applied in the engine against server-evaluated personas;
-- row security here keeps every read and write inside the caller's company.

CREATE TABLE erp.journey_definitions (
    slug        text        PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9-]*$'),
    title_key   text        NOT NULL,
    group_key   text        NOT NULL,
    personas    text[]      NOT NULL CHECK (cardinality(personas) > 0),
    created_at  timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE erp.journey_definition_steps (
    slug          text        NOT NULL REFERENCES erp.journey_definitions (slug),
    step_id       text        NOT NULL CHECK (step_id ~ '^[a-z][a-z0-9_]*$'),
    position      int         NOT NULL CHECK (position > 0),
    kind          text        NOT NULL CHECK (kind IN ('form', 'validate', 'write_draft', 'route', 'await', 'post', 'read')),
    title_key     text        NOT NULL,
    input_schema  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    guard         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (slug, step_id),
    UNIQUE (slug, position)
);

CREATE TABLE erp.journey_instances (
    id               uuid        PRIMARY KEY,
    company_id       uuid        NOT NULL,
    slug             text        NOT NULL REFERENCES erp.journey_definitions (slug),
    user_id          text        NOT NULL,
    persona          text        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('running', 'awaiting', 'rejected', 'completed')),
    current_step_id  text        NOT NULL,
    server_state     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state_version    bigint      NOT NULL CHECK (state_version >= 1),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at       timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX journey_instances_company_idx ON erp.journey_instances (company_id, id);

CREATE TABLE erp.journey_transitions (
    id             uuid        PRIMARY KEY,
    company_id     uuid        NOT NULL,
    instance_id    uuid        NOT NULL REFERENCES erp.journey_instances (id),
    step_id        text        NOT NULL,
    outcome        text        NOT NULL CHECK (outcome IN ('completed', 'pending', 'rejected', 'refused')),
    input          jsonb       NOT NULL,
    result         jsonb       NOT NULL,
    state_version  bigint      NOT NULL,
    occurred_at    timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX journey_transitions_instance_idx ON erp.journey_transitions (instance_id, state_version);

REVOKE UPDATE ON erp.journey_definitions, erp.journey_definition_steps, erp.journey_transitions FROM erp_app;
REVOKE DELETE ON erp.journey_definitions, erp.journey_definition_steps, erp.journey_instances, erp.journey_transitions FROM erp_app;

ALTER TABLE erp.journey_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.journey_definition_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.journey_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp.journey_transitions ENABLE ROW LEVEL SECURITY;

CREATE POLICY journey_definitions_read ON erp.journey_definitions
    FOR SELECT TO erp_app
    USING (erp.current_user_id() IS NOT NULL);
CREATE POLICY journey_definition_steps_read ON erp.journey_definition_steps
    FOR SELECT TO erp_app
    USING (erp.current_user_id() IS NOT NULL);

CREATE POLICY journey_instances_tenant ON erp.journey_instances
    FOR ALL TO erp_app
    USING (company_id = erp.current_company() AND erp.current_user_id() IS NOT NULL)
    WITH CHECK (company_id = erp.current_company() AND erp.current_user_id() IS NOT NULL);

CREATE POLICY journey_transitions_tenant ON erp.journey_transitions
    FOR ALL TO erp_app
    USING (company_id = erp.current_company() AND erp.current_user_id() IS NOT NULL)
    WITH CHECK (company_id = erp.current_company() AND erp.current_user_id() IS NOT NULL);

-- Go-Live skeleton (R18.1). The import work belongs to later modules; this
-- definition is the ordered server-side journey those steps plug into.
-- Unlock runs only when the server re-reads a zero opening trial balance and
-- an approved stakeholder decision. Client input cannot set either.
INSERT INTO erp.journey_definitions (slug, title_key, group_key, personas)
VALUES ('go-live', 'journey.go_live.title', 'go_live', ARRAY['accountant']);

INSERT INTO erp.journey_definition_steps (slug, step_id, position, kind, title_key, input_schema, guard) VALUES
    ('go-live', 'chart_of_accounts', 1, 'form', 'journey.go_live.step.chart_of_accounts',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'open_customer_invoices', 2, 'form', 'journey.go_live.step.open_customer_invoices',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'open_supplier_invoices', 3, 'form', 'journey.go_live.step.open_supplier_invoices',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'opening_stock', 4, 'form', 'journey.go_live.step.opening_stock',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'opening_assets', 5, 'form', 'journey.go_live.step.opening_assets',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'bank_balances', 6, 'form', 'journey.go_live.step.bank_balances',
        '{"type":"object","required":["rows"],"properties":{"rows":{"type":"array"}}}'::jsonb, '{}'::jsonb),
    ('go-live', 'trial_balance', 7, 'validate', 'journey.go_live.step.trial_balance',
        '{"type":"object"}'::jsonb, '{"kind":"opening_trial_balance_zero"}'::jsonb),
    ('go-live', 'stakeholder_approval', 8, 'await', 'journey.go_live.step.stakeholder_approval',
        '{"type":"object"}'::jsonb, '{}'::jsonb),
    ('go-live', 'unlock_company', 9, 'post', 'journey.go_live.step.unlock_company',
        '{"type":"object"}'::jsonb,
        '{"all":[{"kind":"opening_trial_balance_zero"},{"kind":"approval_approved"}]}'::jsonb);

-- +goose Down
DROP TABLE IF EXISTS erp.journey_transitions;
DROP TABLE IF EXISTS erp.journey_instances;
DROP TABLE IF EXISTS erp.journey_definition_steps;
DROP TABLE IF EXISTS erp.journey_definitions;
