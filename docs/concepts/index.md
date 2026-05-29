# 概念与架构

QuantumAtlas 是一个"分层"系统。理解三层各自的职责和它们之间的数据流，是把这个项目用对的前提。

## 这一节讲什么

<div class="grid cards" markdown>

-   :material-layers-triple:{ .lg .middle } **[三层架构](three-layer.md)**

    ---

    Raw Sources / Wiki / Graph 各自是什么、谁是 source of truth、谁可以改谁不能改。

-   :material-source-branch:{ .lg .middle } **[数据流](data-flow.md)**

    ---

    一篇论文从 arXiv 进来到生成可运行代码的全链路图，以及每一步的工具。

-   :material-key-variant:{ .lg .middle } **[鉴权模型](auth-model.md)**

    ---

    PocketBase 用户、session token、PAT 与 scopes 的关系；read 开放 / write 鉴权的边界。

-   :material-database-cog:{ .lg .middle } **[存储架构](storage-architecture.md)**

    ---

    对象存储 (RustFS/S3) + Metadata DB (SQLite) + Graph (Neo4j) 三层切分，以及对象寻址、sha256 dedup、bucket versioning 等机制。

-   :material-server-network:{ .lg .middle } **[多边缘部署](multi-edge.md)**

    ---

    海外 / 国内 active-active 是怎么组的；共享什么、不共享什么；client 如何选线路。

</div>

## 为什么要分这么多层？

简单回答：**分类和关联是两回事**。

- **Raw Sources** 是证据链 —— 论文 PDF、MinerU 解析出的 Markdown、各种 JSON。它们存在的目的是「永远可追溯」，所以追加为主、几乎不删改。
- **Wiki** 是知识的 source of truth —— 经过人审阅 / LLM 辅助整理后的结构化页面。它面向「人和 LLM 都能稳定消费」。
- **Neo4j** 是从 Wiki 派生出来的关系索引 —— 回答「这个算法跟哪些原语相关」「哪些论文引用了它」。它**不是独立事实来源**，而是「Wiki 投影到图模型上的副本」。

这样的边界有两个好处：

1. 让研究笔记可以**被人读**，也能**被工具稳定消费**。
2. 让关系查询**不污染正文**；分类、叙述和图查询各在合适的地方发生。

下面的几节会把每一层讲清楚。
