-- +goose Up
CREATE TABLE download_fleet_nodes (
 id text PRIMARY KEY, name text NOT NULL, secret_hash bytea NOT NULL UNIQUE,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','draining','rejected','revoked')),
 created_at timestamptz NOT NULL DEFAULT now(), last_seen timestamptz,
 capacity integer NOT NULL DEFAULT 0, browser_ok boolean NOT NULL DEFAULT false,
 disk_free_bytes bigint NOT NULL DEFAULT 0, spool_bytes bigint NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT ''
);
CREATE TABLE download_fleet_enrollments (
 token_hash bytea PRIMARY KEY, expires_at timestamptz NOT NULL,
 used_by text REFERENCES download_fleet_nodes(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE download_fleet_tasks (
 id text PRIMARY KEY, identity text NOT NULL, ref jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','staged','done','failed')),
 attempt_count integer NOT NULL DEFAULT 0, current_attempt text,
 deadline timestamptz NOT NULL, error text NOT NULL DEFAULT '', outcome jsonb,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 hook_pending boolean NOT NULL DEFAULT false, hook_tries integer NOT NULL DEFAULT 0,
 hook_next_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX download_fleet_task_active_identity ON download_fleet_tasks(identity) WHERE state IN ('queued','running','staged');
CREATE INDEX download_fleet_task_queue ON download_fleet_tasks(created_at) WHERE state='queued';
CREATE INDEX download_fleet_task_retention ON download_fleet_tasks(updated_at);
-- A parent admission replay must observe its original terminal result rather
-- than resetting the distinct-worker attempt budget. Explicit retries use a new ID.
CREATE TABLE download_fleet_admissions (
 request_id text PRIMARY KEY,
 task_id text NOT NULL REFERENCES download_fleet_tasks(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX download_fleet_admissions_task ON download_fleet_admissions(task_id);
CREATE TABLE download_fleet_attempts (
 id text PRIMARY KEY, task_id text NOT NULL REFERENCES download_fleet_tasks(id) ON DELETE CASCADE,
 worker_id text NOT NULL REFERENCES download_fleet_nodes(id),
 state text NOT NULL CHECK(state IN ('running','uploading','staged','done','failed','expired')),
 lease_expires timestamptz NOT NULL, deadline timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 failure text NOT NULL DEFAULT '', error text NOT NULL DEFAULT '', trace jsonb NOT NULL DEFAULT '[]',
 sha256 text NOT NULL DEFAULT '', size bigint NOT NULL DEFAULT 0, spool_path text NOT NULL DEFAULT '', upload_id text NOT NULL DEFAULT '',
 source_url text NOT NULL DEFAULT '', strategy text NOT NULL DEFAULT '', metadata jsonb NOT NULL DEFAULT '{}',
 UNIQUE(task_id, worker_id)
);
CREATE INDEX download_fleet_attempt_worker ON download_fleet_attempts(worker_id,state);
CREATE INDEX download_fleet_attempt_lease ON download_fleet_attempts(lease_expires) WHERE state IN ('running','uploading');

-- +goose Down
DROP TABLE download_fleet_attempts;
DROP TABLE download_fleet_admissions;
DROP TABLE download_fleet_tasks;
DROP TABLE download_fleet_enrollments;
DROP TABLE download_fleet_nodes;
