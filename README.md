# QuantumAtlas 🧭

> **Agentic AI 驱动的量子算法实现框架**
>
> 将量子算法论文转化为可执行代码，构建可持续演化的量子算法知识库。

[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)
[![Neo4j](https://img.shields.io/badge/Neo4j-5.15+-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)
[![Tests](https://img.shields.io/badge/tests-407%20passed-success)](tests/)

---

## 🎯 项目愿景

QuantumAtlas 是一个**分层式量子算法知识库系统**，通过 LLM + 知识图谱的技术栈，实现：

```
论文 → 分层Wiki（分类/摘要）→ 图数据库（关系建模）→ 可执行代码
```

**核心洞察**：
> **分类和关联是两回事。**
> - **Wiki 知识库** → 解决"这是什么"（定义、摘要、分类）
> - **图数据库** → 解决"与什么有关"（依赖关系、引用网络）

---

## 🏗️ 三层架构

受到 [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) 启发，采用三层知识管理架构：

```
┌─────────────────────────────────────────────────────────────────┐
│                     Layer 3: Neo4j Graph                         │
│  ┌─────────┐    ┌───────────┐    ┌─────────┐                    │
│  │Algorithm│───▶│DEPENDS_ON │───▶│Primitive│   关系查询         │
│  └────┬────┘    └───────────┘    └─────────┘   路径发现         │
│       │              ▲                                         │
│       │PUBLISHES      │                                         │
│       ▼              │                                         │
│  ┌─────────┐         │                                         │
│  │  Paper  │─────────┘                                         │
│  └─────────┘                                                   │
└─────────────────────────────────────────────────────────────────┘
                              ▲
                              │ 同步
                              │
┌─────────────────────────────────────────────────────────────────┐
│                     Layer 2: Wiki                                │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌────────────┐    │
│  │ concepts │  │ entities  │  │  sources  │  │comparisons │    │
│  │          │  │           │  │           │  │            │    │
│  │ QFT 定义 │  │ 算法实例  │  │ 论文摘要  │  │ 性能对比   │    │
│  └──────────┘  └───────────┘  └──────────┘  └────────────┘    │
│                                                                 │
│  Human-readable, LLM-editable Markdown pages                    │
│  双向链接: [[page-id]], YAML frontmatter                        │
└─────────────────────────────────────────────────────────────────┘
                              ▲
                              │ 摄入
                              │
┌─────────────────────────────────────────────────────────────────┐
│                     Layer 1: Raw Sources                         │
│  ┌─────────────┐  ┌──────────┐  ┌───────────┐                  │
│  │ papers/pdf  │  │ datasets │  │  articles  │                  │
│  └─────────────┘  └──────────┘  └───────────┘                  │
│                                                                 │
│  Immutable source of truth - 所有内容可追溯                      │
└─────────────────────────────────────────────────────────────────┘
```

| 层级 | 目录 | 用途 | 可变性 |
|------|------|------|--------|
| **Raw** | `raw/` | 原始资料（PDF、数据集） | 🔒 不可变 |
| **Wiki** | `wiki/` | 结构化知识页面 | ✏️ 可编辑 |
| **Graph** | Neo4j | 实体关系网络 | 🔄 自动同步 |

---

## 🚀 快速开始

### 环境要求

- Python 3.11+
- Docker & Docker Compose
- 4GB+ RAM

### 1. 安装

```bash
git clone https://github.com/Agony5757/QuantumAtlas.git
cd QuantumAtlas

# 安装依赖
pip install -e ".[dev]"

# 设置环境变量
export NEO4J_PASSWORD="quantum-atlas"
```

### 2. 启动服务

```bash
# 启动 Neo4j
docker-compose up -d

# 初始化知识图谱
python scripts/init_primitives.py

# 启动 Web 界面
uvicorn atlas.server.main:app --reload --port 8000
```

### 3. 访问界面

| 服务 | URL | 说明 |
|------|-----|------|
| **Web 首页** | http://localhost:8000 | Wiki 统计、快速操作 |
| **Wiki 浏览器** | http://localhost:8000/wiki | 页面列表、搜索、编辑 |
| **图可视化** | http://localhost:8000/graph | Neo4j 关系图 |
| **API 文档** | http://localhost:8000/api/docs | REST API |
| **Neo4j Browser** | http://localhost:7474 | 图数据库查询 |

### 4. 摄入论文

```bash
# 方式一：CLI
python -m atlas.parser 9508027 --wiki --sync-neo4j

# 方式二：Web API
curl -X POST http://localhost:8000/api/ingest/paper \
  -H "Content-Type: application/json" \
  -d '{"arxiv_id": "9508027"}'
```

---

## 📦 模块架构

```
atlas/
├── parser/          # 论文解析 ✅
│   ├── arxiv_fetcher.py    # arXiv 获取
│   ├── pdf_parser.py       # PDF 解析
│   └── __main__.py         # CLI 入口
│
├── wiki/            # Wiki 引擎 ✅ NEW
│   ├── engine.py           # 核心引擎
│   ├── page.py             # WikiPage 模型
│   ├── ingester.py         # 摄入工作流
│   ├── querier.py          # 查询/搜索
│   ├── linter.py           # 健康检查
│   ├── templates.py        # 页面模板
│   └── sync/
│       └── neo4j_sync.py   # Neo4j 同步
│
├── server/          # Web 服务 ✅ NEW
│   ├── main.py             # FastAPI 应用
│   ├── config.py           # 配置管理
│   ├── routers/
│   │   ├── wiki.py         # Wiki 路由
│   │   ├── graph.py        # 图可视化
│   │   └── api.py          # REST API
│   └── templates/          # Jinja2 模板
│       ├── wiki/           # Wiki 页面模板
│       ├── graph/          # 图可视化模板
│       └── index.html      # 首页
│
├── knowledge/       # 知识图谱 ✅
│   ├── neo4j_client.py     # Neo4j 客户端
│   └── models.py           # Pydantic 模型
│
├── extractor/       # LLM 提取 ✅
│   ├── extractor.py        # 提取器
│   ├── llm_interface.py    # LLM 接口
│   └── algorithm_ir.py     # 算法 IR
│
├── designer/        # 电路设计 ✅
│   ├── designer.py         # 设计器
│   ├── quantum_ir.py       # Quantum IR
│   ├── quantum_circuit.py  # 电路模型
│   ├── primitive_loader.py # 原语加载
│   ├── primitive_composer.py # 原语组合
│   ├── optimizer.py        # 优化器
│   └── parameter_mapper.py # 参数映射
│
├── codegen/         # 代码生成 ✅
│   ├── generator.py        # 生成器基类
│   ├── qiskit_generator.py # Qiskit 生成
│   ├── qpanda_generator.py # QPanda 生成
│   ├── template_engine.py  # 模板引擎
│   └── formatter.py        # 代码格式化
│
├── validator/       # 电路验证 ✅
│   ├── validator.py        # 验证器
│   ├── equivalence_checker.py # 等价检查
│   ├── reference_comparison.py # 参考对比
│   └ test_framework.py     # 测试框架
│
└── estimator/       # 资源估计 ✅
    ├── estimator.py        # 估计器
    ├── resource_analyzer.py # 资源分析
    └── report_generator.py # 报告生成
```

---

## 🔬 核心工作流

### Ingest（摄入）

```
Paper (arXiv ID)
    │
    ├─▶ Fetch PDF → raw/papers/pdf/{id}.pdf
    │
    ├─▶ Parse PDF → raw/papers/markdown/{id}.md
    │
    ├─▶ LLM Extract → Algorithm metadata
    │
    ├─▶ Create Wiki Pages:
    │     ├─ wiki/sources/papers/arxiv-{id}.md
    │     ├─ wiki/entities/algorithms/algo-{name}.md
    │     └─ wiki/entities/primitives/prim-{name}.md
    │
    ├─▶ Update wiki/index.md
    │
    └─▶ Sync to Neo4j (async)
```

### Query（查询）

```bash
# 搜索 Wiki
curl "http://localhost:8000/api/search?q=quantum+Fourier+transform"

# 获取页面
curl "http://localhost:8000/api/pages/prim-qft"

# 图查询
curl -X POST "http://localhost:8000/api/graph/query" \
  -H "Content-Type: application/json" \
  -d '{"query": "MATCH (a:Algorithm)-[:DEPENDS_ON]->(p:Primitive) RETURN a, p"}'
```

### Lint（健康检查）

```bash
# 运行 Lint
curl "http://localhost:8000/api/lint"

# 检查项：
# - 缺失的 frontmatter 字段
# - 孤立页面（无入站链接）
# - 损坏的 [[wiki-links]]
# - 内容矛盾（同一算法不同复杂度）
```

---

## 📊 Wiki 页面类型

| 类型 | 目录 | 示例 |
|------|------|------|
| **Concept** | `wiki/concepts/` | 量子门、纠缠、量子纠错 |
| **Entity** | `wiki/entities/` | 算法实例、原语、作者 |
| **Source** | `wiki/sources/` | 论文摘要、教程笔记 |
| **Comparison** | `wiki/comparisons/` | 算法性能对比 |

### 页面 Frontmatter 规范

```yaml
---
id: prim-qft
title: Quantum Fourier Transform
type: entity
category: primitive
tags: [transform, quantum-algorithm]
created_at: 2026-04-17
updated_at: 2026-04-17
status: published
related: [algo-shor, prim-qpe]
neo4j_synced: true
---

## Summary

Brief description...

## Definition

Mathematical definition...

## References

- [[arxiv-9508027]]
```

---

## 🖥️ Web 界面

### Wiki 浏览器 (`/wiki`)

- 页面列表（按类型分组）
- Markdown 渲染（支持 `[[wiki-links]]`）
- 页面编辑、创建
- 全文搜索
- 反向链接追踪

### 图可视化 (`/graph`)

- D3.js 力导向图
- 节点展开（1-3 跳）
- 类型过滤
- 节点详情面板

### REST API (`/api`)

```bash
# Wiki API
GET  /api/pages              # 列出所有页面
GET  /api/pages/{page_id}    # 获取单个页面
GET  /api/search?q={query}   # 搜索
POST /api/ingest/paper       # 摄入论文
GET  /api/lint               # 运行 Lint
GET  /api/stats              # Wiki 统计

# Graph API
GET  /api/graph/stats        # Neo4j 统计
GET  /api/graph/schema       # 图结构
POST /api/graph/query        # Cypher 查询
```

---

## 🛠️ 开发指南

### 运行测试

```bash
# 全部测试
pytest

# 特定模块
pytest tests/wiki/ -v
pytest tests/server/ -v
pytest tests/designer/ -v
pytest tests/codegen/ -v

# 集成测试
pytest -m integration

# 覆盖率
pytest --cov=atlas --cov-report=html
```

### 代码规范

```bash
# 格式化
black atlas tests
isort atlas tests

# 检查
ruff check atlas tests

# 类型检查
mypy atlas
```

### CLI 命令

```bash
# Paper Parser
python -m atlas.parser {arxiv_id} --wiki --sync-neo4j

# Wiki CLI
python -m atlas.wiki ingest {arxiv_id}
python -m atlas.wiki query {search_term}
python -m atlas.wiki lint --fix

# Circuit Designer
python -m atlas.designer {algorithm_id} --output circuit.json --visualize

# Code Generator
python -m atlas.codegen circuit.json --backend qiskit --output code.py

# Validator
python -m atlas.validator circuit.json

# Estimator
python -m atlas.estimator circuit.json --format markdown

# Demo Pipeline
python examples/demo_pipeline.py --algorithm qft --backend qiskit --save-code
```

---

## 📈 项目状态

| 指标 | 状态 |
|------|------|
| **Wiki 页面** | 9+ (7 primitives + index + log) |
| **核心模块** | 10/10 完成 |
| **测试** | 407 passed |
| **API 端点** | 15+ |
| **Web 模板** | 7 (Wiki + Graph) |

### 开发历程

| Issue | 功能 | 状态 |
|-------|------|------|
| #1 | Phase 1 MVP - Paper Parser + Knowledge Graph | ✅ 完成 |
| #3 | Documentation Roadmap | ✅ 完成 |
| #4 | Algorithm Extractor - LLM 提取 | ✅ 完成 |
| #5 | Circuit Designer - 电路设计 | ✅ 完成 |
| #6 | Code Generator - 代码生成 | ✅ 完成 |
| #7 | Resource Estimator - 资源估计 | ✅ 完成 |
| #8 | Validator - 电路验证 | ✅ 完成 |
| #17 | Bug Fix - Extractor 集成测试 | ✅ 完成 |
| #18 | Wiki + Web 界面 | ✅ 完成 |

### 技术路线图

| Phase | 目标 | 状态 |
|-------|------|------|
| **Phase 1** | MVP 验证：端到端链路 | ✅ 完成 |
| **Phase 2** | 规模化：50+ 算法 | 🚧 进行中 |
| **Phase 3** | 生态化：社区贡献 | 📋 规划中 |

---

## 📁 目录结构

```
QuantumAtlas/
├── atlas/                   # 核心代码 (10 模块)
│   ├── parser/              # 论文解析
│   ├── wiki/                # Wiki 引擎
│   ├── server/              # Web 服务
│   ├── knowledge/           # 知识图谱
│   ├── extractor/           # LLM 提取
│   ├── designer/            # 电路设计
│   ├── codegen/             # 代码生成
│   ├── validator/           # 电路验证
│   ├── estimator/           # 资源估计
│   └ knowledge_graph/       # 原语定义
│
├── raw/                     # Layer 1: 原始资料（不可变）
│   └ papers/
│       ├── pdf/
│       ├── markdown/
│       └ json/
│
├── wiki/                    # Layer 2: Wiki 知识库
│   ├── index.md             # 主目录
│   ├── log.md               # 活动日志
│   ├── concepts/
│   ├── entities/
│   │   ├── algorithms/
│   │   ├── primitives/      # 7 primitives
│   │   └ people/
│   ├── sources/
│   │   └ papers/
│   └ comparisons/
│
├── tests/                   # 测试套件 (27 文件)
│   ├── wiki/
│   ├── server/
│   ├── designer/
│   ├── codegen/
│   ├── validator/
│   ├── estimator/
│   └ integration/
│   └ ...
│
├── scripts/                 # 辅助脚本
│   ├── init_primitives.py
│   ├── migrate_to_wiki.py
│   └ verify_neo4j.py
│
├── examples/                # 示例代码
│   └ demo_pipeline.py
│
├── QUANTUM_ATLAS.md         # Wiki 规范文档
├── CLAUDE.md                # Claude Code 指引
├── docker-compose.yml       # Neo4j 配置
└── pyproject.toml           # 项目配置
```

---

## 🤝 贡献指南

欢迎各种形式的贡献！

### 贡献方式

1. **报告问题**：创建 Issue
2. **添加算法**：实现新的量子算法
3. **完善 Wiki**：撰写概念页面、论文摘要
4. **代码优化**：性能改进、重构

### 提交规范

```
feat: 添加新功能
fix: 修复 Bug
docs: 文档更新
refactor: 代码重构
test: 测试相关
```

---

## 📜 许可证

[MIT License](LICENSE)

---

## 🙏 致谢

本项目基于以下开源项目：

- [Neo4j](https://neo4j.com/) - 图数据库
- [FastAPI](https://fastapi.tiangolo.com/) - Web 框架
- [Pydantic](https://docs.pydantic.dev/) - 数据验证
- [Qiskit](https://qiskit.org/) - 量子计算 SDK
- [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) - 架构灵感

---

## 📮 联系方式

- **GitHub**: https://github.com/Agony5757/QuantumAtlas
- **Issues**: https://github.com/Agony5757/QuantumAtlas/issues

---

<p align="center">
  <i>构建量子算法的活文档，让知识持续增值。</i>
</p>