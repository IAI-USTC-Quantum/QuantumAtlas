# REST API 总览

QuantumAtlas server 的所有 HTTP endpoint。auth 模型详见 [概念/鉴权](../concepts/auth-model.md)。

## 交互式 API 文档（Swagger UI） { #swagger-ui }

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

> 仅以下"无语料数据"端点保持公开：探活 / 版本 / 安装脚本 / API 文档 / scope 词表 / SPA 外壳。**论文数据本身不再匿名可读**——搜索、统计、论文资产等读口都已收敛到 `*:read` scope（见下方鉴权端点）。

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/api/health` | 健康检查 + 依赖探活 |
| `GET` | `/api/server/info` | 版本 / 引擎信息 + **能力发现**（`capabilities`：匿名可见布尔位 `paper_access` / `markdown_delivery` / `pdf_delivery`（恒 false——PDF 分发设计性停用）/ `agentic_search` / `mineru.enabled` / `mineru.on_demand`；认证调用者额外见 `mineru.daily_cap` 与 `mineru.converted_today`，来自批处理调度器快照）|
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
| `GET` | `/api/papers` | `papers:read` | 论文分页列表。过滤参数：`has_md`（默认资产已有 markdown）/ `status`（`pending \| ready \| failed`）/ `q`（标题子串，大小写不敏感）/**`arxiv_id` / `doi` / `paper_id`**（精确身份过滤——`arxiv_id` 自动去 `vN` 版本后缀，`doi` 容忍 `https://doi.org/` URL 前缀后归一）；`page` / `per_page`（默认 20，≤100）/ `sort`（`created_at` 默认 \| `updated_at`，降序）|
| `GET` | `/api/papers/{id}` | `papers:read` | 论文详情（registry 记录 + 资产行）。`{id}` 接受**三种标识符**：`qa_` paper_id、arXiv ID（新式 `2501.00010` / 老式 `quant-ph/9508027`，带或不带 `vN`）、DOI（含 `https://doi.org/…` URL 形态）。合法但未收录的标识符返回 404，`detail` 提示用 `GET /api/papers/lookup?ids=arxiv:…` 解析元数据 |
| `GET` | `/api/papers/lookup?ids=` | `papers:read` | 批量（**≤200 条**）解析 `kind:id` 引用（`arxiv:` / `doi:` / `openalex:`）。每项返回 `{ref, title, authors, year, hosted, has_md, resolved}`——`hosted` 表示是否已收录，`has_md`（hosted 时有意义）表示默认资产是否已有 markdown。**这是批量核对「有没有 markdown」的官方入口**，无需逐篇调详情 |
| `GET` | `/api/papers/stats` | `papers:read` | 论文资产统计（`available`、`total`、`has_pdf`、`has_md`、`has_json`、`needs_mineru`、`total_images`、`loaded_at`）；paperindex 不可用时返回 `{available:false}` |
| `GET` | `/api/papers/needs-mineru?limit=&include_claimed=` | `papers:read` | 列等待 MinerU 解析的论文 |
| `POST` | `/api/papers/{arxiv_id}/upload-pdf` | `papers:write` | 上传 PDF，见 [Upload API](upload-api.md) |
| `POST` | `/api/papers/{arxiv_id}/upload-mineru` | `papers:write` | 上传 MinerU 结果 zip（含 markdown + images）|
| `POST` | `/api/v1/papers/{arxiv_id}/mineru-lease` | `papers:write` | 申请 MinerU 处理 lease（响应字段含 `claim_id`）|
| `DELETE` | `/api/v1/papers/{arxiv_id}/mineru-lease/{claim_id}` | `papers:write` | 释放 MinerU lease |
| `POST` | `/api/papers/{arxiv_id}/mineru-claim` | `papers:write` | 申请 MinerU 处理 lease |
| `DELETE` | `/api/papers/{arxiv_id}/mineru-claim/{claim_id}` | `papers:write` | 释放 MinerU lease |
| `GET` | `/api/papers/{id_or_doi}/markdown` | `papers:read` | **PAPER_ACCESS** · 默认返回缓存的 markdown **字节流**（`text/markdown`）；`?format=link` 改为返回 JSON `{markdown_url}` RustFS 直链。未命中走 LRO：202 → 后台 silent fetch PDF + MinerU convert → poll 后再 GET 200 |
| `GET` | `/api/papers/{id_or_doi}/markdown/status` | `papers:read` | **PAPER_ACCESS** · side-effect-free 进度查询；body 含 `state` / `phase` / `pdf_ready` / `md_ready` / `fetch.*` / `convert.*` |
| `GET` | `/api/papers/{id_or_doi}/pdf` | `papers:read` | **已停用（410 Gone）** · PDF 不再对终端用户交付；请改用 `/markdown`。PDF 仍作为内部资产为 MinerU 转换与贡献者 lease 保留 |
| `GET` | `/api/papers/{id_or_doi}/pdf/status` | `papers:read` | **PAPER_ACCESS** · 内部抓取管线的 side-effect-free 进度查询；状态机比 markdown 少 convert 阶段；body 不再含 `pdf_url` |
| `GET` | `/api/papers/{id_or_doi}/images/zip` | `papers:read` | **PAPER_ACCESS** · 显式获取 MinerU 产出的图片 zip：默认流 `application/zip` 字节；`?format=link` 返回 JSON `{images_url, format:"link", expires_in}` 直链。无 LRO——图片由 `/markdown` 端点的转换流程产生，无资产时 404 并提示先取 markdown |

