# PostgreSQL 中心化关系库（catalog + OpenAlex 语料），不用 MySQL

Paper catalog 已从 Neo4j 迁到关系库（`b1e37fa`）。此外，我们还希望在本地持有一个
**OpenAlex works 语料库**，而不是每次都打有速率限制的公开 API；这样引用上下文、
批量分析，以及和我们自己的 arXiv assets 做 vector joins，都能用普通 SQL 完成。一个关系引擎同时服务二者。
我们选择 **PostgreSQL**（通过 `pgx`），并把它部署为 **mesh 上单个中心化实例**，与 RustFS 和 Neo4j 共址；
两台边缘节点都通过 mesh 连接它，就像它们已经连接 RustFS 和 Neo4j 一样。host core 的 `paper_works`
catalog 与 OpenAlex 语料位于**同一个数据库的不同表**中，因此 deep join（corpus × asset status × vectors）
就是一条查询。

## 为什么是 PostgreSQL，不是 MySQL

这个工作负载正落在 PostgreSQL 最强、MySQL 最弱的关系型场景里：

- **Raw JSON，只过滤，绝不修改。** OpenAlex records 原样以 `jsonb` 存储；hot fields 通过
  generated columns 和 GIN / expression indexes 暴露，**不重写 record**。PostgreSQL `jsonb`
  （binary，`@>`/`->>`，partial + expression indexes）远强于 MySQL 的 JSON type。
- **向量 join。** 领域子集 embeddings 通过 `pgvector`（HNSW / IVFFlat）放在同一数据库中。
  MySQL 没有可比的 indexed-vector 支持。
- **Citation + collection queries。** Recursive CTEs、partial unique / queue indexes、row-lock
  leases、`count(*) FILTER (...)`、窗口函数都是一等能力（见
  [`architecture.md` #paperindex](../concepts/architecture.md#paperindex)）。

MySQL 的真实优势——极高写吞吐（InnoDB / MyRocks）、高并发下 thread-per-connection、Vitess
horizontal sharding、更平缓的运维学习曲线——都面向高并发 OLTP 服务和 sharded scale-out。
这个 store 是**内部的、读多写少的、单实例的、永远不作为 outbound API 暴露的，并且明确不分片**
（catalog ≈ 10⁵ rows；OpenAlex works ≈ 2.87 × 10⁸ rows / ~1–2 TB，单节点完全够用）。
MySQL 的这些优势都不适用；而它的 sharding 王牌又与“一个数据库，对 catalog + corpus + vectors
做 deep joins”这个驱动整套设计的要求互斥。

## 备选方案

- **MySQL** — 否决（见上）。它的优势面向我们没有的 profile（sharded、高并发服务）；
  `jsonb`、`pgvector` 和 CTE 支持都更弱。
- **不要关系库（只保留 object-store + Neo4j）** — 否决：object storage 只能回答 point
  `GetObject` / `ListObjects`，无法做 collection queries、counts、partial-unique DOI constraints，
  或 atomic MinerU leases（`architecture.md` #paperindex）。Neo4j 保留，但只作为（可选）graph
  插件背后的 **citation graph**——一个可重建的派生视图，而不是权威源。

## 影响

- 一个中心化 PostgreSQL 成为与 RustFS、Neo4j 并列的共享 mesh dependency。它不可达时，边缘节点
  会**优雅降级**：asset writes 仍返回 `201` 并设置 `X-Catalog-Sync: deferred`；reads 报告
  `availability=false`。（`storage-architecture.md` 里更早的“PG per edge”草图只是愿景——PG
  加入其他中心化后端。）
- **Scope（推荐）：full metadata，subset vectors。** 完整的 ~2.87 × 10⁸-work metadata
  corpus 以 raw `jsonb` 存储，因此一旦某个 work 被缓存，它命名的每个 `referenced_works` id
  都能**本地**解析——cached works 上的 citation traversal 不会再打公开 API。Vectors
  **只在 domain subset 上构建**；full-corpus embeddings（~3 TB + 大量 embedding compute）明确不在范围内。
- **corpus 是 boot-created、lazily-populated write-through cache**，不是只由 operator 批量装载的表。
  `openalex_works`（+ sync-state + audit）在 server boot 时创建；按 id lookup 如果在 corpus 中
  **misses**，会按需从 OpenAlex API 拉取这个单独的 work 并写回（fetch-on-miss write-through）。
  因此 miss 会自愈，而不是永久 fallback；批量 `openalex bootstrap-pg` snapshot load 变成**可选
  pre-warm**（用于 citation-traversal 完整性 + vector coverage），不再是前置条件。这也意味着
  `papers.paper_openalex_id` 可以带指向 `openalex_works` 的 FK，而不引入启动顺序依赖
  （ADR `0009`）。
- OpenAlex corpus **只过滤，绝不修改**：存储的 record 忠实于 snapshot；每个 derived field
  都是 generated column，而不是 destructive transformation。
- `paper_works` 与 OpenAlex corpus 是**同一个数据库中的不同表**——语义上不同（一个是 10⁵-row、
  可从 bucket 重建的 host-core index；另一个是 10⁸-row external reference corpus），但共址，
  因此 deep joins 仍留在 SQL 里。
- Sync：corpus 通过 OpenAlex snapshot（公开节奏为季度）刷新，只拉取新的 `updated_date`
  partitions，并用单行 `openalex_sync_state` watermark 跟踪（ADR `0010`），而不是每行一个
  `updated_date` column。
- **Physical schema 由 ADR `0010`（corpus）和 ADR `0009`（catalog）细化。** corpus 的 citation
  edges——最初作为 `work_referenced` 表交付——变成 inline generated
  `openalex_referenced_work_ids` `jsonb` column + GIN，cited-by 通过 reverse-lookup 提供；
  append-only `openalex_audit` table + structural views 覆盖 snapshot fidelity。`paper_works`
  catalog 被重构为 surrogate-keyed `papers` + child `paper_assets`（ADR `0009`）。**这里**的决策——
  PostgreSQL 优先于 MySQL、一个中心化 store、raw `jsonb` “only filtered, never modified”、
  full metadata + subset vectors——不变；演进的只有 table shapes。**大规模（10⁸ 行 / 353 GB）下的
  启动建 schema 行为由 ADR `0013` 细化**：base 表 boot 即建、`openalex_works` 的重索引改
  `CONCURRENTLY` 并由 `QATLAS_CORPUS_ENSURE_INDEXES` 门控，避免非并发建索引在 boot 时用 SHARE 锁
  挡住懒加载写回。
