# REST API 总览

QuantumAtlas server 的所有 HTTP endpoint。auth 模型详见 [概念/鉴权](../concepts/auth-model.md)。

## 公开端点（不需要 Authorization 头）

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/api/health` | 健康检查 + 依赖探活 |
| `GET` | `/api/server/info` | 版本 / 引擎信息 |
| `GET` | `/install-server.sh` | qatlas-server 安装脚本 |
| `GET` | `/api/wiki/sync/status` | Wiki git 状态 |
| `POST` | `/api/wiki/sync/pull` | 触发 Wiki git fast-forward pull |
| `GET` | `/api/pages` | 列 Wiki 页面（支持 `?page_type=&status=&tags=`）|
| `GET` | `/api/pages/{page_id}` | 取单页（frontmatter + content）|
| `GET` | `/api/stats` | Wiki 统计 |
| `GET` | `/api/search?q=&limit=` | 全文搜索 |
| `GET` | `/api/lint` | Wiki lint 报告（**当前是占位**，返回空 issues）|
| `GET` | `/api/graph/stats` | Neo4j 节点 / 关系计数 |
| `GET` | `/api/graph/schema` | Neo4j label / relationship type 清单 |
| `POST` | `/api/graph/query` | 执行 Cypher（**只读**，server 端跑 `ExecuteRead`）|
| `GET` | `/api/pat/scopes` | 列 PAT scope 词表 |
| `GET` | `/api/papers/needs-mineru?limit=&include_claimed=` | 列等待 MinerU 解析的论文 |
| `GET` | `/api/papers/{arxiv_id}/resources` | 列单篇论文已有的资产 |
| `GET` | `/api/papers/{arxiv_id}/markdown` | 取论文 markdown；无缓存时由 server 用自身 MinerU token 静默后台转换（轮询）|
| `GET` | `/api/papers/{arxiv_id}/markdown/status` | 查询 markdown 转换 job 状态（无副作用，恒 200）|
| `GET` | `/share/{token}` | share token 入口 |
| `GET` | `/share/{token}/{path...}` | share token 下载 |
| `GET` | `/_/` ... | PocketBase admin UI |

## 鉴权端点（需要 PAT 或 session token）

### Papers

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/papers/{arxiv_id}/upload-pdf` | `papers:write` | 上传 PDF（+ metadata），见 [Upload API](upload-api.md) |
| `POST` | `/api/papers/{arxiv_id}/upload-markdown` | `papers:write` | 上传 markdown |
| `POST` | `/api/papers/{arxiv_id}/mineru-claim` | `papers:write` | 申请 MinerU 处理 claim |
| `DELETE` | `/api/papers/{arxiv_id}/mineru-claim/{claim_id}` | `papers:write` | 释放 claim |

### Ingest

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/ingest/paper` | `papers:write` | 提交 server-side ingest task |
| `GET` | `/api/ingest/{task_id}` | `papers:write` | 查 task 状态 |
| `POST` | `/api/ingest/{task_id}/continue` | `papers:write` | 续跑已有 task |

### Shares

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/shares/` | `shares:write` | 创建 share token |
| `GET` | `/api/shares/` | `shares:read` | 列 share |
| `DELETE` | `/api/shares/{token}` | `shares:write` | 撤销 |

