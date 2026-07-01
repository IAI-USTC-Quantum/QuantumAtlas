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
- Neo4j、ingest 状态、临时任务属于派生或运行时层，不是长期主数据源。

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
type: concept
category: primitive
tags: [transformation, fourier, fundamental]
status: published
related: [paper-arxiv-9508027]
---
```

页面之间通过 `[[page-id]]` 互相引用。内置 linter 会检查 frontmatter、断链、孤立页面和部分知识冲突。

!!! note "统一 concept 模型（2026-05）"
    页面类型已统一为 **`concept`**——wiki 以「concept 词条」为唯一可浏览单位（Wikipedia
    风格），子类靠 `category` 区分。`entities/` / `comparisons/` 目录与 `type: entity` /
    `comparison` 为历史组织方式，Go 侧仍可解析以兼容未迁移数据，但统计与 UI 一律按 concept
    处理。`source`（论文）只作为词条「参考文献」里的引用，不作为可浏览条目。详见
    [wiki-schema.md](../reference/wiki-schema.md) 的「统一 concept 模型」段。

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

## Paper 资产

`qatlasd` 服务端**不通过 HTTP API 对外分发** PDF / Markdown 字节。资产
桶里的对象只为内部处理（如解析、索引）服务，用户/客户端只能查询元数据
（OpenAlex 同步进来的 title / authors / 引用关系等）与论文是否已经具备
某项资产（`papers/stats`、`papers/needs-mineru`）。

如果需要原始 PDF，请直接到 arXiv 等上游获取（`papers.ArxivAbsURL` 数据
字段可以拼出 canonical arxiv URL）。


## 论文元数据索引（PostgreSQL catalog） { #paperindex }

> `paperindex` 是历史锚点名：旧版曾把索引放成 RustFS 上的 Parquet。
> 现在的决策是 **RustFS 只做纯 S3 后端**；集合查询、DOI 状态和 MinerU claim
> 租约进入 PostgreSQL，登录态仍由 PocketBase 单独管理。

### 问题：对象存储不会回答"集合性"问题

对象存储 (S3 / RustFS) 的原生接口只有"按 key 取一个对象 (GetObject)"、
"按 key 列出对象 (ListObjects)"、"按 key 删除"几类。它**没有**：

- 跨对象的 query / filter / sort / count / group by
- 二级索引（按非 key 字段查）
- 原子租约（例如"抢一篇还没被 MinerU 处理的论文"）

对单个论文做"按 id 取 PDF"这种**点查**是天然合适的（就是 GetObject）。
但真实业务需要集合查询：

| 需求 | 对象存储原生能力 | 当前实现 |
|---|---|---|
| "PDF / Markdown 总数" | `ListObjects(prefix=pdf/)` 全扫 + 计数 | PostgreSQL `count(*) FILTER (...)` |
| "需要 MinerU 的论文（有 PDF 无 MD）" | 双 ListObjects + 内存 diff | PostgreSQL partial index (`paper_assets ... WHERE mineru_md_path IS NULL`) |
| "DOI 是否已有本地贡献" | 无二级索引 | PostgreSQL UNIQUE `papers.paper_doi` |
| "抢一个 MinerU lease" | 无事务锁 | PostgreSQL row lock + `paper_assets.lease_expires_at` |

### 实际方案：PostgreSQL 是非登录态 catalog

`papers` + `paper_assets` 两张表是 paper catalog 的派生索引（ADR 0009）：

- **`papers`**（一 work 一行）：代理主键 `paper_id`；三个外部 id 列各 UNIQUE
  `paper_arxiv_id` / `paper_doi` / `paper_openalex_id`（`paper_openalex_id` 带可空 FK →
  `openalex_works`）；`paper_title` / `paper_publication_date`；生成式 `paper_ref`
  （openalex>arxiv>doi）；触发器维护的 `paper_default_asset_id`；精简
  `paper_verification_status`
- **`paper_assets`**（一份 PDF 一行：arxiv v1/v2/… + 正式版）：`asset_id`、
  `paper_id`(FK CASCADE)、`source`(arxiv|published)、`arxiv_version`、对象 key
  `pdf_path` / `mineru_md_path` / `mineru_json_path`、`pdf_sha256` / `pdf_size` /
  `image_count` / `fetched_at`；MinerU 租约 `lease_id` / `lease_holder` /
  `lease_expires_at`

使用 PostgreSQL 特性：

- `INSERT ... ON CONFLICT` 做 upload write-through 和 `papers sync` 重建；
- partial unique index：`paper_assets (paper_id) WHERE source='published'`（正式版每篇一份）；
- partial queue index：只索引 `paper_assets ... WHERE mineru_md_path IS NULL`
  的 MinerU 队列候选；
- row-level lock (`SELECT ... FOR UPDATE`) 保证同一资产的 lease 只有一个赢家；
- 触发器在 `paper_assets` 增删时重算 `papers.paper_default_asset_id`（正式版优先，否则最新 arxiv）。

### 写入路径

```text
1. PUT s3://qatlas-pdf/<yymm>/<stem>.pdf        ← 字节先落 RustFS
2. INSERT ... ON CONFLICT papers + paper_assets ← PostgreSQL 派生索引同步
3. PostgreSQL 不可用时：HTTP 仍成功 + X-Catalog-Sync: deferred
4. 之后跑 qatlasd papers sync --full --from-rustfs 从 S3 重建缺失状态
```

RustFS 仍是资产字节的 source of truth；PostgreSQL 是可重建的集合索引与租约层。
这避免了把索引文件、Parquet 或应用级 metadata 放进 RustFS，同时也避免把登录态和
论文业务状态混进 PocketBase。

### 查询路径

```text
GET /api/papers/needs-mineru
  → SELECT ... FROM paper_assets a JOIN papers p ON p.paper_id = a.paper_id
    WHERE a.source = 'arxiv'
      AND a.pdf_path IS NOT NULL AND a.mineru_md_path IS NULL
      AND (a.lease_expires_at IS NULL OR a.lease_expires_at < now())
    ORDER BY a.fetched_at DESC
  → JSON 返回
