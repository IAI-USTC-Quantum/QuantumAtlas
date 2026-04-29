# Development

## 开发环境

```bash
uv sync --extra dev
```

常用命令：

```bash
# 测试
uv run pytest
uv run pytest tests/wiki -v
uv run pytest tests/server -v
uv run pytest -m integration

# 代码质量
uv run black atlas tests
uv run isort atlas tests
uv run ruff check atlas tests --select E9,F63,F7,F82
uv run mypy atlas
```

## API 概览

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

应用仓库内的 `wiki/` 只是本地测试/临时目录，不作为主仓库内容追踪。正式 Wiki 内容应通过 `QuantumAtlas-Wiki` 的普通 Git commit / push / pull / PR 流程进入远端。QuantumAtlas server 不提供 push API，也不通过 Web UI 暴露创建/编辑页面的写入口。

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

### 摄入

- `GET /api/ingest/stages`
- `POST /api/ingest/paper`
- `POST /api/ingest/{task_id}/continue`
- `POST /api/ingest/paper/reviewed-extraction`
- `GET /api/ingest/{task_id}`
- `GET /api/ingests`

### 协作与分享

- `GET /api/papers/{id}/resources`
- `POST /api/shares`
- `GET /api/shares`
- `DELETE /api/shares/{token}`
- `GET /share/{token}`
- `GET /share/{token}/{path}`

交互式文档默认在 `http://localhost:4200/api/docs`。

## 数据目录模型

QuantumAtlas 把知识内容、论文资产和运行时状态拆成三个显式目录：

| 目录 | 职责 | 是否建议 Git 管理 |
|------|------|------------------|
| `WIKI_DIR` | 可审阅、可追踪的 Markdown 知识库，例如页面、实体、论文来源页和比较页 | 是。生产/协作环境推荐独立的 `QuantumAtlas-Wiki` 仓库 |
| `RAW_DIR` | canonical 论文资产库，例如 PDF、解析后的 Markdown、metadata JSON、图片等大文件或可再生成资产 | 否。通常不进 Git，用对象存储、备份或服务器文件系统管理 |
| `DATA_DIR` | 服务端运行时状态，例如 share token、ingest task 状态和版本 manifest | 否。属于服务状态，不应当作知识源提交 |

`WIKI_DIR` 回答“我们如何描述和组织知识”。它应该像普通文档仓库一样走 commit、review、push、pull、PR；服务端只读取它，或在明确调用 `/api/wiki/sync/pull` 时快进自己的 clean checkout。Wiki 内容创建、编辑、lint fix 和提交应发生在用户端或独立的 Wiki checkout。

`RAW_DIR` 回答“论文原始与解析资产在哪里”。server ingest 可以写入这里，包括下载 PDF、保存 arXiv metadata JSON、保存解析 Markdown 和图片。对外分享这些资产时，不暴露本地路径，而是通过 `/api/shares` 和 `/share/{token}` 生成受 token 限制的 URL。

`DATA_DIR` 回答“服务运行到了什么状态”。它保存任务、share、ingest 进度和版本 manifest。这里的数据可以持久化和备份，但不是长期知识源；如果需要重建知识，应优先依赖 `WIKI_DIR` 和 `RAW_DIR`。

开发环境可以使用仓库内默认目录：

```env
WIKI_DIR=wiki
RAW_DIR=raw
DATA_DIR=data
```

生产或协作环境推荐外置：

```env
WIKI_DIR=/srv/quantumatlas-wiki
RAW_DIR=/srv/quantumatlas-raw
DATA_DIR=/srv/quantumatlas-data
```

## 仓库结构

```text
QuantumAtlas/
├── atlas/                 核心代码
├── examples/              独立 demo
├── scripts/               初始化与维护脚本
├── tests/                 测试套件
├── wiki/                  本地测试/临时 Wiki（不追踪）
├── raw/                   本地开发默认论文资产目录
├── data/                  本地运行时状态
├── docs/                  补充文档
├── docker-compose.yml     Neo4j 开发环境
└── pyproject.toml         项目配置
```

`atlas/` 里的核心模块包括：

- `parser`: 从 arXiv 获取论文并解析 PDF。
- `extractor`: 调用 LLM 抽取算法结构。
- `wiki`: 页面 CRUD、搜索、lint、Neo4j 同步。
- `server`: FastAPI Web 服务与模板。
- `knowledge` / `knowledge_graph`: 图谱模型与 Neo4j 交互。
- `designer`: 从算法定义生成 Quantum IR。
- `codegen`: 生成 Qiskit 或 QPanda 代码。
- `validator`: 电路验证与参考实现对比。
- `estimator`: 资源估计。

## 版本与发布

版本号只由 Commitizen 在发版时维护，不要手改 `pyproject.toml` 里的 `version`。

推荐流程：

1. 日常提交使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`。
2. 通过 Commitizen 统一更新版本号和 changelog。
3. 用 `v$version` 形式打 tag，并由 GitHub Actions 构建 release。

本地如需预演版本变化：

```bash
uv run --with commitizen cz bump --dry-run
```

如果生产环境要求代码版本必须对齐 release tag，可以设置：

```env
QUANTUMATLAS_REQUIRE_RELEASE_TAG=true
```

## 协作时的注意点

- Wiki 内容和应用代码可以分开演进，不必同频发版。
- 分享链接只用于公开资源访问；登录用户身份由反向代理、SSO 或 API gateway 注入。
- 远程协作者优先通过 API 和 Wiki Git 仓库协作，而不是直接依赖服务器文件系统权限。