### PAT 管理（**只接受 session token**，PAT auth 被拒）

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/pat` | session only | 创建 PAT，返回明文（一次）|
| `GET` | `/api/pat` | session only | 列当前用户的 PAT（无明文）|
| `DELETE` | `/api/pat/{id}` | session only | 撤销 |

## 端点详解：选粹

### `GET /api/health`

返回 PocketBase envelope 形状：

```json
{
  "code": 200,
  "message": "API is healthy.",
  "data": {
    "status": "healthy",
    "version": "0.2.8",
    "uptime_seconds": 12345,
    "time": "2026-05-29T03:00:00Z",
    "checks": {
      "rawstore": {
        "status": "ok",
        "backend": "s3",
        "endpoint": "http://10.144.18.10:9000",
        "bucket": "qatlas-raw",
        "latency_ms": 12
      },
      "neo4j": {
        "status": "ok",
        "uri": "bolt://10.144.18.10:7687",
        "database": "neo4j",
        "latency_ms": 8
      },
      "wiki": {
        "status": "ok",
        "dir": "/home/timidly/QuantumAtlas-Wiki",
        "commit": "abc123de",
        "commit_time": "2026-05-28T22:10:33Z",
        "branch": "main",
        "dirty": false
      }
    }
  }
}
```

- `data.status` 是聚合状态：`healthy`（全部 ok 或 not_configured）/ `degraded`（任一 error）
- `code` **永远 200**（即使 degraded）—— 别让上层 LB / Caddy 把整条链路 trip 成 down
- `message` 在 degraded 时变 `"Dependency degraded."`，方便 log scraper
- 每个 probe 5 秒超时（`probeTimeout`），三个并行执行
- Neo4j / wiki 不配置时返回 `"status": "not_configured"`，**不下拉聚合等级**

### `POST /api/papers/{arxiv_id}/upload-pdf`

完整流程详见 [Upload API](upload-api.md)。要点：

- multipart form 字段：`pdf` (必)、`metadata` (可选)
- query 参数：`expected_sha256=<hex>` (强烈推荐) / `expected_metadata_sha256=` / `overwrite=true`
- 状态码：
    - `201 Created` — 写了新对象
    - `200 OK` — 全部 unchanged 短路，零写入
    - `409 Conflict` — sha256 不同且没 `overwrite`，body 含 `existing_sha256` + `new_sha256`
    - `400 Bad Request` — sha256 mismatch / 损坏的 multipart / PDF header 不对等
- 并发安全（S3 conditional PUT `If-None-Match`），多 client 同字节并发只产生 1 个 201 + 其余 200

### `GET /api/papers/{arxiv_id}/markdown`

开放读，无需 auth。语义：**有缓存直接给，无缓存 server 用自身 `MINERU_API_TOKEN` 静默后台转换**，client / 网页轮询直到拿到 markdown。与 `qatlas mineru`（贡献者用自己 key 主动 claim→转→上传）并行存在、互不冲突。

- 状态码：
    - `200 OK` — `Content-Type: text/markdown; charset=utf-8`，body 即缓存的 markdown
    - `202 Accepted` — 后台转换进行中，body `{arxiv_id, status:"processing", state, started_at, status_url, detail}`；同时带响应头 `Operation-Location: /api/papers/{id}/markdown/status`（job 状态资源，Azure/Google AIP 风格）和 `Retry-After: 5`（建议的最小轮询间隔，client 在其上叠加带 jitter 的指数退避）。client 应轮询 `status_url` 而非反复打本端点
    - `404 Not Found` — `{status:"no_pdf"}`，库里没有该论文的 PDF（先 upload-pdf 才能转）
    - `502 Bad Gateway` — `{status:"failed", error}`，MinerU 转换失败（带冷却，过冷却期后再次请求会重试）
    - `503 Service Unavailable` — `{status:"unavailable"}`，server 未配置 MinerU token
    - `400 Bad Request` — arxiv_id 非法
- 转换产出的 markdown + images 会写入对象存储（images 经现有 resources / share 系列暴露）
- in-process 按 canonical arxiv_id 去重，并发请求同一论文只起一个转换 job
- **本端点 GET 带副作用**（miss 时会触发转换）；只想观测状态、不想触发转换时改用 `/markdown/status`
- client 用法见 [`qatlas markdown`](cli-qatlas.md)

### `GET /api/papers/{arxiv_id}/markdown/status`

开放读，无需 auth。**无副作用的 job 状态资源**：永不触发转换、永不要求 PDF，只汇报当前状态。因此**恒返回 `200 OK`**，转换结果在 body 的 `status` 字段（GET 状态资源本身成功 = 200；这与 content 端点失败时响亮的 502 是有意的分工）。

- body `status` ∈：
    - `done` — markdown 已就绪，`markdown_url` 指向 content 端点（命中缓存即此态，覆盖进程重启后 job map 空但 md 已存在的情况）
    - `processing` — job 排队 / 运行中，附带响应头 `Retry-After: 5`；body 含 `state`、`started_at`
    - `failed` — 上次转换失败，`error` + `finished_at` 给出原因；下次请求 content 端点会重试
    - `not_started` — 尚未发起转换（GET content 端点以触发）
    - `unavailable` — server 未配置 MinerU token
- `400 Bad Request` — arxiv_id 非法（唯一的非 200）

### `POST /api/wiki/sync/pull`

无 auth，因为 fast-forward only，最多让 Wiki 跟上 GitHub main：

```bash
curl -X POST https://<server>/api/wiki/sync/pull
```

响应：

```json
{
  "status": "ok",
  "changed": true,
  "old_commit": "abc123",
  "new_commit": "def456",
  "wiki": {"exists": true, "external": true},
  "git": {"commit": "def456", "branch": "main", "dirty": false}
}
```

非 fast-forward / 工作树脏 / dir 不存在等情况返回 409 + detail。

### `POST /api/graph/query`

只读 Cypher 执行：

```bash
curl -X POST https://<server>/api/graph/query \
  -H "Content-Type: application/json" \
  -d '{
    "query": "MATCH (a:Algorithm)-[:USES]->(p:Primitive {id: \"prim-qft\"}) RETURN a.id",
    "limit": 50
  }'
