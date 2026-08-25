-- +goose Up
-- Usage metering for the metered agentic-search surface
-- (POST /api/search/agentic). plans is the small lookup table of named
-- bundles; user_quotas binds a PocketBase user id to a plan (plus an
-- optional per-user limit override); usage_daily is the per-(user, day,
-- metric) counter the atomic CheckAndReserve increments.
--
-- "Day" is the database's CURRENT_DATE — i.e. the Postgres server's
-- timezone (deployments run with TZ=UTC), documented in internal/usage.

-- plans: named bundles carrying a daily agentic-search limit.
CREATE TABLE plans (
    name TEXT PRIMARY KEY,
    daily_agentic_search_limit INT NOT NULL CHECK (daily_agentic_search_limit >= 0),
    description TEXT NOT NULL DEFAULT ''
);

INSERT INTO plans (name, daily_agentic_search_limit, description) VALUES
    ('free', 10000, 'Default plan'),
    ('pro', 100000, 'Pro plan'),
    ('max', 1000000, 'Max plan');

-- user_quotas: optional per-user binding to a plan, plus an optional
-- hard override that wins over the plan limit. Absent row = the
-- server-configured default limit applies (see internal/usage).
CREATE TABLE user_quotas (
    user_id TEXT PRIMARY KEY,
    plan TEXT NOT NULL DEFAULT 'free' REFERENCES plans(name),
    daily_limit_override INT CHECK (daily_limit_override IS NULL OR daily_limit_override >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- usage_daily: the counter table. count is the number of consumed calls
-- for the (user, day, metric) bucket; llm_tokens accumulates the LLM
-- token spend the microservice reported for that bucket.
CREATE TABLE usage_daily (
    user_id TEXT NOT NULL,
    day DATE NOT NULL,
    metric TEXT NOT NULL,
    count INT NOT NULL DEFAULT 0 CHECK (count >= 0),
    llm_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day, metric)
);

-- +goose Down
DROP TABLE IF EXISTS usage_daily;
DROP TABLE IF EXISTS user_quotas;
DROP TABLE IF EXISTS plans;
