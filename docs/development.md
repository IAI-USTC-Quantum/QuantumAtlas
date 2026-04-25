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

- `POST /api/auth/cli-token`
- `GET /api/papers/{id}/resources`
- `POST /api/shares`
- `GET /api/shares`
- `DELETE /api/shares/{token}`
- `GET /share/{token}`
- `GET /share/{token}/{path}`

交互式文档默认在 `http://localhost:4200/api/docs`。

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
- 分享链接和 CLI token 是两套权限模型，不应混用同一个密钥。
- 远程协作者优先通过 API 和 Wiki Git 仓库协作，而不是直接依赖服务器文件系统权限。
