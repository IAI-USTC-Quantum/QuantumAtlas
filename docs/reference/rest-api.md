# REST API 总览

QuantumAtlas server 的所有 HTTP endpoint。auth 模型详见 [概念/鉴权](../concepts/auth-model.md)。

## 交互式 API 文档（Swagger UI）

server 内嵌了一份由 [swaggo](https://github.com/swaggo/swag) 从代码注解自动生成的
OpenAPI spec，挂在 **`/swagger`**（如 <https://quantum-atlas.ai/swagger/>），可在浏览器里
直接浏览/点测每个 endpoint：

- `GET /swagger/index.html` — Swagger UI 页面（公开，无需 auth）。
- `GET /swagger/doc.json` — 原始 OpenAPI 2.0 JSON，可喂给 Postman / openapi-generator 等。

写口在 UI 里点 **Authorize** 填 `Bearer <PAT 或 session token>` 即可带鉴权调用。

> 📖 这份 spec 也嵌进了文档站：[API Explorer](api-explorer.md) 页可在浏览器里
> 展开浏览全部 endpoint 的参数 / schema（静态镜像，在线点测仍用上面的 `/swagger`）。

### spec 怎么来的（维护者须知）

PocketBase 的路由是匿名闭包，没有可挂 doc 注释的具名 handler，所以注解集中写在
[`internal/routes/openapi.go`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/internal/routes/openapi.go)
的一组 no-op stub 函数上（每个 endpoint 一个），general info 写在
`cmd/qatlasd/main.go` 的 `func main()` 之上。生成产物落在
`internal/apidocs/`（被 `main.go` blank-import，编译进二进制）。

改完注解后重新生成：

```bash
pixi run swagger        # = go tool swag init -g main.go -d ./cmd/qatlasd,./internal/routes -o internal/apidocs ...
```

swag CLI 通过 `go.mod` 的 `tool` 指令钉版本（`go tool swag`），生成是确定性的。CI
（`.github/workflows/go.yml`）跑 `pixi run swagger-check`——重新生成后 `git diff --exit-code`，
注解改了但忘 `pixi run swagger` 会直接红，保证 `internal/apidocs/` 不漂移。

> ⚠️ 注解里的 path / 参数 / 响应是**手写声明**，不是从真实闭包反射出来的——swaggo 在任何
> 非 net/http-mux 风格路由上都这样。它和真实行为的一致性靠 code review + 这份手维护的
> Markdown 表交叉验证，不是自动保证。

## 公开端点（不需要 Authorization 头）

> 仅以下"无语料数据"端点保持公开：探活 / 版本 / 安装脚本 / API 文档 / scope 词表 / SPA 外壳。**知识库本身不再匿名可读**——Wiki 页面、搜索、统计、论文资产、图谱等读口都已收敛到 `*:read` scope（见下方鉴权端点）。

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/api/health` | 健康检查 + 依赖探活 |
| `GET` | `/api/server/info` | 版本 / 引擎信息 |
| `GET` | `/install-qatlasd.sh` | qatlasd 安装脚本 |
| `GET` | `/swagger/index.html` | 交互式 API 文档（Swagger UI）|
| `GET` | `/swagger/doc.json` | OpenAPI 2.0 JSON spec |
| `GET` | `/api/pat/scopes` | 列 PAT scope 词表（纯常量，无用户数据）|
| `GET` | `/{path...}` | SPA 前端静态外壳（数据在被门禁的 API 后面）|
| `GET` | `/_/` ... | PocketBase admin UI |

## 鉴权端点（需要 PAT 或 session token）

### Papers

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/papers/stats` | `papers:read` | 论文资产统计（`available`、`total`、`has_pdf`、`has_md`、`has_json`、`needs_mineru`、`total_images`、`loaded_at`）；paperindex 不可用时返回 `{available:false}` |
| `GET` | `/api/papers/needs-mineru?limit=&include_claimed=` | `papers:read` | 列等待 MinerU 解析的论文 |
| `POST` | `/api/papers/{arxiv_id}/upload-pdf` | `papers:write` | 上传 PDF，见 [Upload API](upload-api.md) |
| `POST` | `/api/papers/{arxiv_id}/upload-mineru` | `papers:write` | 上传 MinerU 结果 zip（含 markdown + images）|
| `POST` | `/api/papers/{arxiv_id}/mineru-claim` | `papers:write` | 申请 MinerU 处理 claim |
| `DELETE` | `/api/papers/{arxiv_id}/mineru-claim/{claim_id}` | `papers:write` | 释放 claim |

> `papers:write` 隐式含 `papers:read`。
>
> **本服务不通过 API 对外分发 PDF / markdown 字节**：服务端持有这些原文供
> MinerU 流水线和上游 wiki 生成使用，但**没有**对外的 download / share /
> redirect 端点。需要原文请到论文原始来源（arXiv）拉取。

### Wiki

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/pages` | `wiki:read` | 列 Wiki 页面（支持 `?page_type=&status=&tags=`）。**默认排除 `type==source`**（Wikipedia 风格只展示 concept 词条）；显式传 `?page_type=source` 才返回 source |
| `GET` | `/api/pages/{page_id}` | `wiki:read` | 取单页（frontmatter + content）|
| `GET` | `/api/stats` | `wiki:read` | Wiki 统计（含 `entries`=词条数、`sources`=源文献数、`by_category`、`by_status`）|
| `GET` | `/api/search?q=&limit=` | `wiki:read` | 全文搜索。**默认排除 source**；显式传 `?include_sources=true` 才纳入 |
| `GET` | `/api/lint` | `wiki:read` | Wiki lint 报告（**当前是占位**，返回空 issues）|
| `GET` | `/api/wiki/sync/status` | `wiki:read` | Wiki git 状态 |
| `POST` | `/api/wiki/sync/pull` | `wiki:write` | 触发服务端 Wiki git fast-forward pull（`git fetch --prune` + `git pull --ff-only`），随后同步刷新内存缓存 |

> `wiki:write` 隐式含 `wiki:read`。

!!! note "内容追加不走 server"
    QuantumAtlas **没有**在线 ingest 端点。Wiki 内容追加走离线多 subagent 流水线
    （读 paper → 总结 concept → 去重合并 → commit 到 wiki repo），server 只读地
    serve 生成好的词条。详见 [生成 wiki 内容](../guides/generate-wiki-content.md)。

### Graph（Neo4j）

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/graph/stats` | `graph:read` | Neo4j 节点 / 关系计数 |
| `GET` | `/api/graph/schema` | `graph:read` | Neo4j label / relationship type 清单 |
| `POST` | `/api/graph/query` | `graph:read` | 执行 Cypher（**只读**，server 端跑 `ExecuteRead`）|

> 三个 graph 读口都收敛到 `authGuard + graph:read`。session token（浏览器登录）自带 `*` 自动放行；PAT 调用方需勾选 `graph:read`。其中 `/api/graph/query` 风险最高（执行调用方提供的 Cypher、无成本上限）——见下方 query 详述。

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
        "endpoint": "http://<rustfs-internal-host>:9000",
        "bucket": "qatlas-raw",
        "latency_ms": 12
      },
      "neo4j": {
        "status": "ok",
        "uri": "bolt://<neo4j-bolt-host>:7687",
        "database": "neo4j",
        "latency_ms": 8
      },
      "wiki": {
        "status": "ok",
        "dir": "/home/<USER>/QuantumAtlas-Wiki",
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

- multipart form 字段：`pdf` (必)
- query 参数：`expected_sha256=<hex>` (强烈推荐) / `overwrite=true`
- 状态码：
    - `201 Created` — 写了新对象
    - `200 OK` — 全部 unchanged 短路，零写入
    - `409 Conflict` — sha256 不同且没 `overwrite`，body 含 `existing_sha256` + `new_sha256`
    - `400 Bad Request` — sha256 mismatch / 损坏的 multipart / PDF header 不对等
- 并发安全（S3 conditional PUT `If-None-Match`），多 client 同字节并发只产生 1 个 201 + 其余 200

### `POST /api/wiki/sync/pull`

**需鉴权 + `wiki:write` scope**（session token 自动放行）。即使是 fast-forward only，它仍会在服务端跑 git 子进程并重建内存缓存，因此和其它写口一样门禁，避免被匿名滥用：

```bash
curl -X POST https://<server>/api/wiki/sync/pull \
    -H "Authorization: Bearer $QATLAS_TOKEN"
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

只读 Cypher 执行。**需鉴权 + `graph:read` scope**（session token 自动放行）。

```bash
curl -X POST https://<server>/api/graph/query \
  -H "Authorization: Bearer <token>" \
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

!!! warning "已接受的风险：Cypher 无代价上限"
    `query` 是只读的（驱动层 `ExecuteRead` 拒绝写），但**没有查询代价上限**——理论上一条病态查询（如无界笛卡尔积）能拖垮 Neo4j。**这是有意不加限制的取舍**：过了 `graph:read` 鉴权的调用方即「自己人」（登录用户或显式勾了 `graph:read` 的 PAT 持有者），同一个人本就能直连 Bolt 跑同样的查询，加应用层限制器只是徒增复杂度而挡不住真正想跑重查询的人。唯一缓解手段是**撤销出问题的凭据**（删 PAT / 登出用户）。详见 [鉴权模型](../concepts/auth-model.md) 与 [Neo4j 部署](../deployment/neo4j.md)。

### `POST /api/pat`（session only）

```json
{
  "name": "ci-upload",
  "description": "...",
  "scopes": ["papers:write", "wiki:read"],
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
  "scopes": ["papers:write", "wiki:read"],
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
