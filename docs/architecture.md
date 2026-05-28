# Architecture

## 项目分层

QuantumAtlas 的核心设计不是“把所有东西都塞进一个仓库”，而是明确区分不同层级的 source of truth。

```text
QuantumAtlas app repo      QuantumAtlas-Wiki repo      RAW_DIR/{pdf,markdown,json,images}      Neo4j / 任务记录
应用代码与工具        <->   可审阅知识页面        <->    canonical paper assets            <->   派生查询与运行时层
```

建议这样理解：

- 应用仓库负责代码、模板、CLI、API、测试和脚本。
- `WIKI_DIR` 指向可审阅、可追踪的 Markdown 知识库，生产环境推荐单独放在 `QuantumAtlas-Wiki` 这类普通 Git 仓库里。
- `RAW_DIR` 保存 PDF、解析 Markdown、元数据 JSON 和图片等论文资产，是 canonical paper asset store。
- Neo4j、share 记录、ingest 状态、临时任务属于派生或运行时层，不是长期主数据源。

## 为什么要把 Wiki 和论文资产分开

Wiki 负责回答“这是什么”，图数据库负责回答“它和什么有关”。

这带来几个好处：

- Wiki 页面可以像普通文档一样审阅、修改、回滚。
- 大文件资产不会污染应用仓库和知识仓库。
- 应用代码可以按 release tag 固定，Wiki 内容可以独立高频更新。
- Neo4j 只是查询层，不会反向定义知识边界。

## Wiki 结构

Wiki 不必放在应用仓库里。推荐作为独立 Git 仓库维护，并通过 `QATLAS_WIKI_DIR`（旧名 `WIKI_DIR` 仍作 alias）接入 QuantumAtlas。

```env
QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
```

推荐目录结构：

```text
QuantumAtlas-Wiki/
├── index.md
├── concepts/
├── entities/
│   ├── algorithms/
│   ├── primitives/
│   └── people/
├── sources/
│   └── papers/
└── comparisons/
```

页面是带 YAML frontmatter 的 Markdown 文件，例如：

```yaml
---
id: prim-qft
title: Quantum Fourier Transform
type: entity
category: primitive
tags: [transformation, fourier, fundamental]
status: published
related: [paper-arxiv-9508027]
---
```

页面之间通过 `[[page-id]]` 互相引用。内置 linter 会检查 frontmatter、断链、孤立页面和部分知识冲突。

## Primitive 的三层表示

与 primitive 相关的内容实际分成三层：

- `atlas/knowledge_graph/primitives/*.yaml`: 程序侧定义源，供 loader、designer 和初始化脚本使用。
- `$WIKI_DIR/entities/primitives/*.md`: 面向知识协作的 Wiki 页面。
- Neo4j 里的 Primitive 节点: 面向查询和关系遍历的图谱层。

这三层的职责不同：

- YAML 更偏“程序定义”。
- Wiki 更偏“知识页面”。
- 图数据库更偏“关系查询”。

新增或修改 primitive 时，应该判断哪几层需要同步更新，而不是只改其中一层。

## Source 页面与 RAW 资产

`$WIKI_DIR/sources/papers/*.md` 是正式知识内容，不是临时缓存。它们应该保存：

- 论文摘要与来源链接。
- 论文相关补充笔记。
- 被其他页面引用的来源页关系。

而 PDF、解析 Markdown、JSON 和图片等大文件，应放到 `RAW_DIR`，不要直接塞进 Wiki 页面目录。

## Share 机制

QuantumAtlas 对外分享原始资源时统一走 `/api/shares` 和 `/share/{token}`。

这意味着：

- 外部调用方拿到的是 share URL，而不是服务器本地路径。
- 公开访问的是 share token，而不是用户身份。
- share 只负责"哪些资源可访问"，不负责"谁是调用者"。

## 论文元数据索引 (`paperindex` — Parquet + DuckDB Lakehouse 模式)

