# Durable outbound download fleet

## Parent integration

```go
fleet, err := downloadfleet.New(pool, downloadfleet.Config{
    Enabled: true,
    SpoolDir: "/durable-volume/downloadfleet",
    SpoolMaxBytes: 2 << 30,
})
// Handle err before continuing.
fleet.SetArchive(dl.ArchiveRemote)
fleet.SetHooks(dl.AfterRemoteArchive)
go fleet.Start(applicationContext)
// Mount fleet.WorkerHandler() at workerprotocol.BasePath + "/*".
// Fleet implements downloader.Remote via Enabled and FetchPDF.
```

The worker handler authenticates its own independent worker bearer credentials.
Do **not** wrap it in administrator bearer/session authentication. Conversely,
`Enrollment`, `Snapshot`, and `Action` are privileged service methods: the parent
must protect the admin routes. No API key or administrator password is a worker
credential. Deploy worker HTTP endpoints behind TLS.

Worker wire types and exact routes live in `internal/workerprotocol/protocol.go`.
Registration retries use the same client-persisted random ID and secret; only
SHA-256 credential/enrollment hashes are stored. Pending nodes can only read
status. Rejected/revoked identities cannot be re-enabled: enroll a fresh identity.
Draining nodes finish existing assignments but claim no new work.

## Durability and limits

* PostgreSQL owns tasks, node approval/health, attempts, lease/deadline fencing,
  spool reservations, receipts and a post-archive hook outbox. Persistent parent
  admission IDs are linked to tasks so journal replay observes the same terminal
  success/failure instead of accidentally resetting the worker-attempt budget.
  An explicit user retry must supply a fresh admission ID.
* Defaults: global concurrency 6, per-worker concurrency 2, distinct-worker
  attempts 3, task deadline 15m, attempt execution deadline 6m, renewable lease
  60s, independent upload phase 2m, enrollment lifetime 15m, retained history
  7 days, PDF cap 100 MiB.
  Positive overrides are supported. Separate safety ceilings are 32 attempts,
  24h task, 1h execution, 5m lease and 10m upload; lease <= execution <= task.
* Claims serialize across coordinators with a PostgreSQL advisory lock; row locks
  fence current attempts. Capacity advertised by a worker never exceeds the
  server's per-worker hard cap. Tried workers are excluded for that task.
* Uploads reserve declared bytes before writing, stream into an exclusive 0600
  file, verify exact size and SHA-256, check PDF magic/minimum size/EOF, fsync file
  and directory, then durably stage. Task/attempt locks protect live transfers
  against expiry and deletion. Upload HTTP has a read deadline and body cap.
  A first upload must start during the execution lease, then gets a separate
  task-bounded upload lease. Interrupted/restarted transfers can retry the same
  attempt using generation-fenced reservations without extending that deadline.
  Upload heartbeats read this fixed lease without locking the active transfer,
  so other attempts can continue renewing.
* Archive success precedes the `done` transaction and receipt. The receipt stores
  the **uploaded** SHA/size even when storage kept an existing equivalent asset.
  Only matching `done` receipts authorize worker-side deletion; GET is read-only.
* Configure both callbacks before serving. Archive and hooks **must be idempotent**:
  a crash between an external side effect and the PostgreSQL commit can repeat
  it. Hooks use a 30s callback budget and retry with bounded exponential delay,
  at most 20 deliveries; exhausted
  failure is visible in the job error. No exactly-once external-side-effect claim.
* Archive callbacks receive a file-backed reader, not a PDF-sized allocation;
  restart recovery re-hashes and independently validates the file, rewinds it,
  then keeps it open for the synchronous callback.
* Restart recovery retries staged commits. Caller cancellation/deadline stops its
  wait without cancelling the durable task. `FetchPDF` returns
  `downloader.ErrRemotePending` plus `FetchOutcome.Pending=true` on ambiguous DB
  errors or detached/expired waits, never a terminal failure just because the
  caller stopped waiting. Terminal reads precede stored-deadline checks. Archive
  and hook callbacks carry the latest linked `downloader.AdmissionID(ctx)` so
  parent journal completion can fence against a newer explicit request.
  Staged commits can complete beyond
  the execution deadline; a persistently uncommittable staged file is failed and
  cleaned after retention. No worker can reassign an accepted staged result.
* Cleanup is bounded in batches, excludes live reservations, cleans terminal
  spool files before deleting rows, and retains receipts until task retention.
  Admin snapshots return at most 500 workers and 500 jobs. Failure traces cap at
  64 entries per attempt and bounded field lengths; success metadata caps at 16KiB.

Use a **persistent shared spool directory** when multiple coordinators serve the
same database, including identical absolute paths on every replica. This backend
is not a distributed blob spool. Archive and hook callbacks hold a task transaction
while making external calls. Production main should provide a **dedicated fleet
pool** to the same database/schema, separate from the registry callback pool,
with headroom (recommended >= 2*MaxInFlight+8 connections). If embedding with one
shared pool, provision equivalent callback headroom to prevent pool starvation.
Callbacks must obey their contexts. Worker leases remain bounded rather than
being resurrected after expiry.

## Testing

`go test ./internal/downloadfleet ./internal/workerprotocol` runs unit tests.
Integration tests are opt-in through `TEST_DOWNLOADFLEET_DATABASE_URL` and require a
**disposable PostgreSQL database** where the test user can create schemas. Each test
creates a random isolated schema, executes only this package's migration Up there,
and drops the schema afterward; it never touches the app's public tables or goose
migration bookkeeping. They cover enrollment/approval, claim caps and retries,
lease fencing, invalid uploads, staged restart recovery, receipts and hook delivery.
