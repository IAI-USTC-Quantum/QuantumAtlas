# OpenAlex 语料 schema 分两阶段：base 表启动即建，重索引 CONCURRENTLY 且可门控

_细化 ADR `0006`（语料是 boot-created、懒加载写穿缓存）与 ADR `0010`（语料物理 schema）在
**生产规模**下的启动建 schema 行为。术语见[术语表](../reference/glossary.md)。_

`openalex_works` 到生产规模（~10⁸ 行 / 353 GB）后，暴露一个只在**大表 + 有并发写**时才显形的
启动缺陷：`ensureCatalogSchema` 在 boot 时无条件把**全部** DDL（含非 `CONCURRENTLY` 的
`record` GIN、citation-array GIN、tsvector GIN，以及若干 btree）逐条 `Exec`。在 353 GB 上，
单个 GIN 建索引远超 90s 单次预算 → 超时；更糟的是非 `CONCURRENTLY` 的 `CREATE INDEX` 持有表级
**SHARE 锁**，它**挡住懒加载的 fetch-on-miss 写回**（`INSERT … ON CONFLICT` 的 `RowExclusive`
与 `SHARE` 冲突）、饱和磁盘 I/O、并与正在跑的 operator bootstrap 抢资源；10 次重试全部超时后放弃。
实测：按 id lookup 的**读**始终正常（SHARE 允许读），但**写回**在建索引窗口内超时——同步懒加载
被 schema-ensure 自己挡住。这是启动建 schema 的设计缺陷，不是懒加载代码的 bug。

## 决策

- **把 `EnsureSchema` 拆成 base 与 index 两组**（`internal/openalexcorpus`）：
  - **`EnsureSchema`（base，boot 关键路径）** 只建函数、表、约束、以及**小**审计表索引。对一张
    已存在的 353 GB 表，`CREATE TABLE IF NOT EXISTS` 是 **no-op**（永不 rewrite），审计索引在空表上
    也廉价——所以 base 在每次 boot 都快且安全。
  - **`EnsureIndexes`（重索引，独立阶段）** 建 `openalex_works` 上的 11 个重索引。

- **重索引一律 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`。** `CONCURRENTLY` 只拿
  `ShareUpdateExclusive` 锁（**允许并发读 + 写**），所以懒加载写回和 bootstrap 全程不被挡——代价是
  更慢 + 更重 I/O，因此单列成阶段并可门控。两个必须处理的工程细节：
  - **simple protocol**：`CONCURRENTLY` 不能跑在事务块里；pgx 的扩展协议会包一层隐式事务被 PG 拒绝，
    故这些语句走 `pgx.QueryExecModeSimpleProtocol`。
  - **INVALID 残留清理**（`CONCURRENTLY` 的已知坑）：中断的并发建索引会留下一个 `indisvalid=false`
    的坏索引，而 `IF NOT EXISTS` 会永远跳过它 → 索引永远坏着。故建前先查 `pg_index.indisvalid`，
    只 `DROP INDEX CONCURRENTLY` 掉**无效**的再重建；有效的 / 不存在的都不动。逐个 best-effort，
    一个失败不拖累其余，合并错误返回让调用方下次 boot 续建。

- **门控开关 `QATLAS_CORPUS_ENSURE_INDEXES`（默认 `true`）。** 一台 edge 指向**已预置好索引**的
  共享 corpus 时设 `false`：base schema 仍在 boot 时保证，但跳过重索引的 `CONCURRENTLY` 建过程。
  默认 `true` 保持既有单节点 / 新库"自建索引"的行为不变（空表上并发建索引很快）。

- **boot 分两阶段**（`cmd/qatlasd ensureCatalogSchema`）：阶段 1 base（90s × 10 重试，快，收敛廉价）；
  base 成功后，若开关开则进阶段 2——在 12 h 预算内 `CONCURRENTLY` 建重索引（幂等 + 逐个 best-effort，
  超时/中断只是下次 boot 续建剩余的）。operator 的 `openalex bootstrap-pg` 在批量灌完后也调用
  `EnsureIndexes`——这正是"预置共享 corpus"该建索引的地方。

## 后果

- `internal/openalexcorpus/schema.go`：`schemaStatements` 拆为 `baseSchemaStatements` +
  `corpusIndexes`（`[]indexDef{name,ddl}`，全部 `CONCURRENTLY IF NOT EXISTS`）；`EnsureSchema`
  只跑 base；新增 `EnsureIndexes` + `ensureOneIndex` + `dropIfInvalid`（simple protocol）。
- `internal/config`：新增 `CorpusEnsureIndexes bool`（`QATLAS_CORPUS_ENSURE_INDEXES`，默认 true）。
- `cmd/qatlasd/main.go`：`ensureCatalogSchema` 改两阶段签名（多一个 `ensureIndexes bool`）。
- `cmd/qatlasd/openalex_cmd.go`：`bootstrap-pg` 灌完后建索引（`EnsureIndexes`）。
- **对外行为不变**：按 id lookup / 懒加载写穿 / 语料查询语义都不变——只是重索引不再在 boot 时
  用 SHARE 锁挡写。指向大型共享 corpus 的 edge 现在可用 `QATLAS_CORPUS_ENSURE_INDEXES=false`
  安全接入，boot 不再 thrash。
- 单测：base 不含任何 `openalex_works` 索引、每个 `corpusIndexes` 都是 `CONCURRENTLY`＋`IF NOT EXISTS`
  的不变量测试；config 默认/关闭测试；`EnsureIndexes` nil-pool 降级测试。

## 备选方案与否决理由

- **加一个"整体跳过 EnsureSchema"开关**（`QATLAS_ENSURE_SCHEMA=false`）：否决。太粗——base 表 /
  约束关系到**正确性**（唯一约束、FK、生成列），任何情况下都该保证；真正的问题只是那几个**重索引**
  在大表上非并发地建。拆 base / index 让"必须建的"和"可跳过的"各自独立，比一刀切精准。
- **重索引保持非 `CONCURRENTLY`，只把单次超时调大**：否决。SHARE 锁仍会挡懒加载写回 + bootstrap；
  且超时/中断后留下的坏状态是"下次又从头建"，不是续建。
- **把建索引放到前台 boot-block（等建完再 serve）**：否决。会把 `/api/health` 阻塞数小时。
- **`CONCURRENTLY` 但不处理 INVALID 残留**：否决。一次中断就会留下 `IF NOT EXISTS` 永远跳过的坏索引，
  索引静默地永久缺失。