```

返回：

```json
{
  "query": "...",
  "records": [
    {"a.id": "algo-shor"},
    {"a.id": "algo-qpe-demo"}
  ]
}
```

**Neo4j 故障时返回 200 + `{"error": "..."}`**——这是有意的，让 SPA 渲染"Neo4j 不可用"banner 而不是错误页。

### `POST /api/shares/`

```json
{
  "paths": ["pdf/2501/2501.00010v1.pdf"],
  "label": "Reviewer A access",
  "expires_in": 86400
}
```

响应：

```json
{
  "token": "abc123def456...",
  "url_prefix": "https://<server>/share/abc123.../",
  "paths": ["pdf/2501/2501.00010v1.pdf"],
  "created_at": "2026-05-29T03:00:00Z",
  "expires_at": "2026-05-30T03:00:00Z",
  "label": "Reviewer A access"
}
```

### `POST /api/pat`（session only）

```json
{
  "name": "ci-upload",
  "description": "...",
  "scopes": ["papers:write", "shares:write"],
  "expires_in_days": 365
}
```

约束：
- `name` 必填，≤80 字符
- `description` ≤200 字符
- `expires_in_days` 必填，1–365
- `scopes` 必须是 `/api/pat/scopes` 返回词表里的；空集合也接受（**这个 PAT 啥都干不了**）

响应（**plaintext 只出现这一次**）：

```json
{
  "id": "abc123",
  "name": "ci-upload",
  "prefix": "qat_AB",
  "plaintext": "qat_ABXXXXX...XXXXX",
  "description": "",
  "scopes": ["papers:write", "shares:write"],
  "expires_at": "2027-05-29 03:00:00.000Z",
  "created": "2026-05-29 03:00:00.000Z"
}
```

## 错误响应规范

绝大多数错误都是 `{"detail": "<message>"}`，例外是：

| 端点 | 特殊形状 |
|---|---|
| `upload-pdf` 409 | `{detail, existing_sha256, new_sha256, existing_path, hint}` |
| `upload-pdf` 400 (sha256 mismatch) | `{detail, expected_sha256, actual_sha256}` |
| `scopeGuard` 403 | `{detail, obj, act}` |
| `graph/*` 故障 | **`200 + {error: "..."}`**（不是 5xx）|

## 速率限制

PocketBase 自带 collection 级 throttle。`/api/pat` 还挂了自定义 rate-limit 规则（默认在启动时 `pat.EnsureDefaults` 装），防止暴力 mint。常见限制：

- `/api/pat` POST: 10/minute per user
- `/api/health`: 无限
- 其他写口: 60/minute per user

具体规则在 PocketBase Settings → `_pb_users_auth_` 等 collection 的 throttle 字段。

## PocketBase 原生 collection API

除了上面 `/api/*` 自定义 endpoint，PocketBase 自己暴露 `/api/collections/<name>/records/...` 的 CRUD API。详见 [PocketBase 文档](https://pocketbase.io/docs/api-records/)。**业务上几乎不用**——所有暴露给用户的能力都通过自定义 `/api/...` endpoint 走。

例外：SPA 直接用 PocketBase JS SDK 做 OAuth 登录、读 users 自身记录等。
