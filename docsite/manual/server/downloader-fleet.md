# Multi-worker downloader: master setup and operations

The coordinator is part of **qatlasd**, not another service. Downloads remain
local-first. Remote work enters a durable PostgreSQL queue; approved workers
initiate registration, heartbeat, task claims, uploads and receipt checks. The
master never dials workers. Different papers run concurrently; a paper tries
workers sequentially, with bounded attempts and deadlines.

## Enable the master

```yaml
paper_access:
  enabled: true
downloader:
  enabled: true
  concurrency: 2                 # local fetch slots; remote waits release these
  remote:
    enabled: true
    max_in_flight: 6
    max_worker_in_flight: 2
    max_worker_attempts: 3
    task_timeout: 15m
    worker_timeout: 6m
    lease_duration: 60s
    spool_max_bytes: 2147483648   # master staging quota: 2 GiB
    # spool_dir: /srv/qatlas/downloader-spool
    # Default: <configured pb_data_dir>/downloader-spool
```

Configure `postgres.dsn` and the existing object store. Normal startup migrations
add fleet tables (`00004_downloadfleet.sql`) and a durable local-first admission
journal (`00005_download_requests.sql`). Wait for the existing asynchronous schema
manager to converge before enrolling workers. `remote.enabled` together with a
nonempty legacy `downloader.proxy.url` is rejected, not silently mixed.

The master uses a separate fleet connection pool to the **same PostgreSQL DB**
(16–64 maximum connections; default 20), in addition to its existing registry pool.
Budget database connections accordingly. Archive callbacks use the registry pool,
so they cannot starve waiting for a connection held by their own fleet task lock.
Single-master deployment is supported; this is not a multi-master HA rollout.
Persist the database and master spool directory across restarts.

## Register and approve computers

1. Visit `/en/admin/downloader-workers` or `/zh/admin/downloader-workers` using an
   administrator session; generate an expiring one-use enrollment credential
   (default validity 15 minutes).
2. Configure the computer's worker with the master HTTPS origin and credential:
   see [worker deployment](downloader-workers.md).
3. Approve the pending node. Registration has already persisted its independent
   random credential; normal reconnects do not need another approval.
4. Enroll each additional computer with a **new credential and a unique volume**.

A lost registration response can retry the same persisted identity. An enrollment
credential cannot register a different identity after consumption. Pending workers
can check status, but cannot claim work or upload results. Worker secrets are
hashed on the master and never shown by the admin API.

The admin page shows nodes, capacity, browser health, heartbeat time, spool/disk
usage, recent tasks and errors. `drain` stops new work while accepting existing
results; `enable` re-enables draining nodes. `reject` and `revoke` are terminal:
re-enrollment needs a fresh identity. Preserve unacknowledged old spool files for
investigation before retiring their volume. Revocation invalidates authentication.
Admin actions require human admin sessions, not worker secrets or PATs.

The downloader page adds persisted remote progress alongside its existing local
snapshot. `/api/downloader/remote-jobs` requires `papers:read` and exposes jobs,
not worker topology or credentials. States are queued, running (download/upload),
staged (archiving), done and failed. Admin snapshots are bounded to 500 rows.
The original jobs snapshot also merges up to 512 durable queued admissions,
including those waiting for a local execution slot. Larger backlogs remain safely
queued but the initial UI is a bounded snapshot, not a paginated history browser.

## File archive and receipts

A worker's ready PDF is temporary. The master streams uploads into quota-bounded
staging files and independently checks size, SHA-256, PDF signature and EOF. It
then writes the existing object store and registers the PDF asset in registry.
Only **both operations succeeding** creates a durable `done` receipt.

Canonical-key conflicts use the existing stored object's actual hash and size
for registry registration, not another candidate's metadata. The receipt retains
the original accepted upload's digest and size, allowing the worker to identify
and delete its acknowledged copy. A received/staged response is not an archive ACK.

MinerU/index hooks are delivered through an at-least-once durable outbox after
archive and **do not delay the receipt**. Consumers must coalesce duplicates.
When MinerU is enabled, its outbox entry remains pending until the converter
reports durable completion, not merely an in-memory queued job. Reconciliation
calls Ensure again after restarts for both DOI and arXiv assets; it does not wait
inside the archive/receipt path. Explicitly disabled conversion is skipped.
The outbox retries with backoff up to 20 times, retaining failure diagnostics.
There is no distributed transaction across storage and PostgreSQL: partial
success and restart recovery replay the idempotent storage/registration operation.

Remote execution leases renew within the execution/task budget. Uploads have a
separate fixed two-minute default budget, still bounded by the task deadline.
A failed transfer retries the same attempt with the already-downloaded PDF;
generation fencing stops abandoned transfers from overwriting newer attempts.
Slow uploads do not block renewal for other downloads. Staged archive failures
are retried and retained for seven days by default, then expire. Worker-local
results have their own 24-hour default retention; neither side stores indefinitely.

Restart recovery includes accepted requests before remote delegation, active
remote tasks, staged commits and post-archive hook delivery. Excess admitted work
waits in PostgreSQL instead of spawning unbounded in-memory waiting goroutines.

## Reverse proxy and security

Worker connections require HTTPS (explicit HTTP opt-in is for trusted development
only). No worker/CDP inbound ports or NAT port forwarding are required. Configure
the reverse proxy to pass Authorization and X-Download-*/X-PDF-* headers, allow
PDF uploads up to **100 MiB**, and allow the transfer timeout. PocketBase's default
32 MiB limit is overridden only for this upload route. Metadata and control bodies
are independently bounded. Protect any reverse-proxy upload-buffer directory.

Ordinary worker PDF HTTP requests use public-IP validation and DNS-pinned dialing.
This is **not a browser sandbox**: browser/subresource and metadata-resolver
networking still require an isolated namespace/firewall preventing access to host,
private services, cloud metadata and service sockets. See worker deployment notes.
Do not run the browser in host networking or expose Docker/database credentials.

## Migration and rollback

Keep the old proxy available during validation. Deploy one outbound worker with a
fresh persistent volume; enable the fleet on the master with the legacy proxy URL
removed, approve the node, and test authorized sample papers. Add a second node
and verify failover/capacity before retiring the old proxy. Legacy once-only token
retrieval does not inherit the new protocol's receipt guarantees.

To roll back routing, keep the **new master binary**, disable `remote`, restore
the old proxy configuration if needed, and restart. First drain nodes and finish
in-flight archives where possible. Keep the database and spool volumes so the
fleet can resume later. Do not blindly restore an older binary: the existing
schema-version guard rejects newer schemas, and migration downgrade deletes the
new task/admission state.

## Validation without publisher traffic

```sh
go test ./...
# Only a disposable PostgreSQL DB; tests create/drop their unique schemas.
TEST_DOWNLOADFLEET_DATABASE_URL='postgres://test_user:password@localhost/test_database?sslmode=disable' \
  go test -race ./internal/downloadfleet ./internal/downloadworker ./internal/registry
```

The runner/fleet end-to-end tests use an httptest master, real isolated PostgreSQL
schemas and synthetic PDFs. They cover approval, parallel capacity, failover,
lost archive ACKs, staged recovery and spool cleanup. Real Chrome/container and
institutional network acceptance must be performed separately on staging.
