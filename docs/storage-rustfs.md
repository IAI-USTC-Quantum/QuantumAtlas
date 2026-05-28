# QuantumAtlas ↔ RustFS integration

> How the Go server (`cmd/qatlas-server`) wires to RustFS (S3-compatible
> object store) for paper assets. Covers env vars, IAM policy spec,
> bucket layout, version lifecycle, the `qatlas-server storage prune`
> operator command, and known RustFS-vs-MinIO quirks.
>
> Application-level upload semantics (sha256 dedup, 409 conflict
> behaviour, `?expected_sha256=` guard) live in
> [upload-api.md](upload-api.md). Wider storage architecture (why
> we have separate Raw / Metadata / Graph layers) lives in
> [storage-design.md](storage-design.md).

## Backend selection

`internal/objstore` exposes a single `Store` interface with two
implementations:

- `LocalStore` — directory under `cfg.RawDir` (XDG default
  `~/.local/share/quantum-atlas/raw/`). Dev / first-boot / CI.
  No version concept, no presigned URLs.
- `S3Store` — RustFS / MinIO / Amazon S3, via `minio-go/v7`.
  Production.

Selection is **all-or-nothing**: setting any of the four
`QATLAS_S3_*` env vars without setting all four is a startup
error. With all four set, the server logs

```
raw store: S3 backend http://10.144.18.10:9000/qatlas-raw
```

on every boot. Without them it logs

```
raw store: local backend /home/timidly/.local/share/quantum-atlas/raw
```

The split is in `cmd/qatlas-server/main.go::initRawStore` and the
all-or-nothing rule is enforced by
`internal/config/config.go::validateS3Config`.

## Required env vars

| Var                              | Example                              | Notes                                                                                 |
| -------------------------------- | ------------------------------------ | ------------------------------------------------------------------------------------- |
| `QATLAS_S3_ENDPOINT`             | `http://10.144.18.10:9000`           | Must include scheme. Production prefers mesh-direct (avoids edge-Caddy self-loop).    |
| `QATLAS_S3_BUCKET`               | `qatlas-raw`                         | Must exist; bootstrap script creates it idempotently.                                 |
| `QATLAS_S3_ACCESS_KEY_ID`        | `CNEDAZ2HQDU9TX8A2BUO`               | Service-account key (`qatlas-server` IAM user). Never use root keys here.             |
| `QATLAS_S3_SECRET_ACCESS_KEY`    | `…`                                  | Secret printed once by bootstrap; copy directly into `.env` (mode 600).               |

## IAM policy: `qatlas-raw-rw`