> 这是 QuantumAtlas 区别于"原始 S3 cache"的一层。理解了它就理解了为什么不需要额外开一个 PostgreSQL / MySQL。

### 问题：对象存储不会回答"集合性"问题

对象存储 (S3 / RustFS) 的原生接口只有"按 key 取一个对象 (GetObject)"、"按 key 列出对象 (ListObjects)"、"按 key 删除"几类。它**没有**：

- 跨对象的 query / filter / sort / count / group by
- 二级索引（按非 key 字段查）
- 全文搜索

对单个论文做"按 id 取 PDF"这种**点查**是天然合适的（就是 GetObject）。但你的真实需求里有这些**集合性问题**：

| 需求 | 对象存储原生能力 | 痛点 |
|---|---|---|
| "PDF / Markdown 总数" | `ListObjects(prefix=pdf/)` 全扫 + 在内存计数 | bucket 大 → LIST 慢 / 出 500（RustFS-beta 实测：10⁵ 量级直接 timeout）|
| "需要 MinerU 的论文（有 PDF 无 MD）" | 双 ListObjects + 内存 diff | 同上 + O(n) 内存 |
| "上周已处理过的论文标题" | ListObjects + 每个 HeadObject 取 LastModified + 每个 GetObject 取 metadata json | N 次 round trip，分钟级 |
| "按 category / author 筛选" | 完全做不到 | 字段不在 key 里 |

### 反方案：再开一个数据库？不

最朴素的修复是"加一个 PostgreSQL / MySQL，每次上传时同时写 DB"。我们**不这样做**，理由：

1. **多一个 stateful 系统**：要 backup、要 HA、要 schema migration、跨 edge 节点要复制；
2. **同步漂移风险真实存在**：DB 和 bucket 在两次写之间的任何 crash 都让两边状态分裂；
3. **多一套凭据**：DB password 要轮换，要发给 client tooling，运维面增大；
4. **跟 active-active 多 edge 不友好**：两台 edge 各自一份 SQLite 就是漂移源，要用 master-replica 就还要选主。

### 实际方案：Lakehouse —— 把"索引"也当成 bucket 里的一个对象

业内成熟模式叫 **lakehouse**（Snowflake / BigQuery / Athena / Trino / DuckDB 同属此谱系）：

```text
┌──────────────────── bucket: qatlas-raw ────────────────────┐
│                                                            │
│  pdf/0704/0704.2988v1.pdf       ← 原始 blob（已存在）       │
│  markdown/0704/0704.2988v1.md                              │
│  json/0704/0704.2988v1.json   ← arxiv 元数据（已存在）      │
│                                                            │
│  index/papers.parquet         ← **新增** 一个对象，就这一个 │
│      └─ 列式表，134k 行 × 数十列                            │
│         (arxiv_id, title, abstract, authors, categories,   │
│          has_pdf, has_md, has_json, md_processed_at,       │
│          pdf_size, ...)                                    │
└────────────────────────────────────────────────────────────┘
                          ▲  ▲
                          │  │  GetObject (~7 MB) + 内存查询
                          │  │
                          │  └─ DuckDB (Go in-process, cgo lib)
                          │     在 qatlas-server 进程里跑
                          │
                          └─ minio-go (Go in-process)
                             同一进程，同一个 svcacct 凭据
```

**关键性质**：

- **"数据库"就是 bucket 里那个 `index/papers.parquet`**。**没有第二个 stateful 系统**。备份 / 迁移 / DR 跟 PDF 们走完全相同的路径。
- **DuckDB 是嵌入式查询库（不是 server）**——通过 cgo binding (`marcboeker/go-duckdb`) 链进 qatlas-server 二进制。**没有第二个进程**、没有 `.db` 文件。你可以把它理解成"会读 parquet 的 `sql.DB`"，跟 `database/sql` 接口完全一致。
- **凭据复用 qatlas 现有 svcacct**：DuckDB 通过 `CREATE SECRET (TYPE S3, ENDPOINT '10.144.18.10:9000', KEY_ID ..., SECRET ...)` 拿 RustFS 凭据，KEY/SECRET 就是 `.env` 里 `QATLAS_S3_*` 那一组，policy `qatlas-raw-rw` 已经把权限钉到这一个 bucket。**不开新 svcacct，不改 policy**。
- **跨 edge 一致性**：两台 edge 看的是同一个 bucket 里的同一个 `index/papers.parquet`，**天然一致**。SQLite-per-edge 那种漂移问题在结构上就不存在。

