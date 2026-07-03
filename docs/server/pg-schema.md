# PostgreSQL 库设计（catalog + OpenAlex 语料）

> **手工维护文档**。事实源是代码里的两个 `schema.go`
> （[`internal/papers/schema.go`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/internal/papers/schema.go)、
> [`internal/openalexcorpus/schema.go`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/internal/openalexcorpus/schema.go)）
> 里的 DDL——`internal/papers` 的 `schemaStatements`，以及 `internal/openalexcorpus` 拆成的
> `baseSchemaStatements`（`EnsureSchema` 在启动逐条 `Exec` 的幂等 base DDL）+ `corpusIndexes`
> （`EnsureIndexes` 用 `CREATE INDEX CONCURRENTLY` 建的重索引，ADR 0013）。**改了 schema.go 就要
> 回来同步这份文档。** 设计理由见
> [ADR 0006](../adr/0006-postgres-central-store-not-mysql.md) /
> [0009](../adr/0009-papers-paper-assets-catalog-redesign.md) /
> [0010](../adr/0010-openalex-works-inline-citations-and-audit.md) /
> [0011](../adr/0011-by-id-asset-reads.md) /
> [0013](../adr/0013-corpus-schema-base-index-split.md)。

一套**中心化 PostgreSQL**（挂在 mesh 上，跟 RustFS / Neo4j 同级），承载两块语义上独立、但同库
共存（一个 `pgxpool` / 一个 `QATLAS_POSTGRES_DSN`）的数据，好让「catalog × 语料 × 向量」的深
join 留在 SQL 里：

| 块 | 表 | 规模 | 由谁建 | 可重建来源 |
|---|---|---|---|---|
| **Paper catalog** | `papers` + `paper_assets` | ~10⁵ 行 | qatlasd 启动时（`papers.EnsureSchema`，后台 goroutine） | 从资产桶 LIST 重建 |
| **OpenAlex 语料** | `openalex_works` (+ `openalex_sync_state` / `openalex_audit` / `work_embeddings`) | ~2.87×10⁸ 行 / 1–2 TB | **基础 schema 启动时建**（`corpus.EnsureSchema`，先于 catalog）；行**懒加载 fetch-on-miss 写穿**填充，批量 `bootstrap-pg` 为可选预热 | 从 OpenAlex snapshot 重灌（预热）/ 按需重取 |

两块的**生命周期不同**——catalog 每次启动 idempotent 重建；语料的**基础 schema 也每次启动重建**
（先于 catalog），但其 ~10⁸ 行是**懒加载 fetch-on-miss 写穿**填充，批量 snapshot 灌库只是可选预热。
PG 不可达时所有方法优雅降级（写返回 `ErrCatalogUnavailable` + `X-Catalog-Sync: deferred`，读报
`available=false`）。

---

## 1. Paper catalog

### 1.1 `papers`（一 work 一行）

论文身份与任何单个外部 id 解耦：代理主键 `paper_id`，三个外部 id 各自 UNIQUE（各可空，PG 默认
UNIQUE 允许多 NULL）。