The `qatlas-server` IAM user is bound to this policy (created by
`scripts/rustfs_bootstrap.sh`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:GetObjectVersion",
        "s3:DeleteObjectVersion"
      ],
      "Resource": "arn:aws:s3:::qatlas-raw/*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:ListBucketVersions",
        "s3:GetBucketLocation",
        "s3:GetBucketVersioning",
        "s3:PutBucketVersioning"
      ],
      "Resource": "arn:aws:s3:::qatlas-raw"
    }
  ]
}
```

What each permission is for:

| Action                                   | Why qatlas needs it                                                                                                    |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `s3:GetObject` / `s3:PutObject`          | Routine PDF / markdown / JSON I/O via the upload handlers.                                                             |
| `s3:DeleteObject`                        | Soft-delete via the (currently unimplemented) `DELETE /api/papers/*` route + admin cleanup.                            |
| `s3:GetObjectVersion`                    | Reading a specific past version (for future rollback CLI; not yet exposed in HTTP).                                    |
| `s3:DeleteObjectVersion`                 | **Required by `qatlas-server storage prune --yes`** — versioned deletes are a separate AWS perm from `s3:DeleteObject`. |
| `s3:ListBucket` / `s3:GetBucketLocation` | minio-go probes the endpoint and walks prefixes (e.g. enumerate-needs-mineru).                                         |
| `s3:ListBucketVersions`                  | Powers `ObjectVersion`-aware listing — backs `qatlas-server storage prune` enumeration.                                 |
| `s3:GetBucketVersioning` / `s3:PutBucketVersioning` | Lets qatlas self-manage versioning at boot (see "Versioning" below).                                                  |

**Deliberately not granted** (re-test before adding):

- `s3:DeleteBucket`, `s3:PutBucketPolicy`, `s3:PutBucketAcl` —
  bucket destruction / ACL change should stay root-only ops; qatlas
  has no use case.
- `s3:GetLifecycleConfiguration`, `s3:PutLifecycleConfiguration` —
  **RustFS 1.0.0-beta.5 rejects these action names** with
  `invalid action`. Re-test when bumping RustFS; until then qatlas
  doesn't use lifecycle rules anyway (see "Why no auto-expiration"
  below).

## Bucket layout

Object keys are constructed by `internal/paperassets.AssetKey` as

```
<kind>/<arxiv-id-prefix>/<arxiv_id>v<n>.<ext>
```

with `<arxiv-id-prefix>` being the first 4 chars of the YYMM segment
(e.g. `2501` → `pdf/2501/2501.00010v1.pdf`) so a flat list of papers
shards naturally into year-month folders, keeping individual prefix
listings manageable.

| Kind       | Path                                  | Content-Type                         |
| ---------- | ------------------------------------- | ------------------------------------ |
| `pdf`      | `pdf/<prefix>/<id>v<n>.pdf`           | `application/pdf`                    |
| `json`     | `json/<prefix>/<id>v<n>.json`         | `application/json`                   |
| `markdown` | `markdown/<prefix>/<id>v<n>.md`       | `text/markdown; charset=utf-8`       |

User metadata always includes `x-amz-meta-sha256` (lowercase) with
the hex digest of the bytes — see [upload-api.md](upload-api.md).
This is the field `qatlas-server storage prune` and the upload handler
both rely on for idempotency / dedup.

## Versioning: qatlas self-manages

`internal/objstore/s3.go::EnsureVersioning` is called once at server
boot, right after `initRawStore`. Pattern:

```
GetBucketVersioning(bucket)
    if Status == "Enabled" → log "already enabled", no-op
    else                   → EnableVersioning(bucket), log "enabled (was: <prior>)"
```

This is **idempotent** and **monotonic**: qatlas only ever
transitions to `Enabled`, never to `Suspended`. Even if an operator
manually suspends versioning via mc, the next qatlas restart
re-enables it. Rationale: losing the ability to recover an
over-written PDF is a much bigger correctness hazard than the
(small) extra storage cost.

Boot log lines you should always see (in this order):

```
raw store: S3 backend http://10.144.18.10:9000/qatlas-raw
bucket versioning: enabled (was: "")           ← first boot ever
bucket versioning: already enabled              ← every subsequent boot
Server started at http://127.0.0.1:4200
```

Failure mode: if the IAM user lacks `s3:Put/GetBucketVersioning`,
EnsureVersioning logs `WARN bucket versioning: reconcile failed; …`
and the server **continues to serve**. Uploads still work; you only
lose overwrite-rollback safety until perms are fixed. This is a
deliberate warn-and-continue choice — bouncing the whole server
because of a non-critical config drift is worse than degrading.

## Why no auto-expiration (lifecycle)

We **deliberately do not install an S3 lifecycle rule** to
auto-expire noncurrent versions. The model is "Synology Snapshot /
Time Machine": keep everything by default, prune on demand.

Reasoning:

- sha256 dedup already short-circuits identical re-uploads (no
  wasted version), so the noncurrent versions we accumulate are
  real content changes — worth holding onto for rollback.
- Auto-expiration windows are operationally fraught: pick 30d and
  you regret it the day someone needs to restore a 6-week-old
  draft; pick 365d and the cost picture matters again.
- The ops side has full visibility + control via `qatlas-server
  storage prune` (see next section), so manual policy is just as
  good in our scale regime.

When (if ever) the bucket grows past a few hundred GB of noncurrent
versions, revisit. RustFS may by then support the standard
`s3:*LifecycleConfiguration` actions and we can add a rule.

## `qatlas-server storage prune`

The on-server CLI for manual cleanup. Lives in
`cmd/qatlas-server/storage_cmd.go`; runs against whatever the server's
own env vars say (`QATLAS_S3_*` from the same `.env` qatlas reads at
boot).

```
qatlas-server storage prune [--prefix P]
                           [--older-than DUR]
                           [--keep-last N]
                           [--yes]
                           [--json]
                           [--dry-run]      # default true
```

Flags:

| Flag             | Effect                                                                                                                                                                                                                          |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--prefix`       | scope to keys under this prefix (e.g. `pdf/2511/2511.00010v1.pdf` for a single paper, `pdf/2511/` for a cohort). Default empty = whole bucket.                                                                                  |
| `--older-than`   | only versions older than this duration are eligible. Accepts Go duration syntax (`24h`, `720h`) plus operator-friendly `d` / `w` / `y` units (`30d`, `4w`, `1y`). Default empty = no age cap.                                   |
| `--keep-last N`  | per object key, keep the N most-recent noncurrent versions, only delete those beyond that count. Current version is ALWAYS kept regardless. Default 0 = no per-key cap.                                                         |
| `--yes`          | actually delete. Without it, the run is forced dry (regardless of `--dry-run`).                                                                                                                                                 |
| `--json`         | emit one JSON object per row on stdout (audit-log friendly).                                                                                                                                                                    |
| `--dry-run`      | preview only. Defaults to true; `--yes` is the only way to actually delete.                                                                                                                                                     |

Hard safety invariants (enforced by `planPruneCandidates` + unit
tested in `cmd/qatlas-server/storage_cmd_test.go`):

- **Current (latest) versions are NEVER deleted.** No flag combination
  can override this.
- **Latest delete markers are NEVER deleted.** Deleting one would
  resurrect the prior version, which is almost never what the
  operator wants.
- Filters compose. A version must satisfy BOTH `--older-than` and
  fall outside `--keep-last` to be pruned. So `--older-than 90d
  --keep-last 5` means "keep at least 5 most-recent noncurrent per
  key, plus drop anything younger than 90d even if it's beyond
  the keep-last cap".

### Recipes

```bash
# Audit pass: list every noncurrent in the bucket (no deletes)
sudo -u timidly $TARGET storage prune

# Cohort cleanup: drop all noncurrent for one paper, keep current
sudo -u timidly $TARGET storage prune \
    --prefix pdf/2501/2501.00010v1.pdf --yes

# Tightening retention: per paper, keep at most 5 noncurrent
sudo -u timidly $TARGET storage prune --keep-last 5 --yes

# Age-based: drop anything noncurrent for > 1 year
sudo -u timidly $TARGET storage prune --older-than 1y --yes

# Machine-readable for an audit log
sudo -u timidly $TARGET storage prune --json | tee prune-$(date +%F).log
```

`$TARGET` = the qatlas binary (`/home/timidly/.local/bin/qatlas-server`
on the production deploy). Run as the `timidly` user (the systemd
unit's `User=`) so the env / file paths resolve identically to the
running server.

### Output format

Plain dry-run / preview:

```
KEY                        VERSION_ID                            SIZE   AGE     ACTION
pdf/2511/2511.88888v1.pdf  5f14251f-8b00-4be4-a0d1-e5ff592a8f89  92826  20m7s   DELETE_PLANNED
pdf/2511/2511.88888v1.pdf  69537cbf-2035-4aa2-8ec3-4fc8dca357a6  92812  20m15s  DELETE_PLANNED
---
candidates: 2 versions, 0.18 MiB total
dry-run only — pass --yes to delete the listed versions
```

`--yes` adds per-row deletion lines:

```
pdf/2511/2511.88888v1.pdf @5f14251f-8b00-4be4-a0d1-e5ff592a8f89 DELETED
pdf/2511/2511.88888v1.pdf @69537cbf-2035-4aa2-8ec3-4fc8dca357a6 DELETED
---
deleted: 2, failed: 0, freed: 0.18 MiB
```

## Bootstrap (initial RustFS setup)

`scripts/rustfs_bootstrap.sh` is idempotent and creates everything
the server expects: bucket `qatlas-raw`, IAM user `qatlas-server`,
policy `qatlas-raw-rw`, and one fresh service-account key pair.

```bash
export RUSTFS_ENDPOINT=https://raw.quantum-atlas.ai     # public, root-creds path
export RUSTFS_ROOT_ACCESS_KEY=<root_ak>
export RUSTFS_ROOT_SECRET_KEY=<root_sk>
bash scripts/rustfs_bootstrap.sh
```

Last few lines of stdout print the new access key + secret. Copy
into the server's `.env` immediately — they are NEVER persisted
to disk by the script. Bootstrapping a second time creates an
*additional* service-account key (existing keys are not rotated /
deleted) — useful for key rotation, see the script's own comments.

Local variable naming: the script uses `IAM_USER` (not `USER`)
internally. `$USER` is auto-set in every interactive shell to the
login user, so `${USER:-qatlas-server}` would never fall through to
the default. Setting `IAM_USER=…` from the environment if you want
to bootstrap a non-default IAM user.

## Troubleshooting

### "Access Denied" on upload but versioning works at boot

Probably the IAM user record got deleted (RustFS quirk) while the
service-account key remained. Symptoms:

- `mc admin user info qatlas qatlas-server` → "user does not exist"
- `mc admin user svcacct ls qatlas qatlas-server` → still shows your key
- Server boots fine (versioning Get/Put succeed somehow)
- Upload returns `500 {"detail": "stat …: objstore: stat …: Access Denied."}`

Recovery:

```bash
RAND_PW=$(openssl rand -base64 24)
mc admin user add    qatlas qatlas-server "$RAND_PW"
mc admin policy attach qatlas qatlas-raw-rw --user qatlas-server
# verify
mc admin user info qatlas qatlas-server  # should now show PolicyName
```

Existing service-account keys re-associate with the recreated user
record. You do NOT need to regenerate credentials or restart qatlas.

### `policy create` succeeded but svcacct still 403

Cache. RustFS 1.0.0-beta.5 has a short policy-eval cache. Wait
~30s and retry. If still 403, double-check policy JSON via
`mc admin policy info qatlas qatlas-raw-rw` — sometimes mc reports
"created" but the JSON didn't apply (we hit this with `s3:*Lifecycle*`
action names, see "Deliberately not granted" above).

### `storage prune --yes` fails with "Access Denied" on delete

The policy is missing `s3:DeleteObjectVersion` (versioned delete is
a different AWS perm from `s3:DeleteObject`). Update the policy via
mc + re-run prune. Bootstrap script already grants it correctly
since 2026-05-28.

### Boot log says `bucket versioning: reconcile failed`

The IAM user lacks `s3:Put/GetBucketVersioning`. Fix the policy (see
"IAM policy" section). Server continues to run without rollback
safety until the policy is fixed and the server restarts (or
EnsureVersioning runs again on next boot).

### `s3:GetLifecycleConfiguration` errors with "invalid action"

Known RustFS 1.0.0-beta.5 limitation. Don't grant lifecycle perms
to the IAM user. We don't use lifecycle anyway (see "Why no
auto-expiration"). Revisit when bumping RustFS.

## Recovery walk-through: rolling back an overwritten PDF

```bash
# Find versions of the paper
mc ls --versions qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf

# Output:
# [2026-05-28 14:27:33 +08]  90KiB STANDARD <new-vid> v2 PUT 2501.00010v1.pdf
# [2026-05-28 14:27:14 +08] 689KiB STANDARD <old-vid> v1 PUT 2501.00010v1.pdf

# Restore v1 by copying it as the new current
mc cp --version-id <old-vid> \
    qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf \
    qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf
```

The server's next GET for that key serves the restored bytes. No
restart needed. The over-written v2 becomes noncurrent (but is
still recoverable until `storage prune` decides otherwise).

## Related docs

- [upload-api.md](upload-api.md) — request/response shape, sha256
  semantics, in-transit guard from the client's perspective.
- [storage-design.md](storage-design.md) — wider architecture (why
  Raw / Metadata / Graph are separate layers).
- [deployment.md](deployment.md) — systemd unit, .env layout,
  RackNerd / Alibaba edge topology.
