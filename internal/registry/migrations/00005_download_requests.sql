-- +goose Up
-- Durable admission log for the LOCAL-first downloader, before a fleet task exists.
CREATE TABLE downloader_requests (
    paper_id text PRIMARY KEY,
    request_id text NOT NULL DEFAULT gen_random_uuid()::text UNIQUE,
    input text NOT NULL,
    kind text NOT NULL,
    ref jsonb NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','done','failed')),
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX downloader_requests_pending ON downloader_requests(updated_at) WHERE state='queued';

-- +goose Down
DROP TABLE downloader_requests;
