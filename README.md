# QuantumAtlas 🧭

> **Agentic AI 驱动的量子算法实现框架**
> 
> 将 Quantum Algorithm Zoo 中 400+ 算法的"纸上描述"转化为可执行的量子电路代码和标准化资源估计报告。

[![Python 3.10+](https://img.shields.io/badge/python-3.10+-blue.svg)](https://www.python.org/downloads/)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-green.svg)](https://opensource.org/licenses/Apache-2.0)
[![Neo4j](https://img.shields.io/badge/Neo4j-008CC1?style=flat&logo=neo4j&logoColor=white)](https://neo4j.com/)

---

## 🎯 核心目标

QuantumAtlas 是一个系统性的量子算法实现框架，通过 Agentic AI 技术栈，实现从**研究论文**到**可执行代码**的自动化转换：

| 目标 | 描述 |
|------|------|
| **📚 论文解析** | 从 arXiv 自动下载并解析量子算法论文 |
| **🧠 知识提取** | 使用 LLM 提取算法元数据、伪代码、复杂度分析 |
| **🕸️ 知识图谱** | 构建量子原语与算法的关联图谱（Neo4j） |
| **⚡ 电路生成** | 自动生成 QPanda/Qiskit 可执行代码 |
| **✅ 验证测试** | 验证电路正确性与等价性 |
| **📊 资源估计** | 标准化门数量、深度、量子比特需求报告 |

**最终愿景**：建立量子算法的"活文档"——持续更新的可执行算法库，为量子计算研究者和开发者提供即用即跑的工具集。

---

## 🗺️ 技术路线图

### Phase 1: MVP 验证 ✅（当前）
目标：打通端到端链路，证明可行性

- [x] 搭建 Neo4j 知识图谱环境
- [x] 人工定义初始原语骨架（7 个顶级 Primitive）
- [x] 实现 Paper Parser（arXiv 下载 + PDF 解析）
- [x] 实现 Knowledge Graph 数据模型
- [x] 实现 Algorithm Extractor（LLM 信息提取）
- [x] 实现 Circuit Designer + Qiskit Code Generator
- [x] 集成 Validator + Resource Estimator
- [ ] 用 3-5 个简单算法（Shor、Grover 等）测试全链路

### Phase 2: 规模化（50+ 算法）
目标：建立自动化流水线，覆盖核心算法类别

- [ ] 扩展 Primitive 模板库（覆盖 6 大数学问题类别）
  - 数论问题（因数分解、离散对数）
  - 代数问题（群论、线性代数）
  - 优化问题（QAOA、量子退火）
  - 模拟问题（哈密顿量模拟）
  - 机器学习（量子机器学习算法）
  - 搜索问题（Grover 变体）
- [ ] 搭建 Paper Harvesting 流水线（arXiv quant-ph 定时爬取）
- [ ] 实现 LLM 批量抽取（算法 → Primitive → 实现难度评估）
- [ ] 建立增量更新机制（新论文自动触发实现流程）
- [ ] 开发 Web 界面（知识图谱可视化、进度看板）

### Phase 3: 生态化（平台化）
目标：建立可持续的开源生态

- [ ] 开源完整工具链
- [ ] 建立模块化合规追溯系统
- [ ] 支持社区贡献算法实现（PR 流程、审核机制）
- [ ] 发布标准化 Benchmark 数据集
- [ ] 与 [Quantum Algorithm Zoo](https://quantumalgorithmzoo.org/) 官方合作
- [ ] 支持多后端代码生成（QPanda、Qiskit、Cirq、PennyLane）

---

## 🏗️ 项目架构

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         QuantumAtlas 架构                               │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐              │
│  │   Input      │───▶│  Processing  │───▶│    Output    │              │
│  │   Layer      │    │    Layer     │    │    Layer     │              │
│  └──────────────┘    └──────────────┘    └──────────────┘              │
│         │                   │                   │                      │
│         ▼                   ▼                   ▼                      │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐              │
│  │ Paper Parser │    │ Algorithm    │    │   Code       │              │
│  │              │    │ Extractor    │    │   Generator  │              │
│  │ • arXiv      │───▶│              │───▶│              │              │
│  │   Fetcher    │    │ • LLM        │    │ • QPanda     │              │
│  │ • PDF        │    │ • Metadata   │    │ • Qiskit     │              │
│  │   Parser     │    │ • Pseudocode │    │ • Cirq       │              │
│  └──────────────┘    └──────────────┘    └──────────────┘              │
│         │                   │                   │                      │
│         ▼                   ▼                   ▼                      │
│  ┌─────────────────────────────────────────────────────────┐          │
│  │              Knowledge Graph (Neo4j)                    │          │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐              │          │
│  │  │Primitive │──│Algorithm │──│ Paper    │              │          │
│  │  │   Node   │  │   Node   │  │  Node    │              │          │
│  │  └──────────┘  └──────────┘  └──────────┘              │          │
│  │         │             │            │                   │          │
│  │         └─────────────┴────────────┘                   │          │
│  │                       │                                │          │
│  │                       ▼                                │          │
│  │              ┌──────────────────┐                      │          │
│  │              │  Relationships   │                      │          │
│  │              │ (DEPENDS_ON,     │                      │          │
│  │              │  PUBLISHES, ...) │                      │          │
│  │              └──────────────────┘                      │          │
│  └─────────────────────────────────────────────────────────┘          │
│                                                                         │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐              │
│  │   Circuit    │    │  Validator   │    │  Estimator   │              │
│  │   Designer   │───▶│              │───▶│              │              │
│  │              │    │ • Correctness│    │ • Gate Count │              │
│  │ • Primitive  │    │ • Equivalence│    │ • Depth      │              │
│  │   Composition│    │ • Testing    │    │ • Qubits     │              │
│  └──────────────┘    └──────────────┘    └──────────────┘              │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### 模块说明

| 模块 | 职责 | 状态 |
|------|------|------|
| `atlas/parser/` | arXiv 论文下载、PDF 解析 | ✅ 已实现 |
| `atlas/knowledge/` | Neo4j 客户端、数据模型 | ✅ 已实现 |
| `atlas/extractor/` | LLM 信息提取 | ✅ 已实现 |
| `atlas/designer/` | 电路设计器 | ✅ 已实现 |
| `atlas/codegen/` | 代码生成器 | ✅ 已实现 |
| `atlas/validator/` | 电路验证器 | ✅ 已实现 |
| `atlas/estimator/` | 资源估计器 | ✅ 已实现 |

---

## 🚀 快速开始

### 环境要求

- Python 3.10+
- Docker & Docker Compose
- 4GB+ RAM（用于 Neo4j）

### 1. 克隆仓库

```bash
git clone https://github.com/Agony5757/QuantumAtlas.git
cd QuantumAtlas
```

### 2. 配置环境

```bash
# 创建虚拟环境
python -m venv .venv
source .venv/bin/activate  # Windows: .venv\Scripts\activate

# 安装依赖
pip install -e ".[dev]"

# 设置 Neo4j 密码（必须）
export NEO4J_PASSWORD="quantum-atlas"  # Windows: set NEO4J_PASSWORD=quantum-atlas
```

### 3. 启动 Neo4j

```bash
# 启动 Neo4j 容器
docker-compose up -d

# 等待服务就绪（约 30 秒）
sleep 30

# 验证连接
python scripts/verify_neo4j.py
```

Neo4j Browser 访问：http://localhost:7474
- 用户名：`neo4j`
- 密码：`quantum-atlas`

### 4. 初始化知识图谱

```bash
# 导入预定义的量子原语
python scripts/init_primitives.py
```

### 5. 测试 Paper Parser

```bash
# 下载并解析论文（以 Shor 算法为例）
python -m atlas.parser 9508027 -m -j --import-to-neo4j

# 输出：
# ✅ Title: Polynomial-Time Algorithms for Prime Factorization...
# ✅ Authors: Peter W. Shor
# ✅ PDF saved to: ./papers/9508027.pdf
# ✅ Markdown saved to: ./papers/9508027.md
# ✅ Paper imported to Neo4j
```

### 6. 验证知识图谱

打开 Neo4j Browser (http://localhost:7474)，执行 Cypher 查询：

```cypher
// 查看所有原语
MATCH (p:Primitive) RETURN p.name, p.category

// 查看所有论文
MATCH (p:Paper) RETURN p.title, p.arxiv_id

// 查看原语依赖关系
MATCH (a:Algorithm)-[:DEPENDS_ON]->(p:Primitive) 
RETURN a.name, collect(p.name) as primitives
```

### 7. 运行演示（无需 API Key）

```bash
# 运行 Bell State 演示（Qiskit 后端）
python examples/demo_pipeline.py --algorithm bell_state --backend qiskit

# 运行 Grover 搜索演示（QPanda 后端）
python examples/demo_pipeline.py --algorithm grover --backend qpanda --save-code

# 输出示例：
# ============================================================
#   Step 1: Circuit Design
# ============================================================
# Creating circuit with 2 qubits...
# ✓ Circuit created: Bell State Preparation
#   Qubits: 2
#   Gates: 2
#   Depth: 2
```

### 8. 端到端管道（需要 API Key）

```bash
# 设置 LLM API Key
export OPENAI_API_KEY="your-api-key"  # 或 ANTHROPIC_API_KEY

# 完整管道：论文下载 → 提取 → 设计 → 生成代码
python -m atlas.parser 9508027 -m -j --import-to-neo4j
python -m atlas.extractor 9508027 --output algorithm.yaml
python -m atlas.designer algorithm.yaml --output circuit.json --visualize
python -m atlas.codegen circuit.json --backend qiskit --output shor.py
python -m atlas.validator circuit.json
python -m atlas.estimator circuit.json --format markdown --output report.md
```

---

## 📁 项目结构

```
QuantumAtlas/
├── atlas/                          # 核心代码包
│   ├── __init__.py
│   ├── parser/                     # 论文解析模块 ✅
│   │   ├── arxiv_fetcher.py       # arXiv 下载
│   │   ├── pdf_parser.py          # PDF → Markdown
│   │   └── __main__.py            # CLI 入口
│   ├── knowledge/                  # 知识图谱模块 ✅
│   │   ├── neo4j_client.py        # Neo4j 操作
│   │   └── models.py              # Pydantic 模型
│   ├── extractor/                  # LLM 提取模块 ✅
│   │   ├── llm_interface.py       # LLM 接口（OpenAI/Claude）
│   │   ├── extractor.py           # 提取器主类
│   │   └── algorithm_ir.py        # 算法 IR 模型
│   ├── designer/                   # 电路设计 ✅
│   │   ├── designer.py            # 电路设计器
│   │   ├── quantum_circuit.py     # 量子电路模型
│   │   └── quantum_ir.py          # 量子 IR
│   ├── codegen/                    # 代码生成 ✅
│   │   ├── generator.py           # 代码生成器
│   │   ├── qiskit_generator.py    # Qiskit 后端
│   │   └── qpanda_generator.py    # QPanda 后端
│   ├── validator/                  # 电路验证 ✅
│   │   ├── validator.py           # 验证器
│   │   └── equivalence_checker.py # 等价性检查
│   ├── estimator/                  # 资源估计 ✅
│   │   ├── estimator.py           # 估计器
│   │   └── resource_analyzer.py   # 资源分析
│   └── knowledge_graph/            # 知识图谱定义
│       ├── schemas/               # 节点/关系 Schema
│       └── primitives/            # 原语 YAML 定义
├── tests/                          # 测试套件
│   ├── integration/               # 集成测试
│   └── ...                        # 单元测试
├── examples/                       # 示例脚本
│   └── demo_pipeline.py           # 端到端演示
├── scripts/                        # 辅助脚本
│   ├── verify_neo4j.py            # 连接验证
│   └── init_primitives.py         # 初始化原语
├── papers/                         # 下载的论文（gitignore）
├── docker-compose.yml              # Neo4j Docker 配置
├── pyproject.toml                  # 项目配置
└── README.md                       # 本文档
```

---

## 🔬 核心概念

### 量子原语（Primitives）

原语是量子算法的基本构建块，类似于经典编程中的库函数：

| 原语 | 类别 | 复杂度 | 应用 |
|------|------|--------|------|
| QFT | 变换 | O(n²) gates | Shor 算法、QPE |
| QPE | 变换 | O(t²) | 哈密顿量模拟 |
| Block Encoding | 状态准备 | O(poly log N) | HHL 算法 |
| Amplitude Amplification | Oracle | O(√N) | Grover 搜索 |
| Hamiltonian Simulation | 模拟 | O(t poly log N) | 量子化学 |

### 知识图谱模型

```
(Paper)-[:PUBLISHES]->(Algorithm)-[:DEPENDS_ON]->(Primitive)
    |
    └-[:CITES]->(Paper)

(Algorithm)-[:IMPLEMENTED_AS]->(Implementation)
```

---

## 🛠️ 开发指南

### 运行测试

```bash
# 运行所有测试
pytest

# 运行特定模块测试
pytest tests/parser/ -v

# 运行集成测试（需要网络）
pytest -m integration
```

### 代码规范

```bash
# 格式化
black atlas tests

# 检查
ruff check atlas tests

# 类型检查
mypy atlas
```

### 添加新原语

1. 在 `atlas/knowledge_graph/primitives/` 创建 YAML 文件
2. 运行 `python scripts/init_primitives.py` 导入
3. 添加对应的单元测试

---

## 🤝 贡献指南

我们欢迎各种形式的贡献！

### 贡献方式

1. **报告 Bug**：创建 Issue 描述问题
2. **提交算法实现**：实现新的量子算法
3. **改进文档**：完善 README、添加教程
4. **代码优化**：性能改进、重构

### 贡献流程

1. Fork 本仓库
2. 创建功能分支：`git checkout -b feature/xxx`
3. 提交更改：`git commit -m "feat: xxx"`
4. 推送分支：`git push origin feature/xxx`
5. 创建 Pull Request

### 代码规范

- 使用 Python 3.10+ 类型注解
- 遵循 PEP 8 规范
- 所有函数必须有文档字符串
- 新功能必须包含测试

---

## 📊 项目状态

| 指标 | 状态 |
|------|------|
| 已定义原语 | 7 个 |
| 已实现模块 | 2/7 |
| 测试覆盖率 | 进行中 |
| 文档完成度 | 80% |

---

## 📜 许可证

本项目采用 [Apache License 2.0](LICENSE) 开源许可证。

---

## 🙏 致谢

QuantumAtlas 基于以下开源项目构建：

- [Neo4j](https://neo4j.com/) - 图数据库
- [PyMuPDF](https://pymupdf.readthedocs.io/) - PDF 解析
- [Pydantic](https://docs.pydantic.dev/) - 数据验证
- [QPanda](https://github.com/OriginQ/QPanda) - 量子计算框架
- [Qiskit](https://qiskit.org/) - IBM 量子计算 SDK

---

## 📮 联系我们

- 项目主页：https://github.com/Agony5757/QuantumAtlas
- Issue 追踪：https://github.com/Agony5757/QuantumAtlas/issues
- 开发路线图：见 [Issue #2](https://github.com/Agony5757/QuantumAtlas/issues/2)

---

<p align="center">
  <i>用量子计算的力量，探索计算的边界。</i>
</p>
