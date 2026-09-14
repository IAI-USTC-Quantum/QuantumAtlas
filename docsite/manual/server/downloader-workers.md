# Outbound downloader workers

For master configuration, node approval, archive semantics and migration, see
[Multi-worker downloader: master setup](downloader-fleet.md).

## Deployment runner

`downloaderworker` is an **outbound-only** worker for the v2 fleet API at
`/api/downloader/workers/v2`. Run it on a machine whose lawful/institutional egress
can access the desired papers. It invokes the shared `downloader.FetchPDF` ladder;
remote proxy/fleet delegation and agent execution are not enabled inside workers.
The existing `downloaderproxy` image and root proxy configuration are unchanged.

### Container deployment

```sh
docker build -f Dockerfile.downloaderworker -t qatlas-downloaderworker .
docker volume create qatlas-worker-data
# worker.env contains DL_WORKER_MASTER_URL=https://qatlas.example.org,
# DL_WORKER_ENROLLMENT_TOKEN=<enrollment credential>, DL_WORKER_NAME=campus-01,
# and optionally DL_UNPAYWALL_EMAIL / DL_S2_API_KEY. Protect it with chmod 600.
docker run -d --name qatlas-downloaderworker --restart unless-stopped \
  --stop-timeout 30 --shm-size 256m \
  --env-file worker.env \
  -v qatlas-worker-data:/var/lib/qatlas-downloader \
  qatlas-downloaderworker
```

**Do not publish any ports** (`-p`/`-P` are unnecessary). The runner has no HTTP
listener. Its sole local browser supervisor binds CDP to `127.0.0.1:9222` inside
the container; CDP is not an administration or public API. The entrypoint only
`exec`s the Go runner and never starts a second browser. It prefers headless-shell
and omits full Chrome's `--headless=new` flag for that binary. Unexpected browser
exit triggers a delayed restart. SIGTERM cancels download contexts, stops and
reaps the owned Chrome process group, and leaves durable results for recovery.
An externally managed browser is not terminated by this worker.

The image runs as UID/GID 65532. Named volumes acquire the image directory's
ownership; a bind mount must already be writable by that UID. Mount **one unique
persistent data volume per worker**. Do not share/copy a worker identity across
concurrently running containers. A volume lock rejects duplicate runners.
The data volume includes a plaintext independent worker secret (files mode 0600,
new directories mode 0700); restrict access and protect backups. Reusing the
volume with a different master URL is rejected to avoid credential disclosure.
Do not store the data directory on an ephemeral container filesystem.

After first registration, the worker remains **pending** until an administrator
approves it on the master. Its random ID and random 256-bit secret are committed
to the volume **before** registration; retries reuse that identity, including
recovery after a lost registration response. The master hashes the worker secret.
Subsequent requests use only that worker's bearer credential, not the enrollment
credential. Once enrollment succeeds, the enrollment env can be removed on the
next restart without changing the volume. Never place credentials in URL query
parameters. All redirects are rejected (including same-host redirects), preventing
bearer forwarding; configure the final master origin directly.

### Configuration

Flags override their corresponding environment defaults. Credential variables
are env-only so `--help` cannot print secret defaults.

| Environment variable | Flag | Default / meaning |
| --- | --- | --- |
| `DL_WORKER_MASTER_URL` | `--master-url` | Required HTTPS master origin; optional reverse-proxy path prefix |
| `DL_WORKER_ENROLLMENT_TOKEN` | env-only | Required for first enrollment, unnecessary once registered |
| `DL_WORKER_NAME` | `--name` | Hostname; descriptive operator label |
| `DL_WORKER_DATA_DIR` | `--data-dir` | `/var/lib/qatlas-downloader`; durable mounted volume |
| `DL_WORKER_CONCURRENCY` | `--concurrency` | **2**, validated range 1–32; enforced by admission and active tasks |
| `DL_WORKER_MAX_SPOOL_BYTES` | `--max-spool-bytes` | `10737418240` (10 GiB PDF quota), integer bytes; minimum 100 MiB |
| `DL_WORKER_RESULT_TTL` | `--result-ttl` | `24h`, Go duration syntax, positive |
| `DL_BROWSER_CDP_URL` | `--browser-cdp-url` | Optional external CDP; disables local browser spawning; use a trusted private endpoint |
| `DL_BROWSER_BINARY` | `--browser-binary` | Auto-detect headless-shell, google-chrome, chrome, chromium; image sets `/headless-shell/headless-shell` |
| `DL_UNPAYWALL_EMAIL` | `--unpaywall-email` | Optional Unpaywall/OpenAlex contact email |
| `DL_S2_API_KEY` | env-only | Optional Semantic Scholar API key |
| — | `--allow-http` | False; explicit opt-in **only** for trusted local development |
| — | `--task-timeout` | `6m`, aligned with the default master worker-execution budget; also bounded by assignment deadline and renewed lease |
| — | `--healthcheck` | Read local health status and exit; never opens a listener |

Production HTTPS is mandatory. `--allow-http` sends credentials in plaintext and
must not be used over an untrusted network. Standard TLS certificate validation
is never disabled. Control requests have a 20-second overall deadline; uploads
have a 3-minute deadline. Non-2xx, malformed, oversized and redirect responses are
failures, never acknowledgements. Failed control/upload operations retry on a
10-second polling cadence, with uploads independent of heartbeats. The master
client may use standard process HTTP(S) proxy environment settings and may target
a private HTTPS master; the repository's root proxy configuration is unchanged.