> `papers:write` 隐式含 `papers:read`。
>
> **PAPER_ACCESS** 标记的端点**只在部署方开启 `QATLAS_PAPER_ACCESS_ENABLED=true`
> 时才注册**（默认 OFF；关闭时是 404 而非 403）。开启等于自愿承担对外重分发
> PDF / markdown 字节（或 RAG snippet 形式的派生片段）的合规义务，见
> [License & Attribution · 论文访问开关](../about/license-and-attribution.md#论文访问开关-self-hosted)。
> 公共 `quantum-atlas.ai` 部署默认 OFF。
>
> `{id_or_doi}` 路径段同时接受 arxiv canonical id（含 `vN`，例
> `quant-ph/9508027v2` 或 `2501.00010v1`）、DOI（IANA 前缀 `10.<registrant>/`
> 自动 detect，例 `10.1103/PhysRevLett.103.150502`）以及 **`qa_` paper_id**
> （server 内部解析成该论文的 canonical 身份，取最高已收录的 arXiv 版本；
> DOI-only 论文走 DOI 管线）。bare arXiv ID 补版本时优先查本地 catalog，
> 未收录才抓 arxiv.org。DOI 经 OpenAlex 反查到 canonical arxiv id 后
> 走同一套 handler；缺 `QATLAS_OPENALEX_MAILTO` 时 DOI 路径返回 503，
> arxiv 路径不受影响。

### Search

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/search` | `papers:read` | 多 provider 论文搜索。body 为 SearchEntry JSON，engine fan-out 到 `search.providers`（config.yaml，默认 `catalog,arxiv,openalex`）列出的 provider。`text` / `title` / `arxiv_id` / `doi` **全空时 400**（不再发出空查询）；带 `arxiv_id` / `doi` 的请求把身份透传给 qatlas-search 微服务做精确查询（arXiv `id_list` / OpenAlex DOI filter / Semantic Scholar paper 端点）。`results` 每项附带 `has_md` / `has_pdf` / `status`（registry 默认资产摘要；catalog 不可用时省略）|
| `POST` | `/api/search/multi` | `papers:read` | **逐平台原始搜索**。body `{text, max_results, sources[]}`（≤32 个 backend），代理一次 `mode:"multi"` 调用到 qatlas-search 微服务：每个 backend 返回**各自的原始命中列表**（源自己的排序），不做跨源融合打分（融合打分在 `POST /api/search` / `/api/search/agentic`）。调用者存着的第三方 key 被解密后随请求 `api_keys` 转发，key 后端跑在用户自己的凭据下。不计费（同 `POST /api/search`）。`search.remote` 未启用时 503 |
| `GET` | `/api/search/backends` | session only | backend 目录（SPA 渲染成 checkbox 选择器）：静态表（`internal/search/backendmeta.go`）合并微服务 live `/v1/backends` 可用性 + 调用者已存的 key。每行 `{name, label, category, requires_key, user_key, server_ready, key_configured, selectable}`，`selectable = server_ready \|\| (user_key && key_configured)`——需要 key 但没配的后端渲染为禁用并附"去 dashboard 配置"链接 |
| `POST` | `/api/search/agentic` | `papers:read` | 计量 + LLM 总结的 agentic 搜索（qatlas-search 微服务）。body 额外接受 `sources[]` **钉死 backend 列表**（v0.26.0 起；不传则由微服务侧全量 fan-out）。空 entry（`text` / `title` / `arxiv_id` / `doi` 全空）在计量**之前**就 400；身份条目同样透传给微服务；`results` 与 `POST /api/search` 一样附带 `has_md` / `has_pdf` / `status` |

语义向量检索不在 `/api/search` 的 provider 列表里：它由独立的 qatlas-rag 微服务
提供，经 qatlas-search 的 fan-out 接入（`POST /api/search/agentic` 路径），
qatlasd 在论文 ready 时通过 `rag.remote` 配置段向 qatlas-rag 推送索引构建。
`/api/search/multi` 与 `/api/search/backends` 同样依赖 `search.remote` 启用。

### Personal Search Keys（个人第三方搜索 key）

每个用户可存自己的第三方搜索 API key（IEEE / Scopus / Tavily 等 key 后端），
`POST /api/search/multi` 调用时自动解密注入。存储是 AES-256-GCM 加密的
`search_api_keys` PocketBase collection（owner-only 规则、仅服务端写入）；
加密密钥由 system PAT token 做域分离 SHA-256 派生——**轮换 system PAT 会使
已存 key 全部失效**（表现为解密失败，用户重新录入即可），无独立配置键。

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/me/search-keys` | session only | 当前用户的 key 清单：`{enabled, keys:[{backend, hint, updated_at}]}`。`hint` 是打码的末 4 位，**永不返回 key 本体**。server 未配 system PAT 时 `enabled:false` |
| `PUT` | `/api/me/search-keys/{backend}` | session only | upsert 一个 key（body `{"key": "..."}`）。`backend` 必须是目录里有 user-key 槽位的后端，否则 400 |
| `DELETE` | `/api/me/search-keys/{backend}` | session only | 删除；不存在时 opaque 404 |

> session-only（非 scopeGuard）的理由同 `/api/pat`：这是浏览器 dashboard 里的
> 凭据管理，泄露的 PAT 不应能读走或替换 owner 的第三方 key。

### Robust Downloader

详见 [Robust Downloader](downloader.md)（策略阶梯 / 配置 / downloaderproxy /
probe）。端点本身：

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/downloader/fetch` | `papers:write` | 提交一批标识符（DOI / arXiv id / 论文 URL，body `{"items":[...]}`，**≤50 条**）。每条先解析 + `ResolveOrMint` 进 registry，再入策略阶梯队列。逐条返回 `{input, kind, paper_id, created, error?}` + 总 `enqueued`。`paper_access.enabled` / `downloader.enabled` 任一关闭时 503；registry 不可用时 503 |
| `GET` | `/api/downloader/jobs` | `papers:read` | job 快照：`{jobs:[...], counters:{...}}`。每个 job 含 per-paper state/phase、胜出策略、**完整 attempt trace**（策略 id / URL / 错误 / 耗时）|

SPA 页面 `/$lang/downloader`：粘贴标识符 → 客户端解析预览徽章 → 提交 →
逐 job 进度 + 策略轨迹渲染（挂在 `downloader` 插件上）。

### Plugins

Plugins share one manifest/capability model with two orthogonal axes
(`kind` × `transport`). The first-party theorems plugin is
`kind=builtin` (compiled into `qatlasd`, in-process); third-party plugins are
`kind=external` and connect to the RPC listener over `transport=socket`, or are
spawned by the host over `transport=stdio`.

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/v1/plugins` | `plugins:read` | 列出发现的插件清单与状态（`connected` / `disconnected` / `disabled` / `incompatible`）|
| `POST` | `/api/v1/plugins/{id}/enable` | `plugins:write` | 运行时启用已配置插件 |
| `POST` | `/api/v1/plugins/{id}/disable` | `plugins:write` | 运行时禁用已配置插件 |

配置驱动的远程微服务也会出现在插件清单里：``search-remote``（qatlas-search）
与 ``rag-remote``（qatlas-rag）在各自配置段启用后注册，qatlasd 启动后探测
它们的 ``/healthz`` 并标记 ``connected`` / ``disconnected``。

External plugins do not call host capabilities over HTTP. They connect to the
WebSocket JSON-RPC service at `QATLAS_RPC_WS_BIND`. `initialize` params contain
`id`, `secret`, and `abi_version`; after the handshake the same connection can
call the host-shared capabilities `papers/getMarkdown`, `papers/getMeta`,
`papers/getCitedRefs`, and `events/publish`. (The host core carries no
plugin-domain methods — ADR 0003.)

### Theorems（builtin 插件，read-through 一个 Lean-content git checkout）

theorems builtin 插件把上游 Lean-content 仓库（`QATLAS_THEOREMS_DIR`）的
`artifacts/registry.json` + `artifacts/audit_records/certified.json` + Lean 源
read-through 暴露出来。它**不持有** PocketBase collection（ADR 0004：已证 Theorem
是从 git read-through，不落库）。

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `GET` | `/api/theorems/list` | `theorems:read` | 列已证 Theorem（支持 `?family_id=&audit_status=&kind=` 过滤）|
| `GET` | `/api/theorems/families` | `theorems:read` | 列 theorem family 定义（过滤下拉数据源）|
| `GET` | `/api/theorems/stats` | `theorems:read` | 聚合计数（by_kind / by_family / by_audit_status / sorry_free / certified）|
| `GET` | `/api/theorems/theorem/{fqn}` | `theorems:read` | 取单个 Theorem（完整 registry 条目 + audit verdict）|
| `GET` | `/api/theorems/theorem-source/{fqn}` | `theorems:read` | 按需取该 Theorem 的 Lean 源文件 |
| `GET` | `/api/theorems/sync/status` | `theorems:read` | theorems git 状态|
| `POST` | `/api/theorems/sync/pull` | `theorems:write` | 触发服务端 theorems git fast-forward pull，随后同步 reload 内存缓存 |

> `theorems:write` 隐式含 `theorems:read`。`/api/<id>/sync/{pull,status}` 是平台为
> pull 类插件挂的**统一形状**（ADR 0002/0003）。

### PAT 管理（**只接受 session token**，PAT auth 被拒）

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/pat` | session only | 创建 PAT，返回明文（一次）|
| `GET` | `/api/pat` | session only | 列当前用户的 PAT（无明文）|
| `DELETE` | `/api/pat/{id}` | session only | 撤销 |

### OAuth Device Flow（`qatlas auth login` 后端）

RFC 8628 device authorization grant。CLI 没有浏览器 / session，所以由用户在浏览器里登录 + 输 user_code 授权，CLI 轮询拿 minted PAT。前两个端点匿名（CLI 调），后三个 **session-only**（PAT auth 被拒，理由同 `/api/pat`：泄露的 PAT 不应能自我繁殖）。

| Method | Path | 鉴权 | 用途 |
|---|---|---|---|
| `POST` | `/api/oauth/device/code` | 匿名 | RFC 8628 §3.1，CLI 启动 flow。body：`{name, description?, expires_in_days, scopes[]}`（要 mint 的 PAT 规格）。返回 `{device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval}` |
| `POST` | `/api/oauth/device/token` | 匿名 | RFC 8628 §3.4 + §3.5。CLI 用 `device_code` 按 `interval` 轮询。成功返回 minted PAT plaintext（一次性）；待批 / 太快 / 过期 / 拒绝 / 无效全部返 **HTTP 400** + `{error}` ∈ `authorization_pending \| slow_down \| expired_token \| access_denied \| invalid_grant` |
| `GET` | `/api/oauth/device/code?user_code=` | session only | SPA 的 `/<lang>/device` 用 `user_code` 查待批请求；响应除了 CLI seeded 的 `name` / `scopes` / `expires_in_days` 外还包含 `available_scopes`（用户能 mint 的全部 scope）/ `scope_descriptions` / `max_expiry_days`，供浏览器渲染编辑表单 |
| `POST` | `/api/oauth/device/approve` | session only | body `{user_code}` + 可选 `{name, scopes, expires_in_days}` 覆盖 —— 让用户在 Approve 时改 PAT 规格。下一次 `/token` 轮询会以最终（覆盖后的）规格 mint PAT 并绑定到当前 session 用户 |
| `POST` | `/api/oauth/device/deny` | session only | body `{user_code}`，拒绝。下一次 `/token` 轮询返回 `access_denied` |

> 完整 device-flow 概念背景见 [概念 · 鉴权 · OAuth Device Flow](../concepts/auth-model.md)；schema 详见 `/swagger/index.html`。

### Admin（管理端：Asset Browser + 获取失败）

Admin 端点全部走 `adminGuard`：**session token + admin 白名单**（`auth.admin_logins`
等，PAT 一律拒绝——管理员是人）。Asset Browser 是运维通道，**独立于
`paper_access.enabled`**（不分发义务：admin 面板不算对外重分发 lane）。

#### Admin Asset Browser（`/api/admin/assets/*`）

SPA 页面 `/$lang/admin/assets`：防抖搜索 → 论文卡片 → 可展开资产表，每行
Preview（Dialog iframe/pre）、Download、Copy Presigned URL + Copy S3 Key。
`{kind}` 取 `pdf` 或 `markdown`（别名 `md`）。

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/api/admin/assets/{paper_id}` | 资产清单：每个 asset 的 S3 object key / size / sha256 / content-type |
| `GET` | `/api/admin/assets/{paper_id}/{kind}` | 单资产详情；`?presign=true&ttl=1h` 附带 presigned URL |
| `GET` | `/api/admin/assets/{paper_id}/{kind}/download` | 代理流式下载（`Content-Disposition: attachment`）|
| `GET` | `/api/admin/assets/{paper_id}/{kind}/inline` | 代理流式预览（`Content-Disposition: inline`；PDF 进 iframe viewer，markdown 按 text 渲染）|
| `GET` | `/api/admin/assets/{paper_id}/{kind}/url` | presigned S3 URL JSON——浏览器直连 S3，绕过 qatlasd 代理。TTL **1 分钟–24 小时**（默认 1h），超出区间截断 |
| `GET` | `/api/admin/assets/batch?paper_ids=a,b,c` | 批量资产清单（**≤50 篇**）|
| `GET` | `/api/admin/assets/batch/download?paper_ids=a,b,c&kind=pdf` | 流式 ZIP 打包下载（**≤20 篇**）|
| `GET` | `/api/admin/assets/search?q=...&kind=pdf&limit=50` | 按 title / DOI / arXiv ID（ILIKE）找**有资产**的论文 |

> presign 仅 S3 后端支持（`presign_supported` 字段）；dev 的 LocalStore 回落代理流。

#### 获取失败与补救

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/api/admin/acquisition/failures` | 持久化的 PDF 抓取失败记录：阶段、原因、重试计数（`paper_acquisition_events` 审计表之上的聚合视图）|

管理员 SPA 的失败表对每行渲染 DOI 直达链接 + **内联 PDF 上传**（人工取回 PDF
对着 DOI 直接 `POST /api/papers/{doi}/upload-pdf`），即时闭合获取状态并触发
MinerU——这是 Robust Downloader 的失败兜底闭环，见
[Robust Downloader · 管理员补救流](downloader.md#admin-remediation)。


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
      "postgres": {
        "status": "ok",
        "backend": "postgres",
        "latency_ms": 8
      }
    }
  }
}
```

- `data.status` 是聚合状态：`healthy`（全部 ok 或 not_configured）/ `degraded`（任一 error）
- `code` **永远 200**（即使 degraded）—— 别让上层 LB / Caddy 把整条链路 trip 成 down
- `message` 在 degraded 时变 `"Dependency degraded."`，方便 log scraper
- 每个 probe 5 秒超时（`probeTimeout`），三个并行执行
- PostgreSQL / S3 不配置时返回 `"status": "not_configured"`，**不下拉聚合等级**

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

### 长任务（LRO）：`/api/papers/{id_or_doi}/{markdown,pdf}`

仅在 `QATLAS_PAPER_ACCESS_ENABLED=true` 时注册。两类资源
（`markdown` 与 `pdf`）共享同一套**异步 + 进度可拉**的协议，让 agent
方可以放心并发调用同一篇论文：

- 第一次 GET 立即返回，**不阻塞** fetch / convert 完成；
- N 个并发调用被 server-side dedupe 成 1 次 fetch + 1 次 convert，所有调用方
  观察同一 Job snapshot；
- `…/status` 端点是 side-effect-free 的，poll 多少次都不会再触发后端任务。

#### 状态机

```
                            cached ────────────────────────────────────────────┐
GET /md ── miss ──┬→ queued ──→ fetching ──→ converting ──→ done ──────────────┤
                  │   (silent fetch from arxiv.org, then MinerU convert)         │
                  │                                                                │
                  └→ failed(retryable | fatal | quota_exhausted,                  │
                         phase ∈ {error_fetching, error_converting})              │
                  └→ not_in_arxiv (404)                                            │

注：PDF 抓取仍是上述管线的第一阶段（内部资产）；对外的 GET /pdf 已停用
（410 Gone），不再有独立的 PDF 状态机对外暴露。
```

#### 触发 GET（202 / 200 / 404）

```bash
curl -i https://<server>/api/papers/quant-ph/9508027v2/markdown \
     -H "Authorization: Bearer $QATLAS_TOKEN"

# 缓存命中
HTTP/1.1 200 OK
Content-Type: text/markdown; charset=utf-8
Content-Length: 76382
…markdown bytes…

# 缓存未命中（首次或仍在跑）
HTTP/1.1 202 Accepted
Operation-Location: /api/papers/quant-ph/9508027v2/markdown/status
Retry-After: 5
{
  "arxiv_id": "quant-ph/9508027v2",
  "state":   "queued",
  "phase":   "fetching_pdf",
  "pdf_ready": false,
  "md_ready":  false,
  "submitted_at": "2026-06-05T03:00:00Z",
  "detail":  "conversion in progress",
  "operation": {
    "status_url":          "/api/papers/quant-ph/9508027v2/markdown/status",
    "next_poll_after_iso": "2026-06-05T03:00:05Z"
  }
}
```

#### 合规：内容访问开关（`QATLAS_PAPER_ACCESS_ENABLED`）

**是否对外分发论文内容**由这个服务端开关决定（合规落点，ADR [0011](../adr/0011-by-id-asset-reads.md)），也决定上面这些 **PAPER_ACCESS** 路由是否注册：

| 开关 | pdf | markdown / json | 说明 |
|---|---|---|---|
| **关**（默认，公共 quantum-atlas.ai） | arxiv 论文 → `arxiv.org` 直链；非 arxiv → 不给 | 不给 | 不再分发；canonical 源自己分发。*（当前实现：端点整体不注册 → 404；arxiv 直链是 ADR 0011 既定 OFF 态，跟踪 [#8](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/8)）* |
| **开**（operator 显式接受衍生作品分发义务） | 发（默认直链，见下） | 发（默认字节流，见下） | 详见 [License & Attribution · 论文访问开关](../about/license-and-attribution.md#论文访问开关-self-hosted) |

#### 字节流 vs 直链（`?format=`，ADR 0011）

开关**允许发之后**，**用什么形式发**是与合规无关的传输选择，按 kind 有默认值，并可用 `?format=link|bytes` 逐请求复写：

| 资产 | 默认 | 复写 | 说明 |
|---|---|---|---|
| `markdown` | **字节流** | `?format=link` | MinerU 派生的小文本，默认内联串出 |
| `images/zip` | **字节流** | `?format=link` | MinerU 产出的图片打包 zip，显式请求才返回 |

后端无法 presign（dev 的 `LocalStore`）时，`link` 请求自动回落字节流。直链响应形如：

```bash
curl -i "https://<server>/api/papers/quant-ph/9508027v2/markdown?format=link" \
     -H "Authorization: ******"

HTTP/1.1 200 OK
Content-Type: application/json
{
  "arxiv_id":     "quant-ph/9508027v2",
  "format":       "link",
  "markdown_url": "https://raw.quantum-atlas.ai/qatlas-pdf/9508/9508027v2.md?X-Amz-…",
  "expires_in":   86400
}
```

#### 进度查询（status，永远 200 除 400 / 404）

```bash
curl https://<server>/api/papers/quant-ph/9508027v2/markdown/status \
     -H "Authorization: Bearer $QATLAS_TOKEN"
```

各状态对应 body（关键字段加粗）：

```jsonc
// 已就绪
{
  "arxiv_id": "quant-ph/9508027v2",
  "state":   "cached",
  "phase":   "ready",
  "pdf_ready": true,
  "md_ready":  true,
  "markdown_url": "/api/papers/quant-ph/9508027v2/markdown"
}

// 正在 fetch PDF
{
  "arxiv_id": "quant-ph/9508027v2",
  "state":   "running",
  "phase":   "fetching_pdf",
  "pdf_ready": false,
  "md_ready":  false,
  "submitted_at": "...",
  "started_at":   "...",
  "fetch": {
    "started_at":     "...",
    "bytes_received": 1234567,
    "bytes_total":    4503234,
    "attempts":       1
  }
}

// PDF 已保存，MinerU 在跑（pdf_ready=true 告诉 agent: PDF 没白下）
{
  "arxiv_id": "quant-ph/9508027v2",
  "state":   "running",
  "phase":   "converting_md",
  "pdf_ready": true,
  "md_ready":  false,
  "fetch":   {"started_at":"...", "completed_at":"...", "bytes_received":4503234, "sha256":"...", "attempts":1},
  "convert": {"started_at":"...", "mineru_task_id":"task-abc", "stage":"running", "polled_count":7},
  "queue":   {"running_count":4, "max_concurrent":4, "eta_basis":"observed_avg_of_15_jobs", "avg_duration_seconds":180}
}

// MinerU slot 不够，排队中（agent 看 queue.position / eta_seconds 决定退避）
{
  "arxiv_id": "2501.00010v1",
  "state":   "queued",
  "phase":   "converting_md",
  "pdf_ready": true,
  "md_ready":  false,
  "submitted_at": "...",
  "queue": {
    "position":             3,
    "ahead_of_me":          2,
    "running_count":        4,
    "max_concurrent":       4,
    "eta_seconds":          540,
    "avg_duration_seconds": 180,
    "eta_basis":            "observed_avg_of_18_jobs"
  }
}

// MinerU 配额耗尽（PDF 已抓取但不再对外交付；等配额恢复后取 markdown）
{
  "arxiv_id": "quant-ph/9508027v2",
  "state":   "cooldown",
  "phase":   "error_converting",
  "kind":    "daily_limit",
  "pdf_ready": true,
  "md_ready":  false,
  "detail":   "server quota exhausted: all 3 MinerU API keys have reached today's daily limit — service resumes at 2026-06-06T00:01:00+08:00",
  "retry_after":     1717603260,
  "retry_after_iso": "2026-06-06T00:01:00+08:00"
}

// arxiv 没这篇（fatal，agent 应放弃）
{
  "arxiv_id": "quant-ph/9999999v9",
  "state":   "failed",
  "phase":   "error_fetching",
  "kind":    "fatal",
  "pdf_ready": false,
  "md_ready":  false,
  "detail":   "fetch from arxiv: arxiv: paper not found"
}
```

#### Agent 决策三元组

任何 status 响应都带 `state` + `pdf_ready` + `md_ready`，从这三个字段即可
判断「该不该等」「等什么」：

| `state` | `pdf_ready` | `md_ready` | agent 行动 |
|---|---|---|---|
| `cached` | ✅ | ✅ | 直接 GET 资源拿字节 |
| `queued` / `running` | ✗ | ✗ | 等 `Retry-After`，再 poll |
| `running` | ✅ | ✗ | 等 MinerU 收尾，之后 GET `/markdown` |
| `cooldown` | ✅ | ✗ | 到 `retry_after_iso` 之前不再 poll |
| `failed` `kind=fatal` | ✗ | ✗ | 永久失败，放弃 |
| `failed` `kind=retryable` | ✗ / ✅ | ✗ | 到 `retry_after_iso` 后重试 |

#### DOI 入口 { #doi-addressing }

`{id_or_doi}` 路径段头部如果匹配 `^10\.\d{4,9}/`（IANA DOI 前缀），server
自动经 OpenAlex 反查 → canonical arxiv id → 走同一套 handler。

```bash
# DOI → arxiv → 同一个 LRO 流程
curl -i https://<server>/api/papers/10.1103/PhysRevLett.103.150502/markdown \
     -H "Authorization: Bearer $QATLAS_TOKEN"
```

#### 自动默认值（server 应用的隐式推断）

server 接受三类"不完整"的 id 输入，并自动补齐到 canonical 形态：

| 输入形态 | 例子 | server 推断 | 默认值出处 |
|---|---|---|---|
| DOI | `10.1103/PhysRevLett.103.150502` | OpenAlex 反查 → arxiv canonical | OpenAlex API |
| 无版本 arxiv id | `0811.3171` / `quant-ph/9508027` | 加 `vN` = latest | `/abs/<id>` HTML `<meta property="og:url">` |
| bare old-style（无 category） | `9508027` / `9508027v2` | 加 `category=quant-ph` | `paperassets.DefaultOldStyleCategory`（来自 `cat:quant-ph` bootstrap 假设；issue #10 跟踪全量审计）|

每一次应用默认值，server 都会在响应里通过**两条平行通道**告知 caller：

**HTTP headers**（永远存在，包括字节流响应）：

```
X-QAtlas-Requested-Id: 9508027
X-QAtlas-Resolved-Id:  quant-ph/9508027v2
X-QAtlas-Defaults-Applied: version=v2 (no version specified; latest published version used); category=quant-ph (no category prefix; server default per docs/reference/arxiv-ids.md §3.1)
```

**JSON body 字段**（仅 JSON 响应；与 header 同步）：

```jsonc
{
  "arxiv_id":       "quant-ph/9508027v2",
  "requested_id":   "9508027",
  "resolved_id":    "quant-ph/9508027v2",
  "defaults_applied": [
    "version=v2 (no version specified; latest published version used)",
    "category=quant-ph (no category prefix; server default per docs/reference/arxiv-ids.md §3.1)"
  ],
  "state": "queued",
  ...
}
```

输入本身就是 canonical（带 vN + 带 category）时，三个字段全部**省略**，body 保持精简。

Agent / client 集成建议：

- 把 `X-QAtlas-Defaults-Applied` 渲染成一行 `Note: …` 输出，避免用户因为"我请求的是 0811.3171 但你给我 v3" 产生疑惑；
- `requested_id` 跟 `resolved_id` 不一致时可作为缓存键的二次校验；
- 不要硬解析 defaults_applied 的字符串内容（它是 human-readable，未来会有微调）；要程序化判断"应用了什么默认"请用 `resolved_id != requested_id` 加上 path 前缀启发式。

DOI 路径特有错误：

| 状态 | 含义 |
|---|---|
| `400` | DOI 格式不合规（长度 > 256 / 含控制字符 / 前缀不匹配）|
| `404` | OpenAlex 找不到该 DOI，**或**该 work 没有 arxiv 表现（OA `ids.arxiv` 缺失）|
| `404` | DOI 解出 arxiv id，但 arxiv abs 页 404（论文被撤稿 / 从未索引）|
| `502` | OpenAlex 上游错误 / arxiv version 解析上游错误（连接 / 5xx 重试用尽）|
| `503` | `QATLAS_OPENALEX_MAILTO` 未配置 — 服务方需补配 |
| `503` | arxiv version 解析 rate-limited — 稍后重试 |

### `POST /api/pat`（session only）

```json
{
  "name": "ci-upload",
  "description": "...",
  "scopes": ["papers:write"],
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
  "scopes": ["papers:write"],
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
