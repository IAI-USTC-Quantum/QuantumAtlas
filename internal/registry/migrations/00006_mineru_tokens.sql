-- +goose Up
-- Runtime-managed MinerU API token pool. The config
-- paper_access.mineru.api_tokens list seeds this table at boot (first
-- seen tokens only); the admin surface
-- (GET/POST/DELETE /api/admin/mineru/tokens) is the authoritative
-- mutation path, so operators can rotate keys without a restart.
-- rotated_at records when the token was last added / re-added.
CREATE TABLE IF NOT EXISTS mineru_tokens (
    token text PRIMARY KEY,
    rotated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS mineru_tokens;