```

跟直接 `ListObjects` 相比，这条链路不会触发全桶 LIST；跟把 catalog 放进 Neo4j 相比，
它用的是关系型数据库擅长的约束、索引和事务，而 Neo4j 继续保留给真正的图查询。

## Client / Server 边界

QuantumAtlas 既可以作为服务端运行，也可以作为远程客户端使用。

- server 模式负责读取本机 `WIKI_DIR`，读写 `RAW_DIR` / `DATA_DIR`，并提供 Wiki 浏览、图谱和摄入能力。服务端不会生成或修改 Wiki 页面；如果启用 Wiki 同步接口，它只对 clean checkout 执行 fast-forward 更新。
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
# QATLAS_RAW_DIR     -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw
# QATLAS_DATA_DIR    -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data
# QATLAS_PB_DATA_DIR -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data
#
# 想搬到挂载盘 / FHS 路径时再显式覆盖：
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名 `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 仍作 alias
> 兼容，新写法推荐 `QATLAS_*` 前缀。从仓库内布局迁移到 XDG 默认的
> 步骤见 [migration-storage-layout.md](../server/migration-storage-layout.md)。

把状态目录放在 git checkout 外的好处：fresh clone 永远干净（不需要维护
长串 `.gitignore` 规则），`go ./...` 不会撞到 FUSE 挂载，并且符合 XDG /
FHS / 12-factor 的常规约定。

## 设计上的取舍

- QuantumAtlas 不把浏览器 OAuth 登录流程内置进应用本体。
- QuantumAtlas 不绑定特定反向代理、SSO 或存储产品。
- `RAW_DIR`、`WIKI_DIR`、`DATA_DIR`、`PB_DATA_DIR` 是显式边界，而不是隐含在仓库结构里的假设。
- 应用代码版本和 Wiki 内容版本可以分离演进。

## 延伸阅读

- [storage-architecture.md](storage-architecture.md) — 当 RAW 资产体量上 TB / 引入对象存储（RustFS / S3 兼容）/ 用 Neo4j 装 paper 引用图时，三层（raw / metadata / graph）怎么分工、怎么对账、怎么重建。
- [migration-storage-layout.md](../server/migration-storage-layout.md) — 把 wiki / raw / data / pb_data 从仓库内搬到 XDG / 挂载点的实操步骤。
- [graph-visualization-research.md](../about/graph-visualization-research.md) — 前端图谱库选型调研（Cytoscape.js / Sigma.js / ...）。
