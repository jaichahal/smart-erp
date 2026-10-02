-- +goose Up
-- Phase 0 design tokens served to both client shells (P0.1, R15.6).
CREATE TABLE erp.design_tokens (
    id       text PRIMARY KEY,
    document jsonb NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS erp.design_tokens;
