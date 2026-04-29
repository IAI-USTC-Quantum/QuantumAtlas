# QuantumAtlas

> 把量子算法论文从“PDF 和笔记”推进到“可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码”。

[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)
[![Neo4j](https://img.shields.io/badge/Neo4j-5.15+-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688?style=flat&logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)

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
| Raw Sources | `raw/` 或外置 `RAW_DIR` | PDF、Markdown、解析结果、运行资产等可追溯证据 | 尽量追加，不随意改写 |
| Wiki | `wiki/` 或外置 `WIKI_DIR` | 面向人和 LLM 的结构化页面、摘要、分类与链接；知识层 source of truth | 可审阅、可编辑 |
| Graph | Neo4j | 算法、原语、论文、实现之间的关系网络 | 从 Wiki 同步/派生 |

这样的边界有两个目的：

- 让研究笔记可以被人读，也能被工具稳定消费。
- 让关系查询不污染正文表达；分类、叙述和图查询各在合适的地方发生。

换句话说，Raw Sources 是证据链，Wiki 是整理和审阅后的知识真相层，Graph 是由 Wiki 派生出的关系索引。代码生成、验证和资源估计都应该从这条链路继续向下走，而不是让图数据库变成另一套独立事实来源。

## 它能做什么

- 从 arXiv 获取论文，下载 PDF 并解析为 Markdown。
- 把知识沉淀到可审阅的 Markdown Wiki，而不是散落在脚本和临时笔记里。
- 将 Wiki 和结构化信息同步到 Neo4j，用图谱回答“它和什么有关”。
- 从算法定义继续往下走到 Quantum IR、Qiskit/QPanda 代码、验证和资源估计。
- 通过 Web API、CLI 和分享链接支持远程协作，而不要求所有协作者都能直接登录服务器。

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
uv sync --extra dev
docker compose up -d
cp .env.example .env
uv run --script scripts/init_primitives.py
uv run -m atlas.server
```

默认入口：

- 首页：`http://localhost:4200`
- API 文档：`http://localhost:4200/api/docs`
- Neo4j：`http://localhost:7474`

生产部署、systemd 安装、反向代理和鉴权边界请看 [docs/deployment.md](docs/deployment.md)。

## 常用命令

```bash
# 作为全局工具安装
uv tool install . --editable --force
qatlas --help

# 论文摄入
qatlas ingest quant-ph/9508027 --no-extract --no-sync-neo4j

# 电路工具链
qatlas designer <kg_algorithm_id> -o circuit_ir.json
qatlas codegen circuit_ir.json --backend qiskit -o output.py
qatlas validator circuit_ir.json --compare-with qft
qatlas estimator circuit_ir.json --format markdown
```

如果不做全局安装，也可以继续使用 `uv run -m atlas.parser`、`uv run -m atlas.wiki`、`uv run -m atlas.server` 这些模块入口。

## 协作模型

当前仓库更偏向“研究基础设施”而不是静态资料库。默认目录适合本地开发和测试；生产或多人协作时，通常会把 `WIKI_DIR`、`RAW_DIR`、`DATA_DIR` 配到仓库之外，让代码版本、论文资产、Wiki 内容和运行状态有各自的生命周期。

如果打算用 Git 维护正式 Wiki，推荐把 Wiki 仓库作为应用仓库的兄弟目录 checkout，而不是嵌套放进 `QuantumAtlas/` 里面：

```text
~/work/
├── QuantumAtlas/          # 应用代码仓库
└── QuantumAtlas-Wiki/     # Wiki 内容仓库
```

然后在 `QuantumAtlas/.env` 中指向这个 Wiki checkout：

```env
WIKI_DIR=../QuantumAtlas-Wiki
```

这样协作边界会更清楚：`QuantumAtlas` 管应用代码和服务能力，`QuantumAtlas-Wiki` 管知识页面；Wiki 的创建、编辑、review、回滚和发布都走普通 Git commit / push / pull / PR 流程。应用仓库内的 `wiki/` 只适合作为本地测试或临时目录，不建议作为正式知识库。

推荐的协作节奏是：

1. 摄入论文或资料，保留 Raw Sources。
2. 生成或整理 Wiki 页面，让分类、摘要、引用和状态可审阅。
3. 将稳定 Wiki 页面同步到 Neo4j，用关系图做依赖发现和路径查询；Graph 是派生视图，不是另一份手工维护的 truth。
4. 从算法或原语继续生成实现，经过验证和资源估计后再进入下游使用。

## 文档导航

- [docs/architecture.md](docs/architecture.md): 项目的分层模型、source of truth、Wiki/Raw/Neo4j 边界，以及协作方式。
- [docs/deployment.md](docs/deployment.md): 本地启动、单机部署、systemd、环境变量、反向代理与鉴权示例。
- [docs/development.md](docs/development.md): 开发命令、API 概览、仓库结构、测试与发版流程。
- [QUANTUM_ATLAS.md](QUANTUM_ATLAS.md): Wiki 页面编写规范。

## 仓库概览

```text
QuantumAtlas/
├── atlas/                 核心代码
├── examples/              可独立运行的 demo
├── scripts/               初始化与维护脚本
├── tests/                 测试套件
├── wiki/                  本地测试/临时 Wiki（生产建议外置）
├── raw/                   本地开发默认论文资产目录
├── data/                  任务、share、ingest 等运行时状态
├── docs/                  补充文档
├── docker-compose.yml     Neo4j 开发环境
└── pyproject.toml         项目配置
```

## 当前状态

项目处于 alpha 阶段，但主线骨架已经完整：

- 论文摄入、Wiki、图谱、设计、代码生成、验证、估计都已打通。
- Web API、分享链接和远程协作流程可用。
- 项目更像“可持续扩展的研究基础设施”，而不是已经产品化的平台。

## TODO

- Agent 应用方向：Caddy 负责 OAuth、cookie 和 bearer token 鉴权；QuantumAtlas 后端只提供 API、share 链接和构建产物托管；所有页面设计集中在 `web/` 的 Vite + React 工作台。

## 贡献

欢迎以下方向的贡献：

- 新增或完善 primitive、algorithm、paper 页面。
- 改进解析、提取、图谱同步和 API。
- 补充测试、修正文档、优化协作体验。

提交说明请使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`、`refactor:`、`test:`、`chore:`。版本发布由 Commitizen 统一维护，细节见 [docs/development.md](docs/development.md)。

## 致谢

QuantumAtlas 最初的三层知识库设计受到 [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) 启发，并基于 Neo4j、FastAPI、Pydantic、Qiskit 等开源生态继续演化。

## 许可证

[MIT License](LICENSE)

GitHub: https://github.com/IAI-USTC-Quantum/QuantumAtlas

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
