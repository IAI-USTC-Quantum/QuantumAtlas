# QuantumAtlas

> 把量子算法论文从“PDF 和笔记”推进到“可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码”。

[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)
[![Neo4j](https://img.shields.io/badge/Neo4j-5.15+-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688?style=flat&logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)

QuantumAtlas 是一个面向量子算法研究的工作台。它把论文摄入、Wiki 编辑、图数据库同步，以及电路设计、代码生成、验证、资源估计串成一条可持续迭代的链路。

## 它能做什么

- 从 arXiv 获取论文，下载 PDF 并解析为 Markdown。
- 把知识沉淀到可审阅的 Markdown Wiki，而不是散落在脚本和临时笔记里。
- 将 Wiki 和结构化信息同步到 Neo4j，用图谱回答“它和什么有关”。
- 从算法定义继续往下走到 Quantum IR、Qiskit/QPanda 代码、验证和资源估计。
- 通过 Web API、CLI 和分享链接支持远程协作，而不要求所有协作者都能直接登录服务器。

## 快速开始

### 1. 先跑一个不依赖外部服务的 demo

```bash
git clone https://github.com/Agony5757/QuantumAtlas.git
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
├── wiki/                  本地测试/临时 Wiki（不追踪）
├── raw/                   本地开发默认论文资产目录
├── data/                  任务、share、ingest 等运行时状态
├── docs/                  补充文档
├── docker-compose.yml     Neo4j 开发环境
└── pyproject.toml         项目配置
```

默认目录只适合单机测试；`wiki/` 不作为主仓库内容追踪。生产环境通常会把 `WIKI_DIR`、`RAW_DIR`、`DATA_DIR` 外置到仓库之外。

## 当前状态

项目处于 alpha 阶段，但主线骨架已经完整：

- 论文摄入、Wiki、图谱、设计、代码生成、验证、估计都已打通。
- Web API、分享链接和远程协作流程可用。
- 项目更像“可持续扩展的研究基础设施”，而不是已经产品化的平台。

## 贡献

欢迎以下方向的贡献：

- 新增或完善 primitive、algorithm、paper 页面。
- 改进解析、提取、图谱同步和 API。
- 补充测试、修正文档、优化协作体验。

提交说明请使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`、`refactor:`、`test:`、`chore:`。版本发布由 Commitizen 统一维护，细节见 [docs/development.md](docs/development.md)。

## 许可证

[MIT License](LICENSE)

GitHub: https://github.com/Agony5757/QuantumAtlas

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
