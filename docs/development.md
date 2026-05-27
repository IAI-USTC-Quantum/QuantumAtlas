# Development

## Go server 开发环境（当前）

Server 是 Go binary（`cmd/server/`），通过 pixi 管理 Go 工具链。前端 SPA
在 `web/`（React + Vite）。

```bash
# 一次性 setup（pixi env 装 go + gxx，进 .pixi/）
pixi install

# 常用任务（pyproject.toml [tool.pixi.tasks]）
pixi run build        # 构建 server binary -> build/quantumatlas
pixi run vet          # go vet（首次 ~5 分钟编 PocketBase 依赖；后续秒级）
pixi run test-go      # go test ./internal/... ./cmd/...
pixi run test-py      # uv run pytest -m 'not e2e and not legacy and not network'

# 前端
cd web && npm install
cd web && npm run build         # 出 dist/，复制到 internal/webui/dist/
cd web && npm run dev           # vite dev server (端口 5173)

# Python CLI（不变）
uv sync --extra dev
uv run pytest -m "not e2e and not network and not legacy"
```

> **不要在 pixi env 外手动 `go build`**：你机器上全局 go 是 pixi-managed
> conda-forge build，自带 `CC=x86_64-conda-linux-gnu-cc` baked-in。pixi env
> 里有匹配的 `gxx`，CGO 调用能成功；env 外没有，`go vet` / 任何走 cgo 的
> path 都会卡在 cc not-found 的 futex 死锁上。如果一定要在 env 外编，
> `CGO_ENABLED=0 go build ./cmd/server` 也行（本项目所有依赖都是 pure-Go）。

### Known Go server gotchas

这些坑都已经在 `cmd/server/main.go` 修了，但解释一下方便后人理解：

1. **PocketBase 默认把 `pb_data/` 写在 binary 同目录**。我们 binary 装在
   `/usr/local/bin/`（root-only）或 `~/.local/bin/`（user-writable 但仍是
   per-user 散落），都不是合适的数据目录。修法：systemd unit `ExecStart`
   显式带 `--dir=/home/timidly/QuantumAtlas-go/pb_data`。**任何手动起
   server 也要带这个 flag**，否则 PocketBase 在当前 CWD 下默写 pb_data。

2. **PocketBase 默认走 `net.Listen("tcp", "0.0.0.0:4200")`，创建 v6-only
   socket**（可在 `/proc/net/tcp6` 看到，但 `/proc/net/tcp` 里没有）。
   WSL2 + Windows 主机的 `netsh interface portproxy` 是 v4-only 的，
   把请求转到 v6-only 监听 = 直接 reject。修法：`cmd/server/main.go::
   maybeIPv4Listener` 检测到 IPv4 字面量 bind addr 时显式 `net.Listen(
   "tcp4", ...)`，强制 v4 socket。日志会打 `forced tcp4 listener on
   0.0.0.0:4200`。

3. **`.env` 通过 godotenv 加载，路径由 `QATLAS_DOTENV` env 决定**（systemd
   unit 里 `Environment=QATLAS_DOTENV=/home/timidly/QuantumAtlas/.env`）。
   相对路径如 `WIKI_DIR=../QuantumAtlas-Wiki` 解析 anchor 是 `.env` 所在
   目录。**不**用 systemd `EnvironmentFile=` 因为后者只 inject env vars，
   server 拿不到文件路径就没法做 anchor。

4. **Go 1.26.3 vs pixi env**：`go.mod` 写 `go 1.26.2`，pixi 装到 1.26.3。
   升 go.mod 前先 `pixi search -c conda-forge go` 确认 conda-forge 有匹配
   版本；conda go 包默认 `GOTOOLCHAIN=local`（在 `.pixi/envs/default/etc/
   conda/env_vars.d/go.json`）禁止自动下载新 toolchain，go.mod 要求更高
   就会卡死编不出来。

### 部署到 1810 后端

```bash
pixi run build
scp build/quantumatlas 1810:/tmp/quantumatlas-go
ssh -t 1810 'sudo bash /tmp/qa-go-update-system.sh'
```

binary 在 `~/.local/bin/quantumatlas`（无 sudo 写入），systemd unit 在
`/etc/systemd/system/qatlas.service` (`User=timidly`，sudo restart)。
日常运维：

```bash
systemctl status qatlas         # 读，免 sudo
journalctl -u qatlas -f         # 读，免 sudo
sudo systemctl restart qatlas   # 写，要 sudo
```

