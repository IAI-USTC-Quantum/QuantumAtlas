-- +goose Up
-- Durable PDF acquisition log. The in-memory progress tracker powers
-- low-latency UI polling; this append-only table preserves the same
-- transitions across restarts for admin diagnosis and audit.
CREATE TABLE paper_acquisition_events (
    event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'done', 'failed')),
    detail TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX paper_acquisition_events_paper_time
    ON paper_acquisition_events (paper_id, created_at DESC, event_id DESC);
CREATE INDEX paper_acquisition_events_failures
    ON paper_acquisition_events (created_at DESC, event_id DESC)
    WHERE state = 'failed';

-- +goose Down
DROP TABLE IF EXISTS paper_acquisition_events;
