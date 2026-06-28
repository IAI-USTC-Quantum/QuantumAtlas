# QuantumAtlas

<div class="hero-tagline" markdown>
**把量子算法论文从「PDF 和笔记」推进到「可查询的知识、可浏览的 Wiki、可同步的图谱，以及可生成的实现代码」。**
</div>

QuantumAtlas 是一个面向量子算法研究的**分层知识库 + 实现工作台**。它把论文摄入、Wiki 沉淀、图谱同步、电路设计、代码生成、验证和资源估计串成一条可持续迭代的链路。

核心想法：**分类和关联是两回事**。Raw Sources 保留证据，Wiki 是被审阅后的知识 source of truth，Neo4j 图谱回答「它与什么有关」。

```mermaid
flowchart LR
    A[论文 / 资料] -->|fetch + parse| R[Raw Sources<br/>PDF + Markdown + JSON]
    R -->|人工 + LLM 整理| W[Wiki<br/>结构化页面]
    W -->|sync| G[Neo4j Graph<br/>实体关系]
    W & G -->|extract IR| C[Quantum IR / 电路代码]
    C -->|verify + estimate| O[可运行 + 可估计的实现]
```

---

## 文档导航

文档按两个组件 + 共享基础组织：**Python 客户端 (`qatlas`)** 与 **Go 服务端 (`qatlasd`)** 各成一节，概念、参考、入门、贡献为两者共享。

<div class="grid cards" markdown>

-   :material-rocket-launch:{ .lg .middle } **入门**

    ---

    装 client、跑一个不依赖外部服务的 demo、5 分钟看完核心数据流。

    [:octicons-arrow-right-24: 入门指南](getting-started.md)

-   :material-book-open-page-variant:{ .lg .middle } **概念与架构**

    ---

    三层模型、数据流动、对象寻址、鉴权语义、多边缘部署。

    [:octicons-arrow-right-24: 概念](concepts/index.md)

-   :material-language-python:{ .lg .middle } **Python 客户端 `qatlas`**

    ---

    上传论文、写 Wiki 页面、跑 MinerU、生成电路代码、管理凭据。

    [:octicons-arrow-right-24: Python 客户端](client/index.md)

-   :material-server:{ .lg .middle } **Go 服务端 `qatlasd`**

    ---

    安装、systemd、反向代理、OAuth、Neo4j、RustFS、REST API、健康检查、备份升级。

    [:octicons-arrow-right-24: Go 服务端](server/index.md)

-   :material-file-tree:{ .lg .middle } **参考 / 数据格式**

    ---

    环境变量、Wiki schema、arXiv ID 格式等跨组件的稳定约定。

    [:octicons-arrow-right-24: 参考](reference/index.md)

-   :material-hand-heart:{ .lg .middle } **贡献**

    ---

    代码、文档、Wiki 内容、发布流程。

    [:octicons-arrow-right-24: 贡献指南](contributing.md)

</div>

---

## 核心能力

- **从 arXiv 摄入论文**：自动抓取 PDF + 元数据，可选用 MinerU 解析为 Markdown
- **沉淀知识到 Wiki**：可审阅的 Markdown + YAML frontmatter，统一 `concept` 词条（按 `category` 细分）+ `source` 论文引用
- **同步到 Neo4j 图谱**：从 Wiki 派生算法 / 原语 / 论文 / 人物的关系网
- **从算法走到代码**：Designer → Quantum IR → Qiskit/QPanda → Validator → Estimator
- **远程协作**：Web API + CLI + 分享链接，协作者不需要服务器登录权限
- **多边缘 active-active**：海外 / 国内多线路部署，跨地域共享同一份知识库

## 当前状态

!!! info "Alpha 阶段，主线已贯通"

    - 摄入、Wiki、图谱、设计、代码生成、验证、估计 **全链路打通**。
    - Web API、分享链接、远程协作流程 **可用**。
    - 项目定位是「可持续扩展的研究基础设施」，而不是已经产品化的平台——意味着稳定但仍在快速演化。

## 仓库 & 包

- :material-github: 源码：<https://github.com/IAI-USTC-Quantum/QuantumAtlas>
- :material-language-python: PyPI：[`quantum-atlas`](https://pypi.org/project/quantum-atlas/)
- :material-server-network: 生产入口：<https://quantum-atlas.ai>
- :material-license: 协议：[Apache-2.0](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