### 写入路径（保证不漂移）

每次 upload PDF / Markdown / JSON 时，handler 末尾追加 paperindex upsert。**写顺序定死**：

```text
1. PUT s3://qatlas-raw/pdf/<...>     ← 数据先落 bucket
2. paperindex.Store.Upsert(row)      ← qatlas 进程内的 DuckDB 内存表立即更新
3. （5s 后异步）flusher 把内存表 dump 成 parquet 并 If-Match CAS 写回 bucket
```

中间任何一步崩 → bucket 里数据**真实**，parquet 可能滞后但**永不超前**（永远不会说"有"实际没有）。

**跨 edge 并发**：两台 edge 都可能 flush parquet → 用 If-Match etag 做 CAS，冲突时 retry max 5 次。上传频率低（每天几十次），冲突极罕见。

**drift 兜底**：每天凌晨跑一次 `qatlas-server bootstrap-index --reconcile`，全桶 LIST 比对 parquet，修复任何漂移行。即"主索引 + 定期对账"模式（跟 Iceberg / Delta Lake 的设计哲学一致，只是简化版）。

### 查询路径（毫秒级）

qatlas-server 启动时一次性 GET parquet → DuckDB 内存表 (~70 MB 内存)；后续所有查询命中内存，**sub-millisecond**。后台每 60s 检查远端 etag，变了就重 load（其他 edge 改了的话能拿到）。

```text
GET /api/papers/needs-mineru
  → paperindex.Stats()
      → SELECT count(*) FILTER (WHERE has_pdf AND NOT has_md) FROM papers
      → 内存 DuckDB ~1 ms
  → JSON 返回
```

跟之前那版 "ListObjects on prefix=pdf/" 直接 timeout 的实现相比，**这条链路完全不再触发 S3 LIST**。

### 为什么是 Parquet 不是 JSON / SQLite / CSV

| 格式 | 134k 行体积 (压缩) | 列式 pruning | DuckDB 原生支持 | 适合 S3 |
|---|---|---|---|---|
| **Parquet** | ~7 MB (zstd) | ✅ | ✅ | ✅ (range GET) |
| JSON / NDJSON | ~80 MB (gzip) | ❌ 要全读 | 🟡 (慢) | 🟡 |
| CSV | ~30 MB (gzip) | ❌ | ✅ | 🟡 |
| SQLite `.db` | ~30 MB | ❌ (行式) | 🟡 (需 sqlite_scanner 扩展) | ❌ 不能 partial GET |

Parquet 的列式 + row group + bloom filter 跟"列出某字段为 X 的所有行"这种 OLAP 工作负载天然契合，DuckDB 又是为列式 OLAP 设计的——两者配套是 lakehouse 行业标准。

### 跟 Iceberg / Delta Lake 的关系

Iceberg / Delta Lake 是"分布式多写并发 + ACID + 时间穿梭 + schema evolution"的成熟实现，跑在 Spark / Trino / Flink 上，适合 TB-PB 量级 + 多 writer 团队。

QuantumAtlas 现在的 single-parquet + 进程内 DuckDB + CAS 是"lakehouse-lite"——**同一个架构哲学的简化版**。如果未来真的需要多 writer 并发 + 时间穿梭等高级语义，平滑升级路径是：data files（PDF 等）不动，把 `index/papers.parquet` 改成 Iceberg / Delta 的 manifest 结构 + 多 parquet partition file。**当前的所有 data 不需要重写**。

### 不做的事情

