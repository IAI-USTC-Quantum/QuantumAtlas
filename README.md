# QuantumAtlas

> 把量子算法论文从“PDF 和笔记”推进到“可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码”。

[![Go 1.23+](https://img.shields.io/badge/go-1.23+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![PocketBase v0.38](https://img.shields.io/badge/PocketBase-v0.38-B8DBE4?style=flat&logo=pocketbase&logoColor=black)](https://pocketbase.io/)
[![Neo4j](https://img.shields.io/badge/Neo4j-5.15+-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)
[![Documentation Status](https://app.readthedocs.org/projects/quantum-atlas/badge/?version=latest)](https://quantum-atlas.readthedocs.io/zh-cn/latest/)

> 📚 **完整文档：<https://quantum-atlas.readthedocs.io>** — 安装、架构、贡献、部署、API 参考都在这里。

QuantumAtlas 是一个面向量子算法研究的分层知识库和实现工作台。它把论文摄入、Wiki 沉淀、图数据库同步，以及电路设计、代码生成、验证、资源估计串成一条可持续迭代的链路。

核心想法很简单：**分类和关联是两回事**。Raw Sources 保留证据，Wiki 是被审阅后的知识 source of truth，图数据库则从 Wiki 同步和构建出来，负责回答“它与什么有关”。

```text
论文 / 资料
    -> Raw Sources        不可变的来源和中间资产
    -> Wiki               人可读、LLM 可编辑的知识 source of truth
    -> Neo4j Graph        从 Wiki 同步出的算法、原语、论文关系索引
    -> Quantum IR / Code  可验证、可估计、可运行的实现
```

## 设计哲学

QuantumAtlas 不把“知识库”理解成一堆脚本生成的临时文件，而是把它拆成三层，各自承担清晰职责：

| 层级 | 默认位置 | 责任 | 变化方式 |
| --- | --- | --- | --- |
| Raw Sources | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw/`，或显式覆盖 `QATLAS_RAW_DIR` | PDF、Markdown、解析结果、运行资产等可追溯证据 | 尽量追加，不随意改写 |
| Wiki | 兄弟 Git checkout（默认 `../QuantumAtlas-Wiki`），或显式覆盖 `QATLAS_WIKI_DIR` | 面向人和 LLM 的结构化页面、摘要、分类与链接；知识层 source of truth | 可审阅、可编辑 |
| Graph | Neo4j | 算法、原语、论文、实现之间的关系网络 | 从 Wiki 同步/派生 |

这样的边界有两个目的：

- 让研究笔记可以被人读，也能被工具稳定消费。
- 让关系查询不污染正文表达；分类、叙述和图查询各在合适的地方发生。

换句话说，Raw Sources 是证据链，Wiki 是整理和审阅后的知识真相层，Graph 是由 Wiki 派生出的关系索引。代码生成、验证和资源估计都应该从这条链路继续向下走，而不是让图数据库变成另一套独立事实来源。

## Wiki 页面类型

Wiki 内部按用途分四类，互相通过 `[[page-id]]` 链接：

- **Concepts** (`wiki/concepts/`)：解释量子计算概念，回答“这是什么”。
- **Entities** (`wiki/entities/{algorithms,primitives,people}/`)：记录算法、原语和人物等可被引用的实体；这些是同步到 Neo4j 的主要对象。
- **Sources** (`wiki/sources/papers/`)：论文等源文献的 Wiki 化摘要，保留元数据、关键贡献和引用关系。
- **Comparisons** (`wiki/comparisons/`)：算法/原语之间的横向比较，只用于阅读，不参与图同步。

每个页面带 YAML frontmatter（`id`、`title`、`type`、`status` 等必填），文件名遵循 `algo-*` / `prim-*` / `person-*` / `paper-arxiv-*` / `comp-*` 等前缀规范。完整模板、frontmatter schema、lint 错误码和同步规则见 [docs/wiki-conventions.md](docs/wiki-conventions.md)。

## 它能做什么

- 从 arXiv 获取论文，下载 PDF 并解析为 Markdown。
- 把知识沉淀到可审阅的 Markdown Wiki，而不是散落在脚本和临时笔记里。
- 将 Wiki 和结构化信息同步到 Neo4j，用图谱回答“它和什么有关”。
- 从算法定义继续往下走到 Quantum IR、Qiskit/QPanda 代码、验证和资源估计。
- 通过 Web API、CLI 和分享链接支持远程协作，而不要求所有协作者都能直接登录服务器。

## 安装

QuantumAtlas 分两个交付物：

- **`qatlasd`**（Go 二进制，单文件 ~30 MB，自带前端 SPA + PocketBase + SQLite）—— 部署服务端用
- **`qatlas`**（Python CLI，`quantum-atlas` 包）—— 日常用户调用 server API 用

### 装 server (`qatlasd`)

```bash
# 一行装：自动检测 OS/arch、下载最新 binary 到 ~/.local/bin、
# SHA256 校验。这步只装 binary 本身。
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh

# 钉版本 / 改安装路径
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --version v0.2.5
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --dir /opt/qatlas/bin
```

支持 `linux/{amd64,arm64}` + `darwin/arm64` 三个平台（Intel Mac 用 [`go install`](docs/deployment.md#b-go-install) 路径）。

装完后**手动**注册成 systemd 服务（脚本不再链式调用，保证在 dash / busybox 等流式 parser 上稳定）：

```bash
qatlasd service install                    # 交互模式，问你要 mode + .env path
# 或全自动：
qatlasd service install \
    --mode user --dotenv-path ~/QuantumAtlas/.env --force
```

### 装 client (`qatlas` CLI)

```bash
# 推荐：uv 全局工具（隔离环境 + 升级方便）
uv tool install quantum-atlas

# 或 pipx
pipx install quantum-atlas

# 或 pip 直接装
pip install quantum-atlas

qatlas --help
```

`qatlas` CLI 默认通过 `QATLAS_SERVER_URL` 指向远端 server。配置详见 [.env.example](.env.example)。

## 快速开始

### 1. 先跑一个不依赖外部服务的 demo

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas
uv sync
uv run --script examples/demo_pipeline.py --algorithm qft --backend qiskit --save-code
```

这个 demo 不需要 LLM API key，也不需要 Neo4j。它会直接走完“设计 -> 生成代码 -> 验证 -> 资源估计”的主流程。

### 2. 本地启动 Web 服务

```bash
# 一次性同步 Python + npm 依赖 + 前端 build + Go build
pixi run build

cp .env.example .env
# 编辑 .env，填上 NEO4J_URI / NEO4J_USERNAME / NEO4J_PASSWORD 指向
# 你自己准备的 Neo4j 实例（团队共享 / 自起 / 托管均可）。
# 如需 GitHub OAuth 登录，再填 GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET。
./build/qatlasd serve --http=0.0.0.0:4200
```

默认入口：

- 首页 / SPA：`http://localhost:4200`
- PocketBase admin UI：`http://localhost:4200/_/`
- PAT 管理页：`http://localhost:4200/pat`（CLI bearer 走 PAT，更细的 scope/过期/审计）

生产部署、systemd 安装、反向代理和鉴权边界请看 [docs/deployment.md](docs/deployment.md)。

## 常用命令

```bash
# 作为全局工具安装 client（qatlas CLI）— 从 PyPI（推荐）
uv tool install quantum-atlas
# 或开发模式装本地 checkout（贡献时用）
# uv tool install . --editable --force
qatlas --help

# 论文摄入（服务器侧抓取 + 解析）
qatlas ingest quant-ph/9508027 --no-extract --no-sync-neo4j

# 论文资产贡献（鉴权用户上传）
qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf --metadata meta.json
qatlas upload markdown 2501.00010v1 --markdown out.md --source mineru

# 本地用自己的 MinerU token 解析后推给服务器
qatlas mineru 2501.00010v1 --push-pdf

# 电路工具链
qatlas designer <kg_algorithm_id> -o circuit_ir.json
qatlas codegen circuit_ir.json --backend qiskit -o output.py
qatlas validator circuit_ir.json --compare-with qft
qatlas estimator circuit_ir.json --format markdown
```

如果不做全局安装，也可以继续使用 `uv run -m qatlas.parser`、`uv run -m qatlas.wiki`、
`uv run -m qatlas.cli` 这些模块入口。

## 协作模型

QuantumAtlas 偏向「研究基础设施」而不是静态资料库。所有配置由 [pydantic-settings](https://docs.pydantic.dev/latest/concepts/pydantic_settings/)（Python client）和 godotenv（Go server）自动从仓库根目录的 `.env` 加载；项目自有字段统一用 `QATLAS_` 前缀（旧名作 alias 保留）。多人协作时通常什么都不用写——所有存储路径都有默认：`QATLAS_WIKI_DIR` 默认是兄弟 checkout `../QuantumAtlas-Wiki`；`QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` / `QATLAS_PB_DATA_DIR` 默认走 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/{raw,data,pb_data}`，跟 git checkout 完全分离。需要存到挂载盘或 `/var/lib/` 时再显式覆盖。

内容贡献分两条并列路径：

- **Raw 资产**走 `QATLAS_RAW_DIR`（默认 `$XDG_DATA_HOME/quantum-atlas/raw`），按 YYMM 分片存储（如 `<raw_dir>/pdf/9508/9508027v1.pdf`、`<raw_dir>/pdf/2501/2501.00010v1.pdf`）。三种方式都会落到同一布局：
  1. 服务器侧按 arXiv ID 抓取（`qatlas ingest`）。
  2. 鉴权用户直接上传 PDF / Markdown（`qatlas upload pdf|markdown`，对应 `POST /api/papers/{arxiv_id}/upload-*`）。
  3. 本地用自己的 `MINERU_API_TOKEN` 跑 MinerU 后推回云端（`qatlas mineru`）。
- **Wiki 内容**走独立 Git 仓库（推荐作为应用仓库的兄弟目录 checkout），任何人都可以 clone / commit / PR；服务器侧的 Wiki checkout 只接受 fast-forward 拉取，通过 `POST /api/wiki/sync/pull` 触发，无需 SSH 上服务器。

完整 CLI 选项、鉴权说明（`QATLAS_USER_HEADER` / bearer token）、ff-only 同步语义和推荐协作节奏见 [docs/contribution-workflow.md](docs/contribution-workflow.md)。

## 文档导航

- [docs/architecture.md](docs/architecture.md): 项目的分层模型、source of truth、Wiki/Raw/Neo4j 边界，以及协作方式。
- [docs/contribution-workflow.md](docs/contribution-workflow.md): Raw 贡献的三条路径、鉴权、Wiki Git 协作与服务器同步的完整 how-to。
- [docs/upload-api.md](docs/upload-api.md): `qatlas upload …` / `POST /api/papers/.../upload-pdf` 完整 API 参考（sha256 dedup、idempotent retry、in-transit guard、conflict 处理）。
- [docs/storage-design.md](docs/storage-design.md): Raw / Metadata / Graph 三层存储如何切分、为什么这样设计、对未来扩展的留口。
- [docs/storage-rustfs.md](docs/storage-rustfs.md): qatlas ↔ RustFS 集成 ops 指南（env vars、IAM policy、bucket versioning 自管、`qatlasd storage prune` 使用、故障排查）。
- [docs/deployment.md](docs/deployment.md): 本地启动、单机部署、systemd、环境变量、反向代理与鉴权示例。
- [docs/development.md](docs/development.md): 开发命令、API 概览、仓库结构、测试与发版流程。
- [docs/graph-visualization-research.md](docs/graph-visualization-research.md): 图谱可视化前端选型调研（待实现）。
- [docs/wiki-conventions.md](docs/wiki-conventions.md): Wiki 页面类型、模板、frontmatter schema、lint 规则与同步约定。

## 仓库概览

```text
QuantumAtlas/
├── atlas/                 核心代码
├── cmd/                   Go server 入口
├── internal/              Go server 内部包
├── web/                   React SPA 前端
├── examples/              可独立运行的 demo
├── scripts/               初始化与维护脚本
├── tests/                 测试套件
├── docs/                  补充文档
└── pyproject.toml         项目配置
```

> 状态目录（`wiki/`、`raw/`、`data/`、`pb_data/`）已**不在**仓库内——
> 它们默认落到 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/`
> 之下，或通过 `.env` 显式覆盖到挂载盘 / `/var/lib/...`。详见
> [docs/migration-storage-layout.md](docs/migration-storage-layout.md)。

## 当前状态

项目处于 alpha 阶段，但主线骨架已经完整：

- 论文摄入、Wiki、图谱、设计、代码生成、验证、估计都已打通。
- Web API、分享链接和远程协作流程可用。
- 项目更像“可持续扩展的研究基础设施”，而不是已经产品化的平台。

## TODO

- Agent 应用方向：用户 / 鉴权由 Go server 内嵌 PocketBase 直接处理（GitHub OAuth + PAT），
  CLI 与 SPA 都通过 `Authorization: Bearer <token>` 调写口；QuantumAtlas 后端集中提供
  API、share 链接和构建产物托管；所有页面设计集中在 `web/` 的 Vite + React 工作台。

## 贡献

欢迎以下方向的贡献：

- 新增或完善 primitive、algorithm、paper 页面。
- 改进解析、提取、图谱同步和 API。
- 补充测试、修正文档、优化协作体验。

提交说明请使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`、`refactor:`、`test:`、`chore:`。版本发布由 Commitizen 统一维护，细节见 [docs/development.md](docs/development.md)。

## 致谢

QuantumAtlas 最初的三层知识库设计受到 [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) 启发，并基于 Go、PocketBase、Neo4j、Pydantic、Qiskit 等开源生态继续演化。

完整开源生态依赖、灵感来源和维护者名单见 [致谢](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/credits/)。

## 数据来源与归属（Attribution）

QuantumAtlas 的论文 catalog 基于以下开放学术数据源：

- **论文 metadata**（标题 / 作者 / DOI / 引用关系等）来自 [OpenAlex](https://openalex.org/) 与 [Crossref](https://www.crossref.org/)，均为 **[CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/)** 公共领域许可。
- **论文条目本身**来自 [arXiv](https://arxiv.org/)，按 [arXiv ToU](https://arxiv.org/help/license) 镜像 metadata。**论文 PDF 字节我们不持有也不再分发**——`share/download` 接口 `307 redirect` 到 arxiv 原 URL，用户直接从 arxiv 下载。

完整 license / attribution / 撤稿请求流程见 [License & Attribution](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/license-and-attribution/)；服务使用条款见 [Terms of Service](https://quantum-atlas.readthedocs.io/zh-cn/latest/about/terms-of-service/)。

## 许可证

[Apache-2.0 License](LICENSE)（代码 + 文档 + Wiki 内容均 Apache-2.0）

GitHub: https://github.com/IAI-USTC-Quantum/QuantumAtlas

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