Ordinary PDF/landing-page and arXiv HTTP fetches use a separate public-network
client: only HTTP(S) URLs without userinfo are accepted, each DNS answer is
checked for loopback/private/link-local/multicast/reserved destinations, and the
connection dials the validated numeric address (no second DNS lookup). The same
checks apply after redirects. Mixed public/private DNS answers are rejected.
This fetch client bypasses environment HTTP proxies, because a proxy could
resolve an otherwise-unvalidated private destination. This can intentionally
reject campus-only private-address PDF hosts; there is no unsafe bypass flag.

**This is not a complete browser/network SSRF sandbox.** Chromium navigation,
JavaScript subresources and metadata-resolver clients are separate networking
paths. Deploy the worker/browser in an isolated network namespace with egress
firewall rules denying host services, RFC1918/internal networks, link-local/cloud
metadata services and other sensitive destinations, except explicitly required
master/CDP endpoints. Do not use host networking or mount host service sockets.
A public-looking URL is not sufficient protection against browser redirects,
DNS rebinding or malicious subresources. External CDP needs equivalent isolation.

### Durable results, receipts, TTL and disk pressure

A catalog records `running`, `ready`, `failed`, or `expired` attempts with durable
atomic file replacement and directory fsync. Downloaded PDFs are hashed locally,
written and synced before their `ready` catalog transition. A restart verifies
ready-file size and digest, resumes delivery, and reports prior `running` attempts
as interrupted instead of silently dropping or redownloading them. A corrupt or
missing ready PDF fails startup, allowing operator investigation rather than
silently treating a lost result as successful.

Delivery first queries the attempt receipt and then sends the raw PDF with stable
attempt ID, SHA-256, size and bounded provenance metadata. Lost responses cause
idempotent receipt checks/retries. **An HTTP 200/202 or `staged`/received receipt
is not an archived acknowledgement.** Before TTL, deletion requires a `done`
receipt matching task ID, attempt ID, digest and size: `done` means the master
has durably archived and registered the PDF. Receipt lookup is nondestructive.
Transient upload failures, uncertain responses and stale attempts retain the
local PDF until an explicit archived receipt or retention expiry.

The default **24-hour TTL starts when the result becomes ready**. At expiry the
runner first durably marks `expired`, then removes the PDF and retries a timeout
failure report to the master so the task can be rescheduled. Active upload files
are protected from expiry. If online, receipt/delivery is attempted before that
cycle's expiry cleanup; there is no guarantee an unavailable master archives a
result before TTL. Increase TTL if offline periods can exceed a day.

Admission reserves **100 MiB per active download**, accounting for ready PDFs,
existing reservations, and actual filesystem free space (plus metadata headroom).
Quota/disk pressure stops new claims; **unexpired ready results are never evicted
to make space**. Each returned PDF is bounded by the smaller of 100 MiB and the
master's assignment limit. Catalog/identity/browser-profile filesystem overhead
is outside the PDF quota; provision extra disk space. Keep the volume on a local
filesystem supporting file locking, atomic rename and fsync.

### Health and operations

`docker inspect` health runs `downloaderworker --healthcheck` against the local
`health.json`. Healthy means approved, browser reachable, a recent successful
master heartbeat, and usable capacity or currently running reserved work. Pending,
draining/revoked, browser failure, communication failure, stale (>90s) health,
and fully blocked spool are unhealthy. Thus newly enrolled containers normally
show unhealthy until approved; this is not a reason to recreate their volumes.
Container restart policy follows process exit, not health status.

Worker logs use fixed error summaries and HTTP status codes rather than tokens,
master response bodies or credential-bearing URLs. Provenance strips source URL
userinfo/query/fragment and reports generic strategy errors (up to 12 trace
entries); it intentionally does not export arbitrary downloader error strings.
No inbound endpoint or live CDP port needs firewall exposure. Restrict outbound
master access and browser/CDP administration appropriately.

### Local verification (no live downloads)

```sh
go test ./internal/downloadworker ./cmd/downloaderworker
go build ./cmd/downloaderworker
```

Unit tests use temporary directories, fake fetchers, and `httptest`; they do not
deploy containers or download real papers. The opt-in `TestPostgresOutbound*`
suite runs real runner-to-fleet HTTP flows against an in-process `httptest` master
and a uniquely named temporary schema in a **disposable PostgreSQL database**:

```sh
TEST_DOWNLOADFLEET_DATABASE_URL='postgres://test_user:password@localhost/test_database?sslmode=disable' \
  go test -run TestPostgresOutbound ./internal/downloadworker
```

The test user needs permission to create/drop schemas. Never point this at a
production database. Tests remove only their own schema on completion and never
modify the public schema or Goose migration state. Coverage includes pending
approval, complete PDF archival and spool cleanup, deliberately lost upload
acknowledgements, staged archive failure/recovery, nondestructive receipts,
two-worker shared capacity, and failure rescheduling to a second worker.
Container dependencies, real Chrome supervision, and institutional egress still
require separate operator-run staging checks with approved infrastructure.
