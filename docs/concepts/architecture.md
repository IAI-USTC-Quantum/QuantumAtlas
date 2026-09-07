# Architecture

## 项目分层

QuantumAtlas 的核心设计不是“把所有东西都塞进一个存储”，而是明确区分不同层级的 source of truth。

```text
QuantumAtlas app repo      对象存储 (RustFS / S3)            PostgreSQL                  搜索引擎
应用代码与工具        <->    canonical paper assets      <->   registry + OpenAlex corpus  <->  派生查询层
```

建议这样理解：

- 应用仓库负责代码、CLI、API、测试和脚本。
- 对象存储保存 PDF、解析 Markdown、元数据 JSON 和图片等论文资产，是 canonical paper asset store（本地 `RAW_DIR` 仅作 dev fallback）。
- PostgreSQL 是中心化关系库：paper registry（papers / paper_assets / paper_identities，goose 管理）+ OpenAlex works 语料，见 ADR 0006。
- 搜索引擎（`POST /api/search` 的多 provider fan-out）与 ingest 任务状态属于派生或运行时层，不是长期主数据源。

## 为什么要把 registry 和论文资产分开

对象存储负责回答“字节在哪”，PostgreSQL 负责回答“有哪些论文、各自处于什么状态、彼此什么关系”。

这带来几个好处：

- 大文件资产不会污染关系库；registry 行可以独立重建。
- 集合查询（计数、过滤、租约）走关系型数据库擅长的约束、索引和事务。
- 应用代码可以按 release tag 固定，语料与资产可以独立高频更新。
- 搜索只是查询层，不会反向定义数据边界。

## Paper 资产

`qatlasd` 服务端**默认不通过 HTTP API 对外分发** PDF / Markdown 字节。资产
桶里的对象只为内部处理（如解析、索引）服务，用户/客户端只能查询元数据
（OpenAlex 同步进来的 title / authors / 引用关系等）与论文是否已经具备
某项资产（`papers/stats`、`papers/needs-mineru`）。Self-hosted 部署可用
`QATLAS_PAPER_ACCESS_ENABLED=true` 打开字节分发，合规义务随之转给部署方。

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

跟直接 `ListObjects` 相比，这条链路不会触发全桶 LIST；registry 用的是
关系型数据库擅长的约束、索引和事务。

## Client / Server 边界

QuantumAtlas 既可以作为服务端运行，也可以作为远程客户端使用。

- server 模式负责读写对象存储 / PostgreSQL，并提供搜索、论文资产和摄入能力。
- client 模式通过 HTTP API 使用这些能力，不要求拿到服务器文件系统权限。

应用仓库内不再保留任何 `raw/`、`data/`、`pb_data/` 目录——所有
状态目录都有内置默认值，落到 git checkout 之外：

```env
# 所有这些都有内置默认；不写就走默认（无需在 .env 里出现）：
# QATLAS_RAW_DIR     -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw
# QATLAS_DATA_DIR    -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data
# QATLAS_PB_DATA_DIR -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data
#
# 想搬到挂载盘 / FHS 路径时再显式覆盖：
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名 `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 的 alias 已在 v0.17.0 移除，
> 新写法统一 `QATLAS_*` 前缀。从仓库内布局迁移到 XDG 默认的
> 步骤见 [migration-storage-layout.md](../server/migration-storage-layout.md)。

把状态目录放在 git checkout 外的好处：fresh clone 永远干净（不需要维护
长串 `.gitignore` 规则），`go ./...` 不会撞到 FUSE 挂载，并且符合 XDG /
FHS / 12-factor 的常规约定。

## 服务端组件清单（`internal/` / `cmd/`）

按上面分层对应的 qatlasd 主要 Go 包；都是主仓内的代码，不是独立进程：

| 组件 | 职责 |
|---|---|
| `internal/registry` | PostgreSQL paper registry（`papers` / `paper_assets` / `paper_identities`），去重唯一入口 `ResolveOrMint` |
| `internal/downloader` | Robust Downloader：多范式 PDF 抓取策略阶梯 + 统一验证管线（v0.26.0；builtin `downloader` 插件）|
| `cmd/downloaderproxy` | 独立单容器部署的同款梯子服务——放在有出版社 entitlement 的机器（校园出口），qatlasd 经 `downloader.proxy` 委托；自带 Chrome-for-Testing 浏览器 lane（v0.27.0）|
| `internal/search` | 搜索 Provider 抽象 + catalog/arxiv/openalex 内置 + remote 微服务 client + backend 静态目录（`backendmeta.go`）|
| `internal/userkeys` | 每用户第三方搜索 key 的 AES-256-GCM 加密存储（`search_api_keys` collection；密钥派生自 system PAT）|
| `internal/routes` | HTTP 路由层（papers / search（含 multi + backends）/ downloader / admin assets / me / auth …）|
| `internal/ingest` · `internal/lazyload` · `internal/mineru` | 惰性摄入管线与 MinerU 转换调度 |

## 设计上的取舍

- QuantumAtlas 不把浏览器 OAuth 登录流程内置进应用本体。
- QuantumAtlas 不绑定特定反向代理、SSO 或存储产品。
- `RAW_DIR`、`DATA_DIR`、`PB_DATA_DIR`、`QATLAS_POSTGRES_DSN` 是显式边界，而不是隐含在仓库结构里的假设。

## 延伸阅读

- [storage-architecture.md](storage-architecture.md) — 当 RAW 资产体量上 TB / 引入对象存储（RustFS / S3 兼容）时，raw / metadata 各层怎么分工、怎么对账、怎么重建。
- [migration-storage-layout.md](../server/migration-storage-layout.md) — 把 raw / data / pb_data 从仓库内搬到 XDG / 挂载点的实操步骤。
- [data-flow.md](data-flow.md) — 一篇论文从 arXiv 到可检索资产的端到端链路。
