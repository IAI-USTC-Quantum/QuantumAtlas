# OpenAlex works 内联引用（弃 work_referenced 边表）+ 审计

_细化 ADR `0006` 中 OpenAlex works 存储的物理模式；其中"完整元数据保存在本地，只过滤、不修改"的决策不变。_

ADR `0006` 将引用边作为独立的 `work_referenced(work_id, referenced_id)` 表发布，并草拟了一个存储的
被引概念。梳理 corpus（grill Q8–Q16）后发现两者都可以避免：原始 `record` 已经包含
`referenced_works`，而 PostgreSQL 的 `jsonb` + GIN 可以基于一个生成列服务*两个*引用方向，并且**零维护**。
我们还补上 ADR `0006` 中隐含未写的刷新水位线和审计界面。

## 决策

**内联引用，不设边表（Q8）。**

- `openalex_referenced_work_ids jsonb GENERATED ALWAYS AS (strip_openalex_prefix(record->'referenced_works')) STORED`
  —— 裸 `W…` ids。生成列不能子查询/聚合，所以前缀剥离封装进一个 **`IMMUTABLE` plpgsql
  `strip_openalex_prefix()`** 函数（Q12）。在其上构建 **GIN** 索引。
- **删除 `work_referenced`。** 正向（"W 引用了什么"）就是数组本身；反向（"谁引用了 W"）是
  `WHERE openalex_referenced_work_ids ? 'W…'` —— 这是 OpenAlex 官方 `cites:` filter 的本地等价物，
  永远新鲜，不需要回填，也不需要 dangling-edge 记账。
- **删除存储的 `cited_by_work_ids`**（Q8）：它与上面的反向查询相同，存储它只会引入漂移。

**刷新水位线，而不是逐行记账（Q11）。**

- `openalex_sync_state` —— 一张**单行**表（`id boolean PRIMARY KEY DEFAULT true CHECK (id)`、
  `last_updated_date date`、`last_synced_at timestamptz`、`snapshot_version text`）驱动增量拉取。
  我们**不**向 `openalex_works` 添加逐行 `arxiv_id` / `updated_date` / `publication_year` 回填列。

**受约束取值使用 `text + CHECK`，绝不使用 PG 原生 `enum`（Q10）** —— 增量取值变更是一行
`CHECK` 编辑，而不是 `ALTER TYPE`。

**按成本/历史拆分完整性检查（Q9/Q14/Q15/Q16）。**

- **结构检查是 VIEWS** —— 本地、计算得出、零存储：（1）`paper_assets` md/json 异常；（2）
  `papers.paper_openalex_id` NULL 缺口（应该映射到 corpus work 但没有映射的 paper）。不涉及 HTTP，
  因此 view 可以完整表达这些检查。
- **API 对比审计是一张持久、append-only 的单表 `openalex_audit`**（需要历史 / 趋势 / unresolved-over-time）：
  `id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, run_id bigint, openalex_id text, field text,
  verdict text CHECK (verdict IN ('ok','field_mismatch','cited_by_regressed','stale_drift')),
  local_value jsonb, remote_value jsonb, checked_at timestamptz DEFAULT now()`, indexed on
  `(run_id)` 和 `(openalex_id, field)`。**记录每一行抽样结果，包括 `ok`。**
  `sampled_count` / coverage / "unresolved" 全部**派生**，从不存储或修改 ——
  *unresolved* = 每个 `(openalex_id, field)` 的最新一次 run 中 `verdict ≠ ok` 的记录（Q16）。
- **审计规则（Q9）：** 稳定字段（`referenced_works` / `doi` / `year` / `type`）**精确**比较；
  `cited_by_count` **容忍单调增长**（只有 remote < snapshot，或漂移超过阈值时才报告——被引只会增长）。
  对比需要一个会抓取 OpenAlex API 的**外部脚本**；SQL view 不能发起 HTTP 调用。

## 为什么内联 `jsonb` + GIN 优于边表

- **record 已经是源。** `referenced_works` 存在于每条 OpenAlex record 中；生成列以确定性方式重新派生它——
  它永远不会与 record 漂移，ingest 一个 work 也不需要单独的边抽取过程或完整重跑。
- **一个索引，两个方向。** GIN 倒排索引同时回答正向（读取数组）和反向（`? 'W…'`）查询；边表要做到同样能力，
  需要一个 PK *加上*第二个索引，还要维护 insert/delete 并容忍 dangling-edge。
- **不会过期的被引。** 反向查询在读取时从同一批 records 计算，因此始终与 corpus 当前持有的内容一致——
  存储列表只是一个 cache，每次 ingest 都必须失效。

## 备选方案

- **保留 `work_referenced`（ADR `0006` 发布时的形态）。** 否决（Q8）：GIN 反向查询已经能零维护服务两个方向；
  边表增加写入、第二个索引和 dangling-edge 处理，却没有唯一能支撑的查询。
- **逐行存储 `cited_by_work_ids`。** 否决：这是可派生 cache，每次 ingest 都会漂移。
- **约束列使用 PostgreSQL 原生 `enum`。** 否决（Q10）：`text + CHECK` 只需一行编辑即可演进；
  `ALTER TYPE … ADD VALUE` 对事务不友好，也难以回滚。
- **可变审计摘要行（running counters / "unresolved" 标志）。** 否决（Q16）：
  append-only 抽样行 + 派生聚合提供历史、趋势和可复现的 "unresolved"，且永远不会谎报过去。
- **每个 work 上逐行 `updated_date`。** 否决（Q11）：单行 `openalex_sync_state` 水位线驱动增量刷新，
  无需约 10⁸ 个冗余 timestamp。

## 影响

- **ADR `0006` 机制被细化。** 其中的 `work_referenced` 边表由内联生成列 + GIN 取代；"引用解析始终在本地，
  永不退回 public API"这一保证**不变**（现在由数组 + 反向查询服务）。
- **破坏性迁移（Phase B）。** `internal/openalexcorpus/{schema,ingest,query,record,store}.go`
  和 `cmd/qatlasd/openalex_cmd.go` 删除 `work_referenced`，添加生成列 + GIN + `strip_openalex_prefix()`
  函数、`openalex_sync_state` 和 `openalex_audit`；被引查询变为 `?` 反向查询。测试随之迁移。
- **新增审计脚本 + 视图。** 运维人员运行的采样器抓取 OpenAlex 并写入 `openalex_audit` 行；
  结构性视图随模式发布。只读 SQL 协议（ADR `0007`）从其可读子集中移除 `work_referenced`。
