# 旧版 Python server / FastAPI 路径（已下线）

> 本文档收录 `atlas/server/` 这套 FastAPI server 相关的开发与部署命令。它仍
> 在仓库里，但**已不是生产路径**——线上跑的是 Go binary（`cmd/server/`）+
> 嵌入式 PocketBase。下面内容保留只是为了在以下三种场景下做兼容性参考：
>
> - 排查 Python 端 client（`atlas/`）行为时想本地起一个 FastAPI mock 端
> - 复刻历史 issue 或对照旧文档的预期 endpoint 行为
> - 把残留逻辑从 `atlas/server/` 迁出去之前做最后的回归
>
> Go server 当前生产细节见 [deployment.md](deployment.md)；新写代码或新写
> endpoint **请落到 `internal/routes/*.go`**，不要再扩 FastAPI 这边。

## 开发环境

```bash
uv sync --extra dev
```

常用命令：

```bash
# 测试
uv run pytest                              # 跑全部（含 slow / network / e2e）
uv run pytest tests/wiki -v
uv run pytest tests/server -v
uv run pytest -m "not network and not e2e" # 离线开发时跳过外部依赖
uv run pytest -m network                   # 只跑需要外网的测试

# 代码质量
uv run black atlas tests
uv run isort atlas tests
uv run ruff check atlas tests --select E9,F63,F7,F82
uv run mypy atlas
```

启动旧 FastAPI server：

```bash
uv sync --extra dev
uv run --script scripts/init_primitives.py
uv run -m atlas.server
```

默认端口同样是 4200，API 文档在 `http://localhost:4200/api/docs`（FastAPI 自带 Swagger UI）。

## FastAPI 端点（仅当前 `atlas/server/` 的实现，**不等于** Go server 的对外 API）

### Wiki 与知识

- `GET /health`
- `GET /api/server/info`
- `GET /api/pages`
- `GET /api/pages/{page_id}`
- `GET /api/search?q=...`
- `GET /api/stats`
- `GET /api/lint`
- `GET /api/wiki/sync/status`
- `POST /api/wiki/sync/pull`

应用仓库内的 `wiki/` 只是本地测试/临时目录，不作为主仓库内容追踪。正式 Wiki 内容应通过 `QuantumAtlas-Wiki` 的普通 Git commit / push / pull / PR 流程进入远端。Python server 不提供 push API，也不通过 Web UI 暴露创建/编辑页面的写入口。

`GET /api/wiki/sync/status` 会返回本地 Wiki checkout 的 branch、commit、upstream、ahead/behind、dirty 和 warnings。若 branch 不是 `main` 或 `master`，会出现 `wiki_branch_not_main` warning。

`POST /api/wiki/sync/pull` 只执行 `git fetch --prune` 和 `git pull --ff-only`。错误码约定：

- `409`: Wiki checkout 状态与同步前提冲突，例如目录不存在、不是 Git 仓库、本地有未提交修改，或不能 fast-forward。
- `500`: 服务器无法执行 git 命令。
- `502`: `git fetch` 已执行但远端交互失败。

### Server / Client Wiki 边界

是否采用 server 行为由代码入口决定，不由用户身份或配置自动猜测：

- `atlas/server/**` 里的 API handler、background task 和 Web 服务属于 server 行为。
- 本地 CLI、脚本、用户端工具和直接 `WikiEngine()` 调用属于 client / local 行为。

server 代码如果需要读取 Wiki，必须通过 `atlas.server.routers.api._configured_wiki_engine()` 创建 `WikiEngine`。这个工厂会传 `ensure_directories=False` 和 `wiki_content_writable=False`，让服务端拿到“禁止内容修改”的 Wiki 引擎。该引擎一旦调用 `save_page()`、`delete_page()`、`append_to_log()`、`update_index()` 等会修改 Wiki 内容文件的方法，会直接抛出 `WikiWriteDisabledError`。不要在 server 代码里直接调用 `WikiEngine()`，也不要从 server 路径调用 `lint(fix=True)`。

服务端允许更新 `WIKI_DIR` 的边界只有一个：`POST /api/wiki/sync/pull` 对 clean Git checkout 执行 `git fetch --prune` 和 `git pull --ff-only`。这属于同步远端已审阅内容，不属于服务端生成或编辑 Wiki 页面。Wiki 内容创建、编辑、lint fix 和提交应在用户端或独立的 `QuantumAtlas-Wiki` checkout 中完成。

### 图谱

- `GET /api/graph/stats`
- `GET /api/graph/schema`
- `POST /api/graph/query`

### 摄入（FastAPI-only，Go server 没有对等端点）

- `GET /api/ingest/stages`
- `POST /api/ingest/paper`
- `POST /api/ingest/{task_id}/continue`
- `GET /api/ingest/{task_id}`
- `GET /api/ingests`

> 摄入流程在 Go server 里被拆成 `qatlas ingest` CLI + `POST /api/papers/.../upload-*`
> 两步，不再有 server 侧持久 task 状态。如果你想跑 FastAPI 的多阶段 ingest，
> 用 `uv run -m atlas.server` 起本地实例。

### 协作与分享

- `GET /api/papers/{id}/resources`
- `POST /api/shares`
- `GET /api/shares`
- `DELETE /api/shares/{token}`
- `GET /share/{token}`
- `GET /share/{token}/{path}`

交互式文档默认在 `http://localhost:4200/api/docs`。

## 旧 systemd / 反向代理部署

```bash
# 安装为 user-mode systemd 服务（旧 Python 路径）
uv run -m atlas.server.service install --scope user --enable --now

# 升级 / 安装为 system-mode（需要 sudo）
uv run -m atlas.server.service install \
    --scope system --user "$USER" --enable --now
```

Caddy / nginx 上的反代和鉴权配置在 Go server 时代已经全部重写，旧 FastAPI 部
署相关 Caddyfile 模板和 caddy-security 鉴权链已下线（曾用方案：
caddy-security GitHub OAuth portal + JWT_SHARED_KEY 跨边缘共享 + 把
`X-Token-Subject` header 注入到 FastAPI，FastAPI 端只信任反代写入的 header）。
任何线上恢复都应改用 Go server + PocketBase 内置 OAuth 路径，见
[deployment.md](deployment.md)。
