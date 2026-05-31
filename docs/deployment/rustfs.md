# QuantumAtlas ↔ RustFS integration

> How the Go server (`cmd/qatlas-server`) wires to RustFS (S3-compatible
> object store) for paper assets. Covers env vars, IAM policy spec,
> bucket layout, version lifecycle, the `qatlas-server storage prune`
> operator command, and known RustFS-vs-MinIO quirks.
>
> Application-level upload semantics (sha256 dedup, 409 conflict
> behaviour, `?expected_sha256=` guard) live in
> [upload-api.md](../reference/upload-api.md). Wider storage architecture (why
> we have separate Raw / Metadata / Graph layers) lives in
> [storage-design.md](../concepts/storage-architecture.md).

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
| `QATLAS_S3_PUBLIC_ENDPOINT` (可选) | `https://raw.quantum-atlas.ai`      | 公网入口，给 client presigned URL 用；留空 = 单 endpoint 模式（仅适合 dev）|

### Dual-endpoint mode { #dual-endpoint }

生产部署里 server↔RustFS 走**mesh / 内网**（省一跳反代 + TLS 终结），但发给 client 的 share URL 必须**公网可达**。两者用同一份 endpoint 显然不行——所以 qatlas server 支持 dual-endpoint：

| 用途 | 走哪个 endpoint |
|---|---|
| server 内部 Put/Get/Stat/List | `QATLAS_S3_ENDPOINT`（internal） |
| 给 client presign URL（share / 直接下载）| `QATLAS_S3_PUBLIC_ENDPOINT`（public） |

启用方法：在 `.env` 同时设两个：

```bash
QATLAS_S3_ENDPOINT=http://10.144.18.10:9000           # mesh 内网
QATLAS_S3_PUBLIC_ENDPOINT=https://raw.quantum-atlas.ai # 公网（独立子域）
```

**公网入口必须反代到内网 RustFS 端口，且 `preserve Host header`**——SigV4 把 Host 算进 canonical request，反代改 Host 会让 RustFS 报 `SignatureDoesNotMatch`。最小 Caddy 模板：

```caddy
raw.quantum-atlas.ai {
    reverse_proxy 10.144.18.10:9000 {
        header_up Host {host}
    }
}
```

详见 [反向代理](reverse-proxy.md)。

启动 log 区分两种模式：

```
raw store: S3 backend http://10.144.18.10:9000/qatlas-raw (presign via https://raw.quantum-atlas.ai)
```

少了 `(presign via ...)` 那段就是单 endpoint 模式。

每台边缘各自配自己的 public endpoint，**不共享**：

- RackNerd: `https://raw.quantum-atlas.ai`（LE 真证书）
- 阿里云: `https://47.102.36.175:9000`（`tls internal` 自签，client 必须 `-k`）

详见 [多边缘部署](../concepts/multi-edge.md)。

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
the hex digest of the bytes — see [upload-api.md](../reference/upload-api.md).
This is the field `qatlas-server storage prune` and the upload handler
both rely on for idempotency / dedup.

## Versioning: qatlas self-manages { #versioning }

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

## `qatlas-server storage prune` { #prune }

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

## Bootstrap (initial RustFS setup) { #bootstrap }

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

## 写入留痕 audit sink (T10) { #写入留痕-audit-sink-t10 }

**问题**：S3 svcacct key 一旦泄露，持有者能绕过 `qatlas-server` API 直连桶
写/删对象。我们要能在日志里**看到**这种直连，并区分它和正规 server 写，
且**跨 edge 一致**（两台 edge 共享 RustFS，审计要落在一处）。

**方案**：RustFS 原生 audit（逐请求记 `userAgent` / `remotehost` /
`req_header`(含 SigV4 `accessKey`) / `api.bucket` / `event` / `request_id`，
**服务器全局**覆盖所有桶所有身份）→ localhost webhook → **Fluent Bit sidecar**
→ 批量写进 `qatlas-audit` 桶。sink 刻意选**通用、零后端约定**的日志转发器
（Fluent Bit，CNCF Graduated 项目）作为 sidecar，**不碰我们的 binary**——dumb
存储层不该被后端演进中的约定（审计 JSON 解析、桶布局、过滤逻辑）绑死；我们每
`cz bump` 一次也不该逼 NAS 跟着换 sink 镜像。Go server 唯一参与的是
`QATLAS_EDGE_NAME` 打的 UA 标（见下，纯辅助标识）。

### 取证判定（主键 = accessKey，不是 UA）

- `accessKey` = root（`TiMidlY`）→ 直接点名误用 root（SigV4 绑定，不可伪造，强信号）。
- `accessKey` ≠ 任何预期 svcacct（既非 edge 写 key、也非 sink 自己）→ 有人拿别的 key 直连。
- `remotehost` 非预期网段 → 佐证。
- **UA 只作辅助提示，绝不作判定主键**——UA 可伪造，靠 UA 判定的话攻击者把 UA
  伪装成 `qatlas-server/*` 就隐身了。`QATLAS_EDGE_NAME` 打的 UA 标
  （`qatlas-server/<ver>/<edge>`）只是让正规写在审计流里"一眼可读"，不是安全边界。
  注意：两台 edge **共享同一把 svcacct key**，光看 `accessKey` 分不出是哪台 edge
  写的——这正是 UA edge 标唯一的用处（要它生效得在每台 edge `.env` 设 `QATLAS_EDGE_NAME`）。

### 自循环陷阱

