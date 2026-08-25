# 概念与架构

QuantumAtlas 是一个"分层"系统。理解各层各自的职责和它们之间的数据流，是把这个项目用对的前提。

## 本节内容

<div class="grid cards" markdown>

-   :material-layers-triple:{ .lg .middle } **[分层架构](architecture.md)**

    ---

    对象存储 / PostgreSQL registry / 搜索引擎各自是什么、谁是 source of truth、谁可以改谁不能改。

-   :material-source-branch:{ .lg .middle } **[数据流](data-flow.md)**

    ---

    一篇论文从 arXiv 进来到可被搜索的全链路图，以及每一步的工具。

-   :material-key-variant:{ .lg .middle } **[鉴权模型](auth-model.md)**

    ---

    PocketBase 用户、session token、PAT 与 scopes 的关系；read / write 鉴权的边界。

-   :material-database-cog:{ .lg .middle } **[存储架构](storage-architecture.md)**

    ---

    对象存储 (RustFS/S3) + PostgreSQL registry 的切分，以及对象寻址、sha256 dedup、bucket versioning、桶布局与对账等机制。

</div>

## 为什么分层

简单回答：**字节和查询是两回事**。

- **对象存储** 是证据链 —— 论文 PDF、MinerU 解析出的 Markdown、各种图片。它们存在的目的是「永远可追溯」，所以追加为主、几乎不删改。
- **PostgreSQL** 是 registry —— 论文、身份、资产状态、OpenAlex 语料。它回答「有哪些论文、各自什么状态、彼此什么关系」，用纯 SQL 就能查。
- **搜索引擎** 是派生查询层 —— `POST /api/search` 把查询 fan-out 到 catalog / arXiv / OpenAlex / Qdrant provider。它**不是独立事实来源**。

这样的边界有两个好处：

1. 资产可以**被人直接取**，也能**被工具稳定消费**。
2. 集合查询**不污染存储层**；存储、索引和搜索各在合适的地方发生。

下面的几节会把每一层讲清楚。