## 旧版 Python server 开发命令

下面这些命令针对 `atlas/server/` 这套 FastAPI server，它仍在仓库里但
**不是生产路径**。当你只动 client 或想跑 FastAPI 兼容性测试时再用。

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
QATLAS_WIKI_DIR=wiki
QATLAS_RAW_DIR=raw
QATLAS_DATA_DIR=data
```

生产或协作环境推荐外置：

```env
QATLAS_WIKI_DIR=/srv/quantumatlas-wiki
QATLAS_RAW_DIR=/srv/quantumatlas-raw
QATLAS_DATA_DIR=/srv/quantumatlas-data
```

> 旧名 `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` 仍作 alias 兼容；新写法推荐 `QATLAS_*` 前缀（详见 `.env.example`）。

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

## CI

仓库里有**两条独立的 GitHub Actions 流水线**，分工很明确：

### 1. `Pytest`（每个 PR / push 都跑）

文件：`.github/workflows/pytest.yml`

```bash
uv run pytest -rs -m "not network and not e2e"
```

- 矩阵：Python 3.11 / 3.12
- 触发：push 到 `main` 分支、对 `main` 提 PR、`workflow_dispatch` 手动触发
- 范围：**只跑离线测试**——不发任何外网请求，不依赖任何 secret，不连任何远端服务
- 目的：保护代码本身的正确性，确保 PR 合入前不破坏既有逻辑

### 2. `Nightly production smoke`（每天定时 + 手动）

文件：`.github/workflows/nightly.yml`

```bash
uv run pytest -rs -m "network or e2e"
```

- 触发：`cron: '0 18 * * *'`（UTC 18:00 = 北京时间次日 02:00），也可在 Actions 页面手动 `workflow_dispatch`
- 范围：跑所有标了 `network` 或 `e2e` 的测试
- 目的：**验证线上服务的稳定性**，把"代码是好的"扩展到"线上跑出来也是对的"

关键测试 `tests/integration/test_production_smoke.py`：直接对 `QATLAS_SERVER_URL` 指向的真实线上实例发请求，覆盖 `/health`、`/api/ingest/stages`、以及一个完整的 fetch + parse 任务。如果某天 nightly 红了，第一时间就知道线上挂了或者降级了。

#### 需要在 repo Secrets 里配置：

| Secret | 用途 | 不设置会怎样 |
|---|---|---|
| `QATLAS_SERVER_TARGETS` | 线上服务公网入口列表，**逗号或换行分隔**。每条可写成 `URL` 或 `URL\|insecure`（后者跳过 TLS 校验，给自签证书 / 直连 IP 用）。示例：`https://quantum-atlas.ai`<br>`https://47.102.36.175\|insecure` | `test_production_smoke.py` 全部 `skip`（不会让 job 红） |
| `MINERU_API_TOKEN` | MinerU API token，被部分用例兜底使用（如本地启 server 测 mineru 路径） | 仅本地启 server 走 mineru 的 e2e 用例会 `skip` |

> 配多个 target 时，每个测试都会按 target 各跑一次（pytest 参数化），id 就是 URL 本身。
> 兼容老配置：`QATLAS_SERVER_URL` 单个 URL + `QATLAS_INSECURE=1` 仍然生效。

加 secret 的方法：repo Settings → Secrets and variables → Actions → New repository secret；或 `gh secret set <NAME> --repo <owner>/<repo>`（多行值从 stdin 读）。

> **为什么 nightly 跑得起来 production 烟测、本地却不太需要打开**
> 线上服务有公网入口、跑着完整的 `.env`；本地开发盒子通常没必要每次都拉真的论文。本地想跑一次：`QATLAS_SERVER_TARGETS=https://atlas.example.com uv run pytest -m e2e tests/integration/test_production_smoke.py`。

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
QATLAS_REQUIRE_RELEASE_TAG=true
```

> alias: `QUANTUMATLAS_REQUIRE_RELEASE_TAG` / `REQUIRE_RELEASE_TAG` 仍生效。

## 协作时的注意点

- Wiki 内容和应用代码可以分开演进，不必同频发版。
- 分享链接只用于公开资源访问；登录用户身份由反向代理、SSO 或 API gateway 注入。
- 远程协作者优先通过 API 和 Wiki Git 仓库协作，而不是直接依赖服务器文件系统权限。
