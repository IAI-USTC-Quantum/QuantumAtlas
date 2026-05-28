# QuantumAtlas

> 把量子算法论文从「PDF 和笔记」推进到「可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码」。

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

每个页面带 YAML frontmatter（`id`、`title`、`type`、`status` 等必填），文件名遵循 `algo-*` / `prim-*` / `person-*` / `paper-arxiv-*` / `comp-*` 等前缀规范。完整模板、frontmatter schema、lint 错误码和同步规则见 [Wiki 约定](wiki-conventions.md)。

## 它能做什么

- 从 arXiv 获取论文，下载 PDF 并解析为 Markdown。
- 把知识沉淀到可审阅的 Markdown Wiki，而不是散落在脚本和临时笔记里。
- 将 Wiki 和结构化信息同步到 Neo4j，用图谱回答“它和什么有关”。
- 从算法定义继续往下走到 Quantum IR、Qiskit/QPanda 代码、验证和资源估计。
- 通过 Web API、CLI 和分享链接支持远程协作，而不要求所有协作者都能直接登录服务器。

## 当前状态

项目处于 alpha 阶段，但主线骨架已经完整：

- 论文摄入、Wiki、图谱、设计、代码生成、验证、估计都已打通。
- Web API、分享链接和远程协作流程可用。
- 项目更像“可持续扩展的研究基础设施”，而不是已经产品化的平台。

## 文档导航

- [入门](getting-started.md): 装 server + client、跑 demo、起本地 Web 服务、常用命令一览。
- [架构](architecture.md): 项目的分层模型、source of truth、Wiki/Raw/Neo4j 边界，以及协作方式。
- 贡献与协作
    - [Contribution workflow](contribution-workflow.md): Raw 贡献的三条路径、鉴权、Wiki Git 协作与服务器同步的完整 how-to。
    - [Upload API](upload-api.md): `qatlas upload …` / `POST /api/papers/.../upload-pdf` 完整 API 参考（sha256 dedup、idempotent retry、in-transit guard、conflict 处理）。
    - [Wiki conventions](wiki-conventions.md): Wiki 页面类型、模板、frontmatter schema、lint 规则与同步约定。
- 部署与运维
    - [Deployment](deployment.md): 本地启动、单机部署、systemd、环境变量、反向代理与鉴权示例。
    - [Storage design](storage-design.md): Raw / Metadata / Graph 三层存储如何切分、为什么这样设计、对未来扩展的留口。
    - [Storage / RustFS ops](storage-rustfs.md): qatlas ↔ RustFS 集成 ops 指南（env vars、IAM policy、bucket versioning 自管、`qatlas-server storage prune` 使用、故障排查）。
    - [Migration: storage layout](migration-storage-layout.md): 状态目录从仓库内迁到 XDG 路径的迁移指南。
- [开发](development.md): 开发命令、API 概览、仓库结构、测试与发版流程。
- 参考
    - [图谱可视化调研](graph-visualization-research.md): 前端选型调研（待实现）。
    - [Python legacy server](python-legacy.md): 已不是生产路径的旧 FastAPI 入口，仅作本地兼容测试参考。
- [关于](about.md): 致谢、贡献方式、许可证、GitHub 链接。
