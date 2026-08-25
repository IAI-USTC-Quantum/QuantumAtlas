# QuantumAtlas 仓库架构

本文面向开发者，说明 QuantumAtlas 多仓库结构、插件机制与部署拓扑。代码、路径与命令保持英文原文。

## 仓库全景

QuantumAtlas 拆分为三个仓库，依赖方向为单向：CLI / search →（HTTP API）→ qatlasd。

### 主仓 QuantumAtlas

`github.com/IAI-USTC-Quantum/QuantumAtlas`（本仓）。定位为「论文收集 + 多范式搜索 + 注册数据库」。

- `cmd/qatlasd` — Go 服务（基于 PocketBase 框架），唯一的服务端入口。
- `internal/` — Go 侧各模块：`config`（YAML + env 配置）、`routes`（HTTP API）、`papers` / `openalex` / `paperindex` / `lazyload`（论文注册与惰性加载）、`ingest`、`auth`、`hostapi`、`apidocs`（swaggo 生成的 OpenAPI，`pixi run swagger` 重新生成）等。
- `web/` — TanStack Router SPA（React + Vite + Tailwind）。构建产物 `web/dist/` 通过 `web/embed.go` 的 `//go:embed all:dist` 直接内嵌进 qatlasd 二进制，`go build ./cmd/qatlasd` 之后无需额外拷贝步骤；SPA 路由由服务端 404 回退到 `/index.html` 支持。
- `qatlas/` — 剩余的 Python 包：`parser/doi`（DOI 解析：Crossref / OpenAlex）、`paper_assets.py` 等。`qatlas` CLI 客户端已拆出（见下）。
- `rag/qatlas_rag` — GPU embed worker（FastAPI），提供 `/embed` 与 `/rerank`，是唯一保留的 Python 运行时角色；qatlasd 自己作为 Qdrant client 调用它。
- `deploy/` — docker compose 模板与 env 示例（见「部署拓扑」）。
- `search/qatlas_search` — 历史残留的旧版搜索代码（含对旧 `qatlas.client` 的惰性 import），已被独立仓 qatlas-search 取代，不在维护范围内。

### 独立私有仓 qatlas-cli

`github.com/IAI-USTC-Quantum/qatlas-cli`。`qatlas` 命令行客户端，src 布局（`src/qatlas/`），保留 `qatlas` 包名以兼容插件协议与 qatlas-search 的惰性 import。通过 HTTP API 访问 qatlasd（`qatlas.client._common` 里的 `ServerConfig` + `requests`）。

### 独立私有仓 qatlas-search

`github.com/IAI-USTC-Quantum/qatlas-search`。Agentic 搜索微服务 + `qatlas-search` CLI（`qatlas_search.cli:main`）。两个角色：

1. 作为 Docker 服务运行（镜像 `qatlas-search:local`），被 qatlasd 通过 HTTP 调用；
2. 作为 Python 包通过 entry point `qatlas.plugins` 挂载进 qatlas CLI，贡献 `qatlas search` 命令。

### 依赖方向

```
qatlas-cli ──HTTP API──┐
                       ├──► qatlasd ──► PostgreSQL / RustFS(S3) / Qdrant / embed worker
qatlas-search ──HTTP───┘
qatlas-search ──entry point "qatlas.plugins"──► qatlas-cli（进程内插件）
```

## 插件机制

CLI 的扩展点协议定义在 qatlas-cli 仓的 `src/qatlas/client/plugins/base.py`：

- `CommandSpec` — 一个插件贡献的 CLI 命令：`handler(argv) -> exit code` + `summary`。
- `QatlasPlugin` — 插件基类。子类设置 `name`，并按需覆盖：
  - `available() -> bool` — 环境检查门控（不满足时插件命令不出现在 CLI 中）；
  - `top_level_commands() -> dict[str, CommandSpec]` — 挂载为 `qatlas <name>`；
  - `contrib_subcommands() -> dict[str, CommandSpec]` — 挂载为 `qatlas contrib <name>`。

第三方插件通过 entry point group `qatlas.plugins` 注册。例如 qatlas-search 的 `pyproject.toml`：

```toml
[project.entry-points."qatlas.plugins"]
search = "qatlas_search.qatlas_plugin:plugin"
```

发现流程在 `src/qatlas/client/plugins/registry.py`：用 `importlib.metadata.entry_points()` 选出 `qatlas.plugins` group，逐个 `ep.load()` 并实例化；只有 `isinstance(plugin, QatlasPlugin)` 且 `available()` 通过的插件才贡献命令。单个插件 import 或运行失败只会被跳过，不会影响 CLI 的其他命令。

## 部署拓扑与启动命令

| 组件 | 默认端口 | 启动方式 |
| --- | --- | --- |
| qatlasd | 4200 | `pixi run build` 后 `./build/qatlasd serve --http=0.0.0.0:4200`，或 `deploy/docker-compose.yml` |
| PostgreSQL | 5432 | 外部实例；`QATLAS_POSTGRES_DSN` 指向它，goose migration 在 qatlasd 启动时自动应用 |
| RustFS / S3 | — | 可选，外部对象存储（如 NAS 上的 RustFS） |
| Qdrant | 6333 | 可选，`deploy/qdrant-compose.example.yaml`；语义检索 provider，需配 `QATLAS_RAG_QDRANT_URL` + `QATLAS_RAG_EMBED_URL` |
| rag embed worker | 8801 | `uv run uvicorn qatlas_rag.embed.worker:app --host 0.0.0.0 --port 8801`（在 `rag/` 目录） |
| qatlas-search | 不暴露端口 | 在主仓 `deploy/` 下 `docker compose --profile search up -d`（镜像 `qatlas-search:local`，仅 qatlasd 内部调用） |

未配 `QATLAS_POSTGRES_DSN` 时 registry 端点降级为 `available:false`（上传仍会落到对象存储，带 `X-Catalog-Sync: deferred` 头）。

## 安装

```bash
# qatlas CLI（独立私有仓，需要 SSH 访问权限）
uv tool install --from git+ssh://git@github.com/IAI-USTC-Quantum/qatlas-cli.git qatlas-cli

# qatlas-search（可选；安装后自动通过 entry point 挂载 `qatlas search`）
uv tool install --from git+ssh://git@github.com/IAI-USTC-Quantum/qatlas-search.git qatlas-search

# 主仓开发环境
pixi install        # Go 工具链 + 构建缓存（.gocache/）
uv sync             # Python 侧（含 dev extra）
pixi run build      # 产出 build/qatlasd
pixi run test-go    # Go 测试
uv run pytest       # Python 测试
```