- ❌ 不引入 PostgreSQL / MySQL / 外置 SQLite server
- ❌ 不给 DuckDB 单独建桶（同一个 qatlas-raw bucket 一个 prefix `index/` 足够）
- ❌ 不为 DuckDB 单独建 svcacct（policy 已经按 bucket 锁死）
- ❌ 不把 parquet 当成 source of truth（bucket 里的 PDF 本身才是；parquet 是派生 cache + 索引，drift 时以 bucket 实情为准）

## Client / Server 边界

QuantumAtlas 既可以作为服务端运行，也可以作为远程客户端使用。

- server 模式负责读取本机 `WIKI_DIR`，读写 `RAW_DIR` / `DATA_DIR`，并提供 Wiki 浏览、share、图谱和摄入能力。服务端不会生成或修改 Wiki 页面；如果启用 Wiki 同步接口，它只对 clean checkout 执行 fast-forward 更新。
- client 模式通过 HTTP API 使用这些能力，不要求拿到服务器文件系统权限。

协作时的推荐主边界不是服务器 shell，而是 `QuantumAtlas-Wiki` 仓库本身：

- LLM、脚本、人工编辑都围绕同一个 Wiki Git 仓库工作。
- server 侧的 Wiki checkout 应保持干净，不提供 push API，也不通过 Web UI 直接创建或编辑页面。
- 只有在需要服务器上的搜索结果、页面展示或 Neo4j 同步时，才让 server 去快进自己的 Wiki checkout。
- server 的 Wiki 同步只执行 `git fetch --prune` 和 `git pull --ff-only`；如果本地 checkout 有修改、不是 Git 仓库、不能 fast-forward 或远端不可达，API 会失败并返回对应错误码。
- 如果 server 的 Wiki checkout 不在 `main` 或 `master`，同步状态响应会带 warning，提醒维护者检查部署分支。

应用仓库内不再保留任何 `wiki/`、`raw/`、`data/`、`pb_data/` 目录——所有
状态目录都有内置默认值，落到 git checkout 之外：

```env
# 所有这些都有内置默认；不写就走默认（无需在 .env 里出现）：
# QATLAS_WIKI_DIR    -> <.env 目录>/../QuantumAtlas-Wiki  （兄弟 checkout）
# QATLAS_RAW_DIR     -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw
# QATLAS_DATA_DIR    -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data
# QATLAS_PB_DATA_DIR -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data
#
# 想搬到挂载盘 / FHS 路径时再显式覆盖：
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名 `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 仍作 alias
> 兼容，新写法推荐 `QATLAS_*` 前缀。从仓库内布局迁移到 XDG 默认的
> 步骤见 [migration-storage-layout.md](migration-storage-layout.md)。

把状态目录放在 git checkout 外的好处：fresh clone 永远干净（不需要维护
长串 `.gitignore` 规则），`go ./...` 不会撞到 FUSE 挂载，并且符合 XDG /
FHS / 12-factor 的常规约定。

## 设计上的取舍

- QuantumAtlas 不把浏览器 OAuth 登录流程内置进应用本体。
- QuantumAtlas 不绑定特定反向代理、SSO 或存储产品。
- `RAW_DIR`、`WIKI_DIR`、`DATA_DIR`、`PB_DATA_DIR` 是显式边界，而不是隐含在仓库结构里的假设。
- 应用代码版本和 Wiki 内容版本可以分离演进。

## 延伸阅读

- [storage-design.md](storage-design.md) — 当 RAW 资产体量上 TB / 引入对象存储（RustFS @ `raw.quantum-atlas.ai`）/ 用 Neo4j 装 paper 引用图时，三层（raw / metadata / graph）怎么分工、怎么对账、怎么重建。
- [migration-storage-layout.md](migration-storage-layout.md) — 把 wiki / raw / data / pb_data 从仓库内搬到 XDG / 挂载点的实操步骤。
- [graph-visualization-research.md](graph-visualization-research.md) — 前端图谱库选型调研（Cytoscape.js / Sigma.js / ...）。
