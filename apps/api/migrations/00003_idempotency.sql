-- +goose Up
-- Idempotency keys (04 "Transport"): a repeated key with the same body replays the
-- stored response; a different body is a 409. Rows expire after 24 hours and are
-- pruned by the scheduler running as erp_migrator; erp_app cannot delete.

CREATE TABLE erp.idempotency_keys (
    key           uuid        NOT NULL,
    principal_id  text        NOT NULL,
    request_hash  text        NOT NULL,
    status_code   int         NOT NULL,
    response_body bytea       NOT NULL,
    content_type  text        NOT NULL DEFAULT 'application/json',
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at    timestamptz NOT NULL DEFAULT clock_timestamp() + interval '24 hours',
    PRIMARY KEY (key, principal_id)
);
CREATE INDEX idempotency_keys_expires_idx ON erp.idempotency_keys (expires_at);
REVOKE UPDATE, DELETE ON erp.idempotency_keys FROM erp_app;
GRANT SELECT, INSERT ON erp.idempotency_keys TO erp_app;

-- +goose Down
DROP TABLE IF EXISTS erp.idempotency_keys;