sink 把审计写进 `qatlas-audit` 桶，这个 PUT 本身又触发一条 audit → sink 再写 →
无限循环。**解法：Fluent Bit 用 `grep` filter drop 掉 `api.bucket == qatlas-audit`
的事件**（纯配置、零代码、最 dumb 的正确过滤）。被 drop 的事件 RustFS 侧已交付
成功，不影响 durability。

> ⚠️ **sink 仍用独立 svcacct（`qatlas-audit-sink`），不复用 edge 的
> `QATLAS_S3_ACCESS_KEY_ID`**——理由是**最小权限 + 审计不可变**：sink 只拿
> `qatlas-audit` 桶的 Get/Put/List，**没有 Delete**（审计落了删不掉），也碰不到
> 三个资产桶。复用 edge key 既越权、又（若改用 accessKey 过滤断环时）会把正规
> edge 写一并 drop 掉——审计里恰恰没了你最想记的那些写。

### 供给（用户持 root 跑一次）

```bash
# RustFS root key 在 NAS compose env 里，agent 不持有。用户跑：
export RUSTFS_ENDPOINT=http://10.144.18.10:9000
read -rs RUSTFS_ROOT_ACCESS_KEY; export RUSTFS_ROOT_ACCESS_KEY   # = compose RUSTFS_ACCESS_KEY
read -rs RUSTFS_ROOT_SECRET_KEY; export RUSTFS_ROOT_SECRET_KEY   # = compose RUSTFS_SECRET_KEY
bash scripts/rustfs_audit_bootstrap.sh
```

脚本幂等：建 `qatlas-audit` 桶（无 versioning，审计对象 write-once）+
`qatlas-audit-rw` policy（Get/Put/ListBucket，**故意不给 Delete** = 审计不可变）+
`qatlas-audit-sink` user/svcacct + `qatlas-audit-ro` 只读 policy 挂到现有
`qatlas-server` 父用户（edge svcacct 继承读，给未来 Go 侧对账/扫描预留只读）。
**只打印 sink 的 access/secret**，root 不落盘——跟 `rustfs_bootstrap.sh` 供给 edge
svcacct 同款套路（agent 全程只见 scoped key，没见过 root）。

### sink = Fluent Bit sidecar（NAS compose）

sink 的 svcacct key、桶名、过滤规则**全在 NAS 侧 Fluent Bit 配置里**，与
qatlas-server `.env` 完全解耦。一个 `fluent/fluent-bit:<钉版本>` 容器：HTTP input
收 webhook、`grep` filter 断自循环、S3 output 批量落 `qatlas-audit`（配置示意，
字段路径以 RustFS 实际 audit JSON 为准，部署时定稿）：

```ini
[INPUT]
    Name     http
    Listen   0.0.0.0
    Port     8080

[FILTER]
    Name     grep
    Match    *
    Exclude  $api['bucket'] ^qatlas-audit$     # 断自循环

[OUTPUT]
    Name             s3
    Match            *
    endpoint         http://rustfs:9000        # NAS docker 网内，service 名解析
    bucket           qatlas-audit
    region           us-east-1
    total_file_size  5M
    upload_timeout   1m
    s3_key_format    /%Y-%m-%d/$UUID.json
    store_dir        /var/log/fluent-bit-buffer  # 本地缓冲，S3 挂时不丢
    # AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY = bootstrap 输出的 sink key（compose env）
```

durability 两层兜底：RustFS audit webhook 自带磁盘队列（sink 挂时缓冲重放），
Fluent Bit S3 output 自带 filesystem buffer（RustFS 写挂时缓冲）。

### RustFS 侧（NAS compose，纯 env）

audit webhook 纯 env 开，**无需 mc / init 容器**（audit 是服务器全局，不像 notify
按桶 `mc event add` 订阅）：

```yaml
RUSTFS_AUDIT_WEBHOOK_ENABLE: "on"
RUSTFS_AUDIT_WEBHOOK_ENDPOINT: "http://fluent-bit:8080/"   # 指向 Fluent Bit HTTP input
RUSTFS_AUDIT_WEBHOOK_AUTH_TOKEN: "<webhook 共享密钥>"        # Fluent Bit 侧校验
RUSTFS_AUDIT_WEBHOOK_QUEUE_DIR: "/data/audit-queue"   # durability：sink 重启窗口缓冲重放
RUSTFS_AUDIT_WEBHOOK_QUEUE_LIMIT: "10000"             # 上限钉死磁盘
```

> ⚠️ NAS 是 Synology DSM，compose 编辑 + Fluent Bit sidecar service + 容器 down/up
> **只能在 DSM GUI 完成**（ssh 用户不在 docker 组、sudo 要交互密码）。agent 写好
> compose 片段交用户在 DSM 里粘贴 + down/up。

### 对象布局

Fluent Bit S3 output 把多条事件**批量**攒成时间分区的 NDJSON 对象
（`/%Y-%m-%d/<uuid>.json`，可选 gzip），每次 upload 是一个**全新不可变对象**——
S3 无 append，但这里根本不需要 append（不是 read-modify-write 同一文件，没有并发
丢行问题；Fluent Bit 的 disk buffer 负责攒批 + 崩溃重放）。读取：
`mc cat qatlas-audit/<date>/*.json | jq`。


## Related docs

- [upload-api.md](../reference/upload-api.md) — request/response shape, sha256
  semantics, in-transit guard from the client's perspective.
- [storage-design.md](../concepts/storage-architecture.md) — wider architecture (why
  Raw / Metadata / Graph are separate layers).
- [deployment.md](operations.md) — systemd unit, .env layout,
  RackNerd / Alibaba edge topology.