| 列 | 类型 | 说明 |
|---|---|---|
| `paper_id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | 代理主键；跨表 / 跨系统（含 lean）用它引用 |
| `paper_arxiv_id` | `text UNIQUE` | arXiv id（裸、无版本，如 `2208.06941`）；版本落在 `paper_assets.arxiv_version` |
| `paper_doi` | `text UNIQUE` | DOI（裸、小写） |
| `paper_openalex_id` | `text UNIQUE` | OpenAlex works id（裸 `W…`）；可空 FK → `openalex_works`（见 [1.4](#14-外键与启动顺序)） |
| `paper_title` | `text` | 标题（DOI 上传时来自 OpenAlex 核验） |
| `paper_publication_date` | `date` | 发表日期 |
| `paper_default_asset_id` | `bigint`（FK → `paper_assets.asset_id`） | 默认资产指针；触发器维护（见 [1.3](#13-默认资产触发器)） |
| `paper_verification_status` | `text CHECK IN ('verified','doi-not-found','metadata-unavailable','unconfigured')` 或 NULL | DOI 上传核验结果（精简审计位） |
| `paper_ref` | `text GENERATED ALWAYS AS (CASE …) STORED` | 规范 `kind:id`，优先级 **openalex > arxiv > doi** |

约束：`CHECK (paper_arxiv_id IS NOT NULL OR paper_doi IS NOT NULL OR paper_openalex_id IS NOT NULL)`
（至少一个外部 id）。

### 1.2 `paper_assets`（一份 PDF 一行）

一个 work 可有多份 PDF（arXiv v1/v2/… + 正式版），故拆子表。资产有无由 `*_path IS NOT NULL`
派生，**无 `has_*` 布尔**。

| 列 | 类型 | 说明 |
|---|---|---|
| `asset_id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | 资产主键 |
| `paper_id` | `bigint NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE` | 所属论文 |
| `source` | `text NOT NULL CHECK (source IN ('arxiv','published'))` | 来源 |
| `arxiv_version` | `int` | arxiv 非空（如 `2`）/ published 为 NULL |
| `pdf_path` | `text NOT NULL` | PDF 对象 key（不是 URL） |
| `pdf_size` | `bigint` | PDF 字节数 |
| `pdf_sha256` | `char(64)` | PDF sha256（MinerU 校验契约用） |
| `mineru_md_path` | `text` | MinerU markdown 对象 key |
| `mineru_json_path` | `text` | MinerU JSON 对象 key（见 [3. 尚未落代码](#3-已设计尚未落代码)） |
| `image_count` | `int` | 图片数 |
| `fetched_at` | `timestamptz` | 抓取 / 转换时间 |
| `lease_id` / `lease_holder` / `lease_expires_at` | `text` / `text` / `timestamptz` | MinerU 处理**租约**（原 claim；ADR 0007/0008 后 "claim" 归 lean，改叫 lease），逐资产授予 |

约束与索引：

- `CHECK ((mineru_md_path IS NULL) = (mineru_json_path IS NULL))` —— md/json 同生同灭（MinerU 一次产两者）。
- `CHECK ((source='arxiv' AND arxiv_version IS NOT NULL) OR (source='published' AND arxiv_version IS NULL))`。
- `UNIQUE (paper_id, source, arxiv_version)` —— arxiv 版本唯一。
- `CREATE UNIQUE INDEX … (paper_id) WHERE source='published'` —— 正式版每篇一份（partial index 规避 published 的 NULL 版本唯一失效）。
- `CREATE INDEX … (paper_id)` —— **PG 不自动给 FK 列建索引**，缺它「取某篇所有资产」全表扫。
- `CREATE INDEX … (fetched_at DESC, asset_id) WHERE mineru_md_path IS NULL` —— MinerU 队列（有 PDF 无 md）。
- `CREATE INDEX … (lease_expires_at) WHERE lease_expires_at IS NOT NULL` —— 过期租约清扫。

### 1.3 默认资产触发器

`papers.paper_default_asset_id` 指向「**正式版优先，否则最新 arxiv 版本**」那一份，由触发器
`papers_refresh_default_asset()`（`plpgsql`）在 `paper_assets` 增 / 删 / 改（source / arxiv_version）
时按 `ORDER BY (source='published') DESC, arxiv_version DESC NULLS LAST, asset_id DESC LIMIT 1`
重算。取默认 md 是「`papers` → `default_asset_id` → `paper_assets`」两次主键探针，不扫表。

### 1.4 外键与启动顺序

两条外键都用**幂等 `DO` 块**加，不写在 `CREATE TABLE` 里（PG 没有 `ADD CONSTRAINT IF NOT EXISTS`）：

- **`paper_default_asset_id` → `paper_assets(asset_id)` `ON DELETE SET NULL`** —— `papers` 先建、
  `paper_assets` 后建，所以这条 FK 在 `paper_assets` 建完后由 DO 块补上。删默认资产时指针置 NULL、
  触发器再重算。
- **`paper_openalex_id` → `openalex_works(openalex_id)` `ON DELETE SET NULL`** —— 见下面专门一节。

#### 为什么 openalex FK 仍用「幂等 DO 块」加

`openalex_works` 现在是**启动时创建、按需懒加载填充的写穿缓存**（write-through cache，见 ADR 0006）：
`ensureCatalogSchema` 在建 `papers` catalog **之前**先跑 `corpus.EnsureSchema`（建 `openalex_works` +
sync-state + audit；`work_embeddings` 因需要 pgvector 扩展，用 `DO` 块条件建、无扩展时 no-op），
所以 `papers` 的这条 FK 在 DO 块跑到时 `openalex_works` **已经在了**。DO 块在这里的作用纯粹是
**幂等**（PG 没有 `ADD CONSTRAINT IF NOT EXISTS`）+ 防御性兜底（万一 corpus 表还没建就跳过、下次启动补）：

```sql
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'openalex_works')
     AND NOT EXISTS (SELECT 1 FROM information_schema.table_constraints
                     WHERE constraint_name = 'papers_openalex_fk')
  THEN
    ALTER TABLE papers ADD CONSTRAINT papers_openalex_fk
      FOREIGN KEY (paper_openalex_id) REFERENCES openalex_works(openalex_id) ON DELETE SET NULL;
  END IF;
END $$;
```

**这跟「迁不迁生产数据表」无关**——纯粹是同一次 `EnsureSchema` 里两张表的**建表先后**问题，不涉及
任何数据搬运。因为 corpus 先于 catalog 建，**已不存在启动顺序依赖**；DO 块两种情况都安全：

- 现网（RackNerd / Alibaba）已有 `openalex_works` → 部署新 schema 时 DO 块**立即**补上 FK。
- 全新库 → 同一次启动里 corpus 先建好，catalog 的 DO 块随后立即补上 FK。

> **不变式如何维持**：这条 FK 的隐含前提是「任何被写进 `paper_openalex_id` 的 openalex id 都在
> `openalex_works` 里」。**懒加载写穿**恰好维持它——by-id lookup 一旦要给某篇填 `paper_openalex_id`，
> 就已经先 fetch-on-miss 把那条 work 写进了 `openalex_works`（`Resolver.FetchWorkRecord` +
> `corpus.UpsertFetchedWork`）。所以不再依赖「先全量 bootstrap」：语料**子集**下 miss 会自愈，
> catalog 写入也不耦合语料完整性。批量 `openalex bootstrap-pg` 退化为**可选的预热**（补全 citation
> 遍历 + 向量覆盖），不再是前置条件。

---

## 2. OpenAlex 语料

原样留存 + 派生热列的混合模式：每条 work 的原始 JSON 进 `record jsonb`（**只筛选、不修改**），热
字段用 `GENERATED … STORED` 派生列 + 索引暴露，不重写 record。

### 2.1 `openalex_works`

| 列 | 类型 | 说明 |
|---|---|---|
| `openalex_id` | `text PRIMARY KEY` | 裸 `W…` |
| `record` | `jsonb NOT NULL` | 原始 JSON（唯一事实源） |
| `arxiv_id` | `text` | 从 locations 抠出的 arxiv id（join key，可空） |
| `updated_date` | `date` | 该 part 的 snapshot 分区日期 |
| `ingested_at` | `timestamptz NOT NULL DEFAULT now()` | 入库时间 |
| `publication_year` / `work_type` / `display_name` / `language` / `primary_topic_id` / `cited_by_count` / `doi` / `is_retracted` / `referenced_works_count` | `GENERATED … STORED` | 从 `record` 派生的热字段 |
| `openalex_referenced_work_ids` | `jsonb GENERATED ALWAYS AS (strip_openalex_prefix(record->'referenced_works')) STORED` | **内联引用出边**：裸 `W…` 数组 |
| `search_text` | `tsvector GENERATED … STORED` | 标题全文检索 |

**行怎么进来**：批量 `openalex bootstrap-pg`（snapshot 预热）与 by-id lookup 的**懒加载 fetch-on-miss
写穿**（`corpus.UpsertFetchedWork`）**共用同一条 `UpsertWorks` 写入路径**，`ON CONFLICT` 幂等更新
`record` + 派生列。所以预热与懒加载填充互不冲突，缺的按需自愈（见 [1.4](#14-外键与启动顺序)）。

**引用不再用边表**（ADR 0010）：

- 出边（W 引用了谁）= `openalex_referenced_work_ids` 数组本身。
- 入边（谁引用了 W）= 反查 `WHERE openalex_referenced_work_ids ? 'W…'`（GIN 命中），等价 OpenAlex 官方 `cites:`，永远新鲜、零维护，无第三张表。
- 剥 `https://openalex.org/` 前缀靠 `IMMUTABLE` 函数 `strip_openalex_prefix(jsonb)`（generated 列不能子查询/聚合，故封进函数）。

索引：`record` 上 `gin (record jsonb_path_ops)`（`@>` 容器查询）；`openalex_referenced_work_ids` 上
`gin (…)`（默认 ops，支持 `?` 反查——`jsonb_path_ops` 不支持 `?`）；`search_text` 上 `gin`；外加
`publication_year` / `work_type` / `language`(partial) / `primary_topic_id`(partial) /
`cited_by_count` / `doi`(partial) / `arxiv_id`(partial) / `updated_date` 的 btree。

这些 `openalex_works` 重索引**不在 boot 关键路径上建**（ADR 0013）：`EnsureSchema` 只建 base 表
（对已存在的大表 `CREATE TABLE IF NOT EXISTS` 是 no-op），重索引由 `EnsureIndexes` 用
`CREATE INDEX CONCURRENTLY IF NOT EXISTS` 单列一个后台阶段建——只拿 `ShareUpdateExclusive` 锁、
不挡懒加载写回 / bootstrap，并处理中断留下的 INVALID 残留。由 `QATLAS_CORPUS_ENSURE_INDEXES`
（默认 `true`）门控：一台 edge 指向**已预置好索引**的大型共享 corpus 时设 `false` 跳过。

### 2.2 `openalex_sync_state`（增量刷新水位，单行）

```sql
CREATE TABLE openalex_sync_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),   -- 单行守卫
  last_updated_date date,
  last_synced_at timestamptz,
  snapshot_version text
);
```

增量刷新只拉「> `last_updated_date`」的新分区，不给 10⁸ 行各挂 updated_date。

### 2.3 `openalex_audit`（API 比对，append-only）

抽样比对官方 OpenAlex API 的结果，**单表 append-only，每抽样一行都落一条（含 `ok`）**，支撑
历史 / 趋势 / 未解决追踪。

| 列 | 类型 |
|---|---|
| `id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` |
| `run_id` | `bigint` |
| `openalex_id` | `text` |
| `field` | `text` |
| `verdict` | `text CHECK (verdict IN ('ok','field_mismatch','cited_by_regressed','stale_drift'))` |
| `local_value` / `remote_value` | `jsonb` |
| `checked_at` | `timestamptz NOT NULL DEFAULT now()` |

索引 `(run_id)`、`(openalex_id, field)`。`sampled_count` / 覆盖率 / **未解决**（每
`(openalex_id,field)` 最新 run 的 `verdict≠ok`）全部**派生**，不另存不 mutate。比对逻辑（ADR 0010
Q9）：稳定字段精确相等；`cited_by_count` 单调容忍（官方 ≥ 快照为正常）。**比对需外部脚本 fetch
OpenAlex API**（视图 / 生成列调不了外部 HTTP）。

### 2.4 `work_embeddings`（域子集向量）

```sql
CREATE TABLE work_embeddings (
  openalex_id text PRIMARY KEY REFERENCES openalex_works(openalex_id) ON DELETE CASCADE,
  embedding vector(1024) NOT NULL,   -- BGE-M3 dense dim
  model text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
```

只对域子集建；HNSW 索引 + 灌入属于后续 embedding 阶段，需 `vector` 扩展（pgvector）。

---

## 3. 已设计、尚未落代码

这些在 ADR 里定了、但当前 `schema.go` / 管线**还没实现**，文档如实标注避免 doc-code 漂移：

- **结构检查视图**（ADR 0010）：① `paper_assets` md/json 异常；② `papers.paper_openalex_id` 的
  NULL 缺口。设计上是纯本地视图（现算、零存储），**尚未写进 schema.go**。
- **`mineru_json_path` 的实际写入**：contrib 上传的 MinerU zip **含 `full.md` + JSON + images/**，
  但 v0.7.0 砍了 json sidecar 桶，`upload-mineru` 当前只抽 md + images、**丢弃 json**。所以
  `mineru_json_path` 列已就位但暂无内容；要让 `GET …/json` 生效，需在 `upload-mineru` 里从 zip
  抽 json 存进对象存储。
- **访问开关 OFF 态给 arxiv 直链**（ADR 0011）：目前开关 OFF 时资产端点整体不注册（404）；「OFF →
  arxiv 论文给 arxiv.org 直链」是既定方向，跟踪
  [#8](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/8)。

---

## 4. lean 侧 MySQL（不在本库）

qatlas-lean 自己维护一套 MySQL（`claims` / `claim_proof` / `lean_theorems`），记录 claim / proof /
Lean theorem，用 `papers.paper_id` **只读**引用本库。那套表不由 QuantumAtlas 维护，也不在这份文档
范围内（边界见 ADR [0007](../adr/0007-claim-references-and-lookup-boundary.md) /
[0008](../adr/0008-claim-authoring-moves-to-the-lean-repo.md)）。
