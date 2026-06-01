# 存储分层设计：对象存储 + Metadata DB + Neo4j

> **状态**：设计文档（已大量落地）。本文写的是**为什么分层 + 三种引擎怎么配合**
> 这类不变的设计原则；落地细节（具体桶名、IAM、ingest handler 走哪条路径）
> 经历过几轮迭代，凡是跟下方"实施现状"对不上的段落已就地标注，**以
> [`docs/deployment/rustfs.md`](../deployment/rustfs.md) 为 canonical 现代答案**。
>
> **范围**：定义 QuantumAtlas 处理大体量文献时三类存储引擎的职责边界、
> 数据流向和演进路线。给"为什么不能把 PDF 塞进 Neo4j 节点属性"、
> "Neo4j 数据脏了怎么重建"、"raw 跟 metadata 跟 graph 怎么对账"这类
> 反复出现的设计问题一个 canonical 回答。
>
> **实施现状（v0.7.0+，2026-05）**：
>
> - **三层引擎都已就绪**：S3 后端（`internal/objstore.S3Store`）+ PocketBase
>   v0.38（嵌入 server，SQLite collections + GitHub OAuth）+ Neo4j 5.26 LTS
>   （apt 装在 1810 WSL2，Bolt 通过 Windows portproxy + EasyTier mesh 暴露给
>   两台 edge）。
> - **RustFS 对象存储**跑在 **NAS Synology Docker**（不在 edge 上、不在 1810 上），
>   通过 1810 mesh IP `10.144.18.10:9000` 暴露给 edge。bucket 已从早期单桶
>   `qatlas-raw` 拆为 **5 个 + 1 个事件桶**：`qatlas-{raw,pdf,md,images,openalex}`
>   + `qatlas-s3-events`（详见 [`rustfs.md`](../deployment/rustfs.md) 与
>   [`storage-design.md` § 桶布局](../concepts/architecture.md)）。
> - **upload 路径同步写**：v0.7.0 起 upload handler **同请求里**完成 S3 + Neo4j
>   MERGE（不再 worker queue / DLQ 链）。Neo4j 挂时 fail-open on write：S3 仍写
>   成功，回 `201 + X-Catalog-Sync: deferred`，下次 `qatlasd papers sync`
>   兜底补齐。详见 [`auth-model.md`](auth-model.md) 与
>   [`upload-api.md`](../reference/upload-api.md)。
> - **OpenAlex bootstrap CLI**（`cmd/qatlasd/openalex_cmd.go`）+ **papers
>   sync CLI**（`cmd/qatlasd/papers_cmd.go`）都已就绪——取代了早期设想的
>   "neo4j-admin offline import" + "worker pipeline"。
>
> 下文若提到 `qatlas-raw` 单桶 / `raw.quantum-atlas.ai` 公网域名 / "worker
> 队列 + DLQ" 等早期设计，请视作历史教学材料；现代实施细节请翻 `rustfs.md`。

## 核心原则

QuantumAtlas 处理论文图谱的本质问题是：

- **PDF 是 MB 级 blob**，存量 TB 级，IO 模式是"偶尔整文件下载"。
- **元数据是 KB 级结构化记录**，要事务、要外键、要按字段筛。
- **引用关系是图**，访问模式是 K 跳遍历和图算法（PageRank/Louvain），
  跟前两者完全不同。

强行让一个引擎全包是反模式。**用合适的引擎做合适的事**，三层各管各的，
通过稳定标识符串起来：

| 层 | 引擎 | 存什么 | 数据量级 | 访问模式 | 是否 source of truth |
|---|---|---|---|---|---|
| Raw blobs | **RustFS @ `raw.quantum-atlas.ai`**（S3 兼容） | PDF / XML / MinerU JSON | TB 级 | 偶尔下载整文件，CDN 友好 | ✅ 原文不可变 |
| Metadata | **PocketBase**（嵌入 server，SQLite 底） | papers/authors/refs_raw/raw_url/status | GB 级 | 单条查询、字段筛选、事务 | ✅ 元数据本体 |
| Graph | **Neo4j**（5.26 LTS Community，部署在 graph-host 节点，Bolt 通过 mesh portproxy 暴露给 server 节点） | `(Paper)-[:CITES]->(Paper)` + 少量 hot meta | 几十 GB 级 | K 跳遍历、图算法 | ❌ 可重建的派生视图 |

**这三层不矛盾、不竞争，互补**。raw 量再大 10x，Neo4j 完全不动；Neo4j
脏了，从 metadata DB 跑 `qatlas graph rebuild` 重灌；metadata schema 想
改，迁移 PocketBase，raw / Neo4j 多半不动。

## 三个稳定标识符把它们串起来

```
sha256(content)        ──>  对象存储寻址 (raw/<sha[:2]>/<sha>.pdf)   ← ⚠️ 设想未实现
paper_id (DOI / arXiv) ──>  Metadata DB 主键 + Neo4j 节点 ID
raw_url                ──>  Metadata DB 的 papers 表字段，
                            指向 raw.quantum-atlas.ai 上的 immutable PDF
```

content-addressed 命名（按内容 sha256 分桶）的好处：

- 同一篇 paper 多次上传自动去重。
- ETag = sha256，对账简单。
- 永远 immutable，下游 cache 可永久缓存。
- 备份只需 rsync 一个目录树，不用导出数据库。

> ⚠️ **2026-05-28 现状**：实际生产 object key schema 是 `<kind>/<arxiv-prefix>/<arxiv_id>v<n>.<ext>`（由 `internal/paperassets.AssetKey` 构造，**arxiv-id 寻址，不是 sha256 寻址**）。原因：跨 arxiv_id 重复内容场景极罕见（要不同 paper 内容字节级相同），不值得为它引入分离的 alias DB 做 `arxiv_id → sha256` 反查双重 lookup。
>
> 目前用的是更轻量的折中：
> - **arxiv-id 寻址**保留（路径直接，无 alias 表）；
> - **sha256 存在 object metadata** (`x-amz-meta-sha256`) 做**content-aware idempotency**——同 arxiv_id 重传相同字节 → server 短路 200，**不重写 S3**；不同字节 → 409 with both hashes；`--overwrite` 才覆盖（旧版本由 bucket versioning 保留）。
> - **多 client 并发 race-safe**：upload-pdf / upload-markdown 不再走原来的 "Stat → 决策 → Put" 三段式（中间有 TOCTOU 窗口，两个并发 client 都能通过 "对象不存在" 检查然后都写，silently last-writer-wins）。改用 S3 **conditional PUT**（`If-None-Match: "*"`）：每个 client 直接尝试 create-only Put，server 返回 412 PreconditionFailed 才回退到 Stat+sha256 分类决定 200 / 409。RustFS 完整支持这套 S3 wire 语义（`crates/e2e_test/src/reliant/conditional_writes.rs` 实证），minio-go 通过 `SetMatchETagExcept("*")` 暴露。`LocalStore` 用 `os.Link(tmp, dest)` 拿到 POSIX 原子 create-if-not-exists（EEXIST → ErrPreconditionFailed），同时把 sha256 metadata 写入 `<dest>.meta.json` sidecar 让 dev 行为与 prod 对齐。**并发同字节上传**保证全部 ≤ 1 个 201 + 其余 200/unchanged，**不可能**因为竞速回 409；**并发不同字节**保证恰好 1 个 201 + 其余 409，**不可能**静默覆盖。
> - **client 上传时 `?expected_sha256=`** 做 in-transit 损坏防护（PyPI/Docker 同款）。
>
> 全部细节见 `.github/copilot-instructions.md` "## 写口语义（papers upload）" 节。如未来跨 arxiv 重复率被发现高（罕见，预期 < 1%），再切到真正 content-addressed 存储 + alias DB；目前不值得。

## 拓扑

```
┌──── Client (浏览器 / qatlas CLI) ────┐
│                                       │
│  下载 PDF: 直连 raw.quantum-atlas.ai  │ ← 经边缘 Caddy 反代到 RustFS
│  查 metadata + 图: quantum-atlas.ai   │ ← 边缘 Caddy → 本机 qatlas
└──────────────────┬────────────────────┘
                   │
                   ▼
┌────────────────────────────────────────────────────────────────┐
│ edge-vps — 唯一 qatlas 实例（生产）                            │
│  Caddy :443 → reverse_proxy 127.0.0.1:4200                     │
│  qatlas Go server                                              │
│   ├── PocketBase (papers/refs_raw/...) ← 本机 SQLite           │
│   ├── HTTP API (公网入口)                                       │
│   └── ingest / resolve workers                                 │
│       │                          │                             │
│       │ S3 GET/PUT raw           │ Bolt (cypher)               │
│       ▼                          ▼                             │
└──────┼────────────────────────── ┼─────────────────────────────┘
       │ raw.quantum-atlas.ai      │ bolt://<graph-host>:7687
       │ → <graph-host>:9000       │ 跨内网 mesh，跨海 RTT ≈ 173ms
       │ 内网 mesh                  │ 仅后台 reindex / 低频分析
       ▼                           ▼
┌────────────────────────────────────────────────────────────────┐
│ graph-host (Windows + WSL2, 大内存) — 中转 + Neo4j 宿主         │
│  netsh portproxy v4tov4:                                       │
│    <graph-host>:9000  → <nas-lan>:9000   (RustFS LAN)          │
│    <graph-host>:9001  → <nas-lan>:9001   (RustFS console)      │
│    <graph-host>:7687  → 127.0.0.1:7687   (Neo4j WSL)           │
│                                  │                             │
│                                  │ wslrelay 127.0.0.1 v4       │
│                                  ▼                             │
│  WSL2 Ubuntu 24.04:                                            │
│    systemd: neo4j.service (apt 装 5.26 LTS community)          │
│      0.0.0.0:7687 Bolt  ← preferIPv4Stack=true 纯 v4 socket    │
│      127.0.0.1:7474 Browser UI                                 │
│    跟其他 dockerd 业务容器共享 WSL kernel                       │
│                                                                │
│  ingest 时 Neo4j → NAS LAN http://<nas-lan>:9000               │
│    走 LAN，RTT < 2ms，对吞吐 bound 任务无感                     │
└────────────────────────────────────────────────────────────────┘
              │ LAN, RTT < 2ms
              ▼
┌────────────────────────────────────────────────────────────────┐
│ nas-host (低内存 NAS) — 数据持久化层                            │
│  docker-compose:                                               │
│    rustfs:   ports 9000 (S3) / 9001 (console)                  │
│  （Neo4j 没放这里——NAS 内存太紧，跑 Neo4j 会 OOM）             │
└────────────────────────────────────────────────────────────────┘
```

> 用到的角色：
> - **edge-vps**：跑 qatlas server 的公网节点。代号即可，多边缘节点
>   时常见还有"国内线路镜像 + DNS / hosts 切流量"做法，跟 storage 无
>   关，不在本图展开。
> - **graph-host**：WSL2 大内存节点，apt 装 Neo4j；同时承担 Windows
>   netsh portproxy 把 NAS RustFS 端口暴露到 mesh。
> - **nas-host**：低内存 NAS，docker compose 跑 RustFS（S3 兼容）。

**网络位置**：

- **`raw.quantum-atlas.ai`**：边缘 Caddy 反代到 mesh `<graph-host>:9000`
  → graph-host portproxy → nas-host RustFS `<nas-lan>:9000`。
- **`quantum-atlas.ai`**：edge-vps 本机 Caddy → `127.0.0.1:4200` qatlas。
  qatlas 唯一一份生产实例就在 edge-vps 上。
- **Neo4j Bolt `<graph-host>:7687`（mesh）→ graph-host Windows portproxy
  → 127.0.0.1:7687 → WSL2 localhost forwarding → WSL Neo4j:7687**：
  跟现有 qatlas `:4200` 链路完全同款。Neo4j 跟 RustFS 不在同机——
  低内存 NAS（≤4 GB RAM）扣掉 DSM + RustFS 后留给 Neo4j 不到 2 GB，
  跑 GDS / 大规模 ingest 必 OOM；graph-host 是大内存 Ubuntu WSL2，
  跑 Neo4j 完全无压力。
- **ingest 时 Neo4j 拉 RustFS 数据**：graph-host WSL → NAS LAN，RTT < 2ms。
  比"同机 docker network loopback"慢的几十 µs 对几 MB JSON 拉取**完全
  无感**（ingest 是 throughput-bound，不是 latency-bound）；用低内存
  NAS 跑 Neo4j 换这几十 µs 不划算。
- **延迟前提**：edge-vps → graph-host mesh RTT 视部署而定（跨海可能
  170+ms）。Neo4j 客户端代码里每个 Cypher query 是 1 RTT，
  `/api/graph/stats` 是 5 个串行 query，跨海部署下网络延迟会接近 1s。
  **因此图查询不进 hot path**，只用于：
  - 后台 reindex 工作流（每次 ingest 完成后异步推 graph，跨海无所谓）
  - 内部分析向 `/api/graph/{stats,query,schema}` 调用
  - 低频管理工具（CLI 倒图、跑 GDS 算法）
  公网用户面的 metadata 检索全部走 edge-vps 本机的 PocketBase，**完全
  不依赖 Neo4j 可达**——Neo4j 在 client 代码里是 optional 模式（URI
  空时返回 200 + error，不 crash），graph-host / mesh 任何一跳挂都只
  让图相关 API 暂时降级，不影响主体。
- 边缘 Caddy `0.0.0.0:443` 只反代 qatlas HTTP，不直接暴露 Bolt 到公网。
- **未来优化方向**：NAS 内存升级到 ≥ 16 GB 后可以把 Neo4j 挪 NAS docker
  跟 RustFS 同 compose，少一跳 mesh，多一个 loopback ingest 路径。本期
  按 NAS 内存现实约束走 graph-host。

## 完整 ingest pipeline

!!! note "v0.7.0+ 实施已收敛为同步路径"
    下面这套 "upload → ingest worker → resolve worker → graph loader worker → 状态机"
    流水线是**早期设计**。生产 v0.7.0 起：upload handler **同一个 HTTP 请求里**完成
    S3 写入 + Neo4j MERGE，不再有 worker queue + DLQ 链；Neo4j 挂时 fail-open on
    write（S3 仍写成功，回 `201 + X-Catalog-Sync: deferred`，由 `qatlasd
    papers sync` 兜底）。OpenAlex 批量灌入走 `qatlasd openalex bootstrap`
    CLI，**不**走下方 worker pipeline。下方流水线保留作为"为什么早期设计成 worker
    队列、为什么后来收敛"的教学材料。

```
1. user POST /api/papers/<id>/upload-pdf  (qatlas)
   ├─ 算 sha256
   ├─ HEAD raw bucket 看是否已有（去重）
   ├─ 没有就 PUT 到 raw/<sha[:2]>/<sha>.pdf
   └─ 写库: papers.raw_url = "https://raw.quantum-atlas.ai/raw/ab/abcd....pdf"
            papers.sha256 = "abcd..."
            papers.status = "uploaded"

2. qatlas 返回 200 给用户                ← 几百 ms，用户感知就是上传成功

3. ingest worker 拉队列（status = uploaded）:
   ├─ 从 raw 拉 PDF（内网走 mesh IP，不计 egress）
   ├─ MinerU/GROBID 抽取 → metadata JSON + refs_raw[]
   ├─ 写 PocketBase: papers.{title,authors,abstract,year,refs_raw}
   └─ papers.status = "extracted"

4. resolve worker 拉队列（status = extracted）:
   ├─ 对 refs_raw 每一条:
   │  ├─ 优先 DOI 精确匹配
   │  ├─ 否则 CrossRef/OpenAlex 模糊搜
   │  └─ matched → 写 ref_edges(src_id, dst_id, status='pending_sync')
   ├─ 没 match 上的进 unresolved_refs 表（人工或 LLM 兜底）
   └─ papers.status = "resolved"

5. graph loader 拉 ref_edges WHERE status='pending_sync':
   ├─ batch 1000 条
   ├─ Cypher UNWIND + MERGE 写 Neo4j（幂等）
   └─ ref_edges.status = 'synced'

6. 用户访问 /papers/<id>/cites-graph:
   ├─ qatlas Bolt 查 Neo4j:
   │  MATCH (p:Paper {id: $id})-[:CITES*1..2]->(q) RETURN q LIMIT 100
   ├─ 拿到 q.id 列表
   └─ 去 PocketBase hydrate title/authors/year 给前端
```

**关键解耦点**：

- **HTTP 请求里永远不同步等图写入**。上传 200 ≠ 图已更新。前端要
  show progress 就 poll `papers.status` 字段。
- **每一步只看上一步的 status 字段**，worker 之间无直接耦合，崩了任
  一个，重启后从上次的 status 继续。
- **失败进 dead letter queue**（DLQ）：MinerU 抽不出 reference 的进
  `extract_failed`、resolve 不到的进 `unresolved_refs`，运维或 LLM
  兜底定期重试。

## 在线 vs 离线：三场景三种打法

### 场景 A. 冷启动 / 历史回填 → **离线 batch**

raw 里已经积累了一堆 PDF 想一次性灌进图。

- 跑 worker 把所有 PDF 走完 extract + resolve，产物落到
  `papers.csv`（id, title, year, ...）和 `cites.csv`（src_id, dst_id）。
- 用 **`neo4j-admin database import full`**（offline tool，target 必须
  空库）一次性导入。**比在线 `MERGE` 快 10–100x**，10M paper +
  100M 引用关系几小时搞定。

```bash
neo4j stop
neo4j-admin database import full neo4j \
  --nodes=Paper=/tmp/papers.csv \
  --relationships=CITES=/tmp/cites.csv \
  --overwrite-destination=true
neo4j start
```

### 场景 B. 日常增量（用户上传新 paper）→ **在线 incremental**

走上面整条 ingest pipeline。Cypher 用 MERGE 保证幂等：

```cypher
UNWIND $batch AS row
MERGE (p:Paper {id: row.src_id})
MERGE (q:Paper {id: row.dst_id})
MERGE (p)-[:CITES]->(q)
```

重复上传不会脏图。

### 场景 C. 关系修复 / 算法跑批 → **离线 batch（cron）**

周期性活儿：

- 重新 resolve 之前没匹配上的 reference（OpenAlex 数据每周更新）。
- 跑 GDS PageRank / Louvain，把社区标签 / 影响力分数**写回 PocketBase
  的 papers 表**（不是写回 Neo4j——派生指标应该跟 metadata 一起活）。

可以用 `apoc.periodic.iterate` 在线跑，也可以 dump 一份图到独立
analytics 实例跑（避免影响线上读 QPS）。

## 容量估算

按 **100 万 paper、平均 30 条 reference** 估算：

| 层 | 存储 | 单价 / 物理位置 |
|---|---|---|
| Raw PDF | 100w × 2 MB ≈ **2 TB** | RustFS @ `raw.quantum-atlas.ai`，挂在 NAS 大盘上 |
| Metadata DB | ~1 GB（每篇 paper meta 几 KB） | PocketBase pb_data，本机 SSD |
| ref_edges 表 | 3000w × ~50B ≈ **1.5 GB** | PocketBase |
| Neo4j store + 索引 | 100w 节点 + 3000w 关系 ≈ **3–5 GB** | 本机 SSD，page cache 配 4–8 GB |

**raw 量再涨 10x（千万 paper、20 TB PDF），Neo4j 还是只要几十 GB**——
图库只存"id + 关系"，存储成本基本不随 PDF 大小变化。这是这种分层
架构能扛任意规模的根本原因。

## 反模式

只要不踩这两个反模式，对象存储和 Neo4j 完全是好邻居：

### ❌ 反模式 A：把 PDF 塞进 Neo4j 节点属性

```cypher
CREATE (:Paper {id: '...', pdf_bytes: '<2MB binary>'})
```

Neo4j property store 不是为 MB 级 blob 设计的——会让 store 爆炸、
page cache 失效、备份变慢、bolt 协议传输超时。**PDF 永远只通过 raw_url
字段引用**。

### ❌ 反模式 B：把图关系编码到 PDF 元数据塞回对象存储

把"这篇 paper 引用了哪些"当 JSON 写回 RustFS，查图时去 RustFS 拉所
有 JSON 自己 join——退化成"对象存储当 KV 库用"，K 跳查询性能直接归
零。**所有关系数据只在 PocketBase + Neo4j 两层**。

### ❌ 反模式 C：Neo4j 当 source of truth

如果 Neo4j 里有的边在 PocketBase `ref_edges` 表里找不到出处，就说明
有人绕开 pipeline 直接写 Neo4j。**这会让"重建图"操作丢数据**。所有
写图操作都必须经过 graph loader worker，且 worker 写完后 ref_edges 行
状态变 `synced`——这是"图是 metadata 的派生视图"的可验证保证。

## RustFS 部署后置：bucket / user / policy 自助配置 {#rustfs-bucket-user-policy}

!!! note "v0.7.0+ 实际部署已扩展到 5 桶 + 1 事件桶"
    下方"当前状态（2026-05）"表格描述的**早期单桶 `qatlas-raw`** 设计现在已经拆为
    5 个资产桶（`qatlas-{raw,pdf,md,images,openalex}`）+ 1 个事件桶
    （`qatlas-s3-events`，T10 notify webhook → Fluent Bit → 写入留痕）。
    bootstrap 脚本拆为 `scripts/rustfs_bootstrap.sh`（5 资产桶 + svcacct）
    和 `scripts/rustfs_notify_bootstrap.sh`（事件桶 + 5 桶 webhook subscribe）。
    边缘 Caddy 公网入口由 `QATLAS_S3_PUBLIC_ENDPOINT` env 控制，**两台 edge
    各自配自己的**（RackNerd 用域名 + LE 证书，Alibaba 用 IP + 自签）。
    最新桶布局、IAM 名、bootstrap 脚本签名、ARN 大小写陷阱、QUEUE_DIR 可写陷阱
    等运维细节见 [`docs/deployment/rustfs.md`](../deployment/rustfs.md)，
    下方表格仅作"为什么最早只有一个桶"的设计回顾。

RustFS 装好 root 凭据就 god mode 了，但**生产 server 不能用 root 调 S3
API**——一旦 server 被攻陷，攻击者能列所有 bucket、改任何对象、建任意
用户。生产 server 必须用绑死到 `qatlas-raw` 桶的 IAM 子用户，权限只够
GetObject/PutObject/DeleteObject/ListBucket（+ GetBucketLocation）。

### 当前状态（2026-05）

| 资源 | 名称 | 说明 |
|---|---|---|
| RustFS 实例 | NAS Docker `rustfs:9000` | named volume `<stack>_rustfs_data`，落到 `/volume1/@docker/volumes/.../_data` |
| 公网入口 | `https://raw.quantum-atlas.ai` | 边缘 Caddy → graph-host netsh portproxy 9000 → mesh `<graph-host>:9000` → NAS Docker |
| Bucket | `qatlas-raw` | private，content-addressed 命名 `raw/<sha[:2]>/<sha>.pdf` |
| Policy | `qatlas-raw-rw` | get/put/delete on object + list/getLocation on bucket，ARN 钉死 `qatlas-raw` |
| IAM user | `qatlasd` | 启用，已 attach `qatlas-raw-rw` |
| Access key | （Phase 3 写 server `.env` 时再生成新对） | 见下面 bootstrap 脚本 |

> RustFS root 凭据**永不进 server `.env`**、永不进任何 git 仓库——只
> 在维护者密码管理器和 RustFS 容器自己的 env vars 里活。Server 用的
> 是 `qatlasd` 子用户的 access_key + secret_key，权限钉死单桶。

### 一键 bootstrap：`scripts/rustfs_bootstrap.sh`

幂等脚本，可重复跑：

- 缺什么补什么（bucket / policy / user / attach 关系），都在则跳过
- **每次跑都新增一对 access key**（旧 key 不动；rotate 是显式动作）
- 用 MinIO Client (`mc`) 调 RustFS 的 admin API（RustFS 兼容 MinIO admin）
- `mc` binary 下到 `mktemp -d` 退出时 trap 销毁；root 凭据走 env var
  `MC_HOST_<alias>` 不落盘；脚本不写任何持久文件

用法：

```bash
export RUSTFS_ENDPOINT=https://raw.quantum-atlas.ai
export RUSTFS_ROOT_ACCESS_KEY=<root_ak>          # 查密码管理器，不在 git
export RUSTFS_ROOT_SECRET_KEY=<root_sk>
bash scripts/rustfs_bootstrap.sh
# 末尾输出 Access Key / Secret Key，复制到 server .env
```

开新桶 / 新子用户场景（不影响现有 `qatlas-raw`）：

```bash
BUCKET=qatlas-snapshots USER=snapshots-writer POLICY=qatlas-snapshots-rw \
  bash scripts/rustfs_bootstrap.sh
```

### Rotate access key 流程

`bootstrap.sh` 每次跑只**新增**不删，便于 zero-downtime rotate：

1. 跑 bootstrap，拿到新 access_key / secret_key
2. 改 server `.env` 把新 key 写进去，重启 server
3. 确认服务正常后用 mc 删旧 key：
   ```bash
   mc admin user svcacct rm <alias> <old_access_key>
   ```

### 边缘 Caddy 入口（已上线）

```caddy
raw.quantum-atlas.ai {
    reverse_proxy <graph-host>:9000     # mesh → graph-host portproxy → NAS Docker rustfs:9000
    import error_pages                   # 站点级 onerror handler
}
```

实际链路与本文档顶部 `## 拓扑` 图里"client 直连 RustFS、不经 VPS"的
**早期设想不同**——RustFS 容器只暴露给内网 mesh，公网 TLS 由边缘
Caddy 终结后反代，PDF 流量会绕边缘节点一程。多边缘节点部署时按相同
模板加反代块；具体哪些边缘已上线见边缘节点 / 网络拓扑文档。

## 备份与灾难恢复

### Raw（RustFS）

- content-addressed 存储意味着**rsync 即备份**。
- 推荐周期：每天/每周 rsync 到独立挂载点 `<backup-mount>/backups/raw/`（独立
  NAS 卷 / 异地存储均可）。
- 灾难恢复：rsync 反向拉回即可。
- 因为 immutable，备份只需要 append-only，从不要删旧版本。

### Metadata DB（PocketBase）

走 QuantumAtlas 现有的 pb_data 备份机制（参见 deployment 文档）。
**这是 source of truth，备份频率应最高**（每日 + 多代保留）。

### Neo4j

**优先级最低**——丢了从 metadata DB 重建即可。但仍然建议（graph-host
WSL cron）：

```bash
# graph-host WSL: crontab -e
0 3 * * * sudo systemctl stop neo4j && \
          sudo -u neo4j neo4j-admin database dump neo4j system \
            --to-path=<backup-mount>/backups/neo4j/$(date +\%Y\%m\%d) && \
          sudo systemctl start neo4j
```

- `system` 库**必须一起 dump**：恢复时只 load `neo4j` 库不 load `system`
  库，用户/权限/database catalog 全丢。
- 凌晨停机 1–2 分钟可接受；不能接受得上 Enterprise Edition（在线
  backup 是付费特性）。
- backup 落 `<backup-mount>/backups/neo4j/`——可以是独立 NAS 卷，也可以
  再用 `mc cp` 推一份到 RustFS bucket 做异地。
- 真不愿停机的兜底：从 metadata DB `qatlas graph rebuild` 到新实例。

**重建优先级**：

1. **从 metadata DB 重建** > dump 恢复 > offline import
2. 因为 metadata DB 是 source of truth，从它跑 `qatlas graph rebuild`
   拿到的图永远最新最干净。
3. dump 适合"昨天的图还好，今天误删了节点"这种点状回滚。
4. offline import 适合"想换 store 格式 / 想清理一波"的大改。

## 跟现有架构的关系

### 跟 `architecture.md` 的关系

- `architecture.md` 描述的是项目整体分层（应用代码 / Wiki / RAW
  资产 / Neo4j），本文档是其中 RAW 资产层 + Neo4j 层的**细化设计**。
- "Primitive 的三层表示"（YAML / Wiki / Neo4j）和本文档讲的"Paper 引
  用图"是**两套并存的 Neo4j 内容**：前者由 wiki sync 写入，后者由
  ingest pipeline 写入。两者在 Neo4j 里通过 label 区分（`Primitive` /
  `Algorithm` 是 wiki 同步出的；新加的 paper 引用边在 `Paper` label
  上加 `:CITES` 关系）。
- 当前 `internal/neo4j/client.go::DefaultLabels` 列了
  `Primitive/Algorithm/Paper/Implementation` 四种。引入 paper 引用
  pipeline 时**复用 `Paper` label**，只是新加 `:CITES` 关系类型，不
  破坏现有 wiki sync 行为。

### 跟 `migration-storage-layout.md` 的关系

- 那篇讲的是把 RAW_DIR 从仓库内搬到 XDG / 挂载点（文件系统层面）。
- 本文档讲的是把 RAW_DIR 进一步演化成 **S3 兼容对象存储**（协议层面）。
- **两套可以并存**：早期开发机继续用 `QATLAS_RAW_DIR=/local/path`，
  生产/线上切到 `QATLAS_RAW_BUCKET=s3://raw.quantum-atlas.ai/`。

### 跟 `graph-visualization-research.md` 的关系

- 那篇讲前端怎么渲染 Neo4j 查询结果。本文档讲后端怎么把数据灌进
  Neo4j。**前端图库选型与后端图谱内容无关**：无论图来自 wiki sync 还
  是 paper 引用 pipeline，前端 Cytoscape.js 都按 `{nodes, edges}` 渲染。

## Neo4j 版本选择：5.26 LTS（钉死）

> **结论**：上 **Neo4j 5.26 LTS Community Edition**。不上 calendar
> release（`2026.xx`），也不要等下一个 LTS。

### 现状（2026-05）的版本号怪谈

Neo4j 在 2024–2025 年改了版本号方案，**没有 Neo4j 6**：

| Track | 当前版本 | 节奏 | 单版支持期 |
|---|---|---|---|
| **LTS** | **5.26.x** | 每月小修 | 长期（数年） |
| **Calendar release** | `2026.04.0` | 每月一发新功能版 | 约 6 个月 |
| **下一个 LTS** | 预计 2026 年底 | 也不会叫 "6"，类似 `2026.xx LTS` | 长期 |

### 为什么选 5.26 LTS

1. **LTS 才是免迁移路径**。calendar release 半年 EOL，跟"省事"反着来。
2. **JRE 镜像自带**。`neo4j:5.26-community` docker 镜像内置 Eclipse Temurin
   JRE 21，NAS 上完全不需要装 Java。
3. **APOC / GDS 这两个核心插件**（PageRank / Louvain 全靠 GDS）在 5.26
   上版本对齐最稳；calendar release 上 plugin 经常滞后。
4. **driver 完全不受影响**。`internal/neo4j/client.go` 引用的
   `github.com/neo4j/neo4j-go-driver/v6 v6.1.0` 是 **driver 主版本号
   v6**，跟 server 版本号是两码事——driver v6 同时支持 server 5.x 和
   2026.xx。换 server 不动 Go 代码、不动 go.mod。

### Python driver 一处不一致（清理项）

`pyproject.toml` 第 23 行：`"neo4j>=5.15.0,<6"`（v5）。

Go 用 v6，Python 用 v5——两边都能连 server 5.26，但版本号给人误导。
后续清理建议把 Python pin 升到 `<7` 跟 Go 对齐：

```toml
"neo4j>=5.15.0,<7",
```

不阻塞部署。

### 装法（Neo4j @ graph-host WSL2 apt）

三个 phase。Neo4j 用 **apt 装** 5.26 LTS community 跑在 graph-host 的
WSL2 Ubuntu 24.04 内（systemd 服务 `neo4j.service`），跟其他业务容器共享
WSL kernel 不抢资源。**不**跟 NAS RustFS 同机——NAS 内存太紧，跑 Neo4j
必 OOM；graph-host WSL 有大内存完全无压力。

> **为什么 apt 不 docker**：apt 装的 systemd 服务比 docker compose 少
> 一层容器抽象，配置文件就在 `/etc/neo4j/neo4j.conf`，journal 直接
> `journalctl -u neo4j` 看。业务容器走 docker 是因为镜像分发 / 多容器
> 编排需求；Neo4j 单进程 + 单数据目录，apt 包反而更顺手。两种装法都
> work，本部署选 apt。

ingest 时 graph-host Neo4j → NAS LAN `http://<nas-lan>:9000` 拉 RustFS
数据，RTT < 2ms，对 throughput-bound 的 ingest 无感。

#### Phase 1: graph-host WSL 装 Neo4j（apt）

在 graph-host WSL Ubuntu 24.04 内（需要 sudo）：

```bash
# 加 Neo4j 5 LTS apt repo
wget -O - https://debian.neo4j.com/neotechnology.gpg.key | sudo gpg --dearmor -o /usr/share/keyrings/neotechnology.gpg
echo 'deb [signed-by=/usr/share/keyrings/neotechnology.gpg] https://debian.neo4j.com stable 5' | sudo tee /etc/apt/sources.list.d/neo4j.list
sudo apt update
sudo apt install -y neo4j   # 5.26.x LTS community

# 改配置
sudo $EDITOR /etc/neo4j/neo4j.conf
#   server.bolt.listen_address=0.0.0.0:7687                   ← 取消注释 + 改 0.0.0.0
#   server.jvm.additional=-Djava.net.preferIPv4Stack=true     ← 追加 1 行（#14154 fix，见下）
#   (按需调内存：server.memory.heap.max_size=8g
#                server.memory.pagecache.size=4g)

# 设密码（首次启动前用 neo4j-admin 写 initial password；后续在 cypher-shell 里改）
sudo neo4j-admin dbms set-initial-password '<24+ 字符强随机>'

sudo systemctl enable --now neo4j
sleep 30                                   # JVM 启动 + bind 7687 慢，约 20-30s
journalctl -u neo4j -n 20 --no-pager       # 找 "Bolt enabled on 0.0.0.0:7687"

ss -tlnp | grep :7687
#   必须是 0.0.0.0:7687（纯 v4）。如果显示 *:7687（dual-stack v6），
#   preferIPv4Stack 没生效，回头检查 conf 的拼写并 restart。
```

**为什么 `0.0.0.0:7687` + `preferIPv4Stack=true`**（关键，**两条缺一不可**）：
graph-host 是 WSL2 NAT 模式，Bolt 经 wslrelay → Windows `127.0.0.1` → portproxy → mesh。
踩 [microsoft/WSL#14154](https://github.com/microsoft/WSL/issues/14154)：
JVM 默认开 dual-stack v6 socket（即使配置文件写 `listen_address=0.0.0.0`，
Java 也会落到 `*:7687` 形态，socket 是 AF_INET6 + V6ONLY=0），wslrelay
只在 Windows 建 `[::1]:7687` listener **不补 `127.0.0.1:7687`**，
portproxy `connectaddress=127.0.0.1` 立刻 RST。`-Djava.net.preferIPv4Stack=true`
强制 JVM 走纯 v4 socket（`ss` 显示 `0.0.0.0:7687`），wslrelay 才会补出
Windows 端 v4 listener，整条链路才通。完整 socket 形态对照表 + 修复推荐
见 `.agents/skills/software/references/network.md` "WSL2 容器端口的
wslrelay / IPv6 dual-stack 坑" 一节。

至于"`0.0.0.0` 不是让 WSL 内其他业务容器也能扫到 Bolt 吗"——是。但 WSL
内业务容器跟 Neo4j 同 kernel network namespace，绑 `127.0.0.1` 还是
`0.0.0.0` 对它们的访问控制等价；真正的访问边界在 Windows host 的
portproxy `listenaddress=<graph-host>`（mesh IP，只允许 mesh peer 进），
加上 Bolt 强密码。

#### Phase 2: Windows host portproxy

Windows PowerShell（管理员）：

```powershell
netsh interface portproxy add v4tov4 listenaddress=<graph-host> listenport=7687 connectaddress=127.0.0.1 connectport=7687
netsh interface portproxy show v4tov4 | findstr 7687
```

预期看到：

```
<graph-host>    7687        127.0.0.1       7687
```

这跟现有 `<graph-host>:4200 → 127.0.0.1:4200`（qatlas dev）同款链路。

#### Phase 3: server 节点配 .env 重启 qatlas

在跑 qatlas server 的节点（edge-vps）上：

```bash
$EDITOR <repo>/.env
# 三行：
#   NEO4J_URI=bolt://<graph-host>:7687
#   NEO4J_USER=neo4j
#   NEO4J_PASSWORD=<跟 graph-host 上 neo4j-admin 设的同一个值>

sudo systemctl restart qatlas.service     # 或 user 单元：systemctl --user restart qatlas
sleep 3
curl -s http://127.0.0.1:4200/api/graph/stats | jq
# 一开始全 0 是正常的：
#   { "nodes": 0, "relationships": 0, "labels": [...], "label_counts": {...} }
# 如果返回 { "error": "..." }，按这顺序排查：
#   1. graph-host WSL: systemctl status neo4j 是否 active
#   2. graph-host WSL: nc -vz 127.0.0.1 7687 通不通
#   3. graph-host Windows: nc -vz 127.0.0.1 7687（验 WSL → Windows localhost fwd）
#   4. server 节点: nc -vz <graph-host> 7687（mesh + portproxy）
```

#### Phase 4（可选硬化）：限制 Bolt 来源

Neo4j Community 默认无 TLS，靠 mesh 加密通道传 Bolt 凭据。mesh 上其他
节点能从 `<graph-host>:7687` 扫到端口，被攻破可对 Bolt 爆破密码。

简单缓解：Windows Firewall 加 inbound rule，限制 `<graph-host>:7687`
只允许 server 节点的 mesh IP 入。本期先靠强密码兜底，规模化前再做硬化。

### Neo4j 跟 RustFS 不同机的 ingest 路径

把 Neo4j 放 graph-host 而不是 NAS，意味着 ingest 时 Neo4j 拉 RustFS
数据要跨机走 LAN。三条路径都可用：

1. **APOC LOAD JSON from S3 URL**：reindex 时 Cypher 里
   `CALL apoc.load.json("http://<nas-lan>:9000/qatlas-raw/...")`，LAN
   RTT < 2ms，对 ingest 完全无感。注意要从 NAS LAN IP 走，不要走
   `raw.quantum-atlas.ai` 公网入口（会绕回 edge-vps → mesh → graph-host
   portproxy → NAS，多 6 跳还可能跨海）。
2. **qatlas Go 侧 worker 拉 + 推 Cypher**：worker 跑在 edge-vps 上，从
   RustFS 拉数据（mesh 跨距，但只在 worker 启动批次时拉一次），解析后
   批量 `UNWIND ... CREATE` 推到 graph-host Neo4j。**推荐方式**——可控、
   可观测、对 APOC 行为透明。
3. **Backup dump 推 RustFS**：cron `neo4j-admin database dump` 到本地
   volume，再用 minio-go SDK 推到 `s3://rustfs/backups/neo4j/`（Community
   没有原生 S3 sink）。

Neo4j 数据本身落 apt 默认路径 `/var/lib/neo4j/data/`（graph-host WSL2 内 ext4）——不要
改 `server.directories.data` 指网络挂载（NFS / CIFS / SMB 等），这些
通常不保证 fsync 语义，会数据损坏。

## 演进路线

| 阶段 | 触发条件 | 工作内容 |
|---|---|---|
| **P0** | — | RAW_DIR 本地、`internal/neo4j/client.go` 客户端代码 ready（driver v6.1.0、`/api/graph/{stats,query,schema}` 三端点 wire）、**Neo4j server 未部署** |
| **P1**（部署起步，**已完成 + sha256 dedup / versioning extension**） | 决定上 Neo4j | graph-host WSL2 **apt 装** `neo4j` 5.26 LTS（systemd 服务，**不是 docker**）、`/etc/neo4j/neo4j.conf` 改 `server.bolt.listen_address=0.0.0.0:7687` + `server.jvm.additional=-Djava.net.preferIPv4Stack=true`（#14154 fix）、Windows host 加 `<graph-host>:7687 → 127.0.0.1:7687` portproxy（写到 registry persistent）、server 节点填 `.env` `NEO4J_*`、`/api/graph/stats` 返回真 0 ✅；RustFS 已部署在 NAS Docker（bucket `qatlas-raw`、user `qatlasd`、policy `qatlas-raw-rw` 见 `scripts/rustfs_bootstrap.sh`）、**Go server 已接 minio-go**（`internal/objstore.{LocalStore,S3Store}`，`QATLAS_S3_*` 四字段 all-or-nothing 切换，详见 `.env.example`）、生产 RackNerd 已切到 S3（endpoint mesh `http://10.144.18.10:9000`）；**2026-05-28 加上 content-aware idempotency**（upload sha256 入 `x-amz-meta-sha256` metadata，重传同字节短路 200/unchanged，不同字节 409 带两个 hash；详见 copilot-instructions.md "写口语义"段）+ **bucket versioning qatlas-managed**（`S3Store.EnsureVersioning` 启动自动开启 + policy 加 `s3:Put/GetBucketVersioning`）+ **client `?expected_sha256=` in-transit guard** |
| **P2** | 有 paper 进来要测引用图 | 实现 extract worker（MinerU 调用 + refs_raw 落库）+ resolve worker（CrossRef/OpenAlex 匹配），先**不写 Neo4j**，refs 仅入 ref_edges 表 |
| **P3** | ref_edges 表积累几万行 | 实现 graph loader worker + `qatlas graph rebuild` CLI；写第一批 `:CITES` 边到 Neo4j |
| **P4** | 节点数破百万 | 切换冷启动路径到 offline import（`neo4j-admin database import full`）；写部署 cron 备份；接 GDS 算法跑 PageRank/Louvain 写回 metadata |
| **P5** | 用户提复杂图查询 | 上 Cytoscape.js 前端（参见 graph-visualization-research.md）；评估是否要 read replica |

**当前位置：P1 → P2 过渡**。Neo4j server 在 graph-host WSL2 apt 装好且端到端可达
（`/api/graph/stats` 从 server 节点 qatlas 调通），客户端代码完备，RustFS 已
部署且 bucket/user/policy 已配齐（见 `scripts/rustfs_bootstrap.sh`），
**Go server 已接 minio-go**——`internal/objstore` 抽象层 + `internal/routes/{papers,shares}` 全部经过 Store 接口，
`QATLAS_S3_*` 四字段填齐就切 RustFS（PUT/GET/Presign 全走 S3，share URL 直接 302 到 presigned），
留空就 fallback LocalStore（dev/CI 无外部依赖）。**下一步 P2** 是实现 paper
ingest pipeline（MinerU 调用 + refs_raw 落库 + resolve），先**不写 Neo4j**，
refs 仅入 ref_edges 表，等 P3 再灌图。

## 待决定的设计点

留给未来 PR / RFC 决议：

- **S3 client 库选型**：~~`minio-go` vs aws-sdk-go-v2~~ — **已选 minio-go**
  （v7.2.0，引入于 `internal/objstore/s3.go`），理由：包小（~3MB vs ~30MB）、
  跟 RustFS 同血脉、API 简洁。
- **去重粒度**：sha256 整文件去重 vs DOI 去重——前者实现简单但同篇
  paper 的预印本/出版版会算两份；后者更"正确"但 DOI 不一定可拿到。
  倾向"两个 key 都存，sha256 做物理去重，DOI 做逻辑去重"。
- **CDN**：边缘节点是否需要 cache raw（PDF 命中重复的场景多吗？）。短期
  不做，等流量数据说话。
- **Neo4j vs 替代**：Neo4j 是当前默认选项，若 wiki sync 那套 +
  paper 引用图加起来始终在百万节点以下，可以考虑 Memgraph（资源
  减半、Cypher 兼容）；详见 `graph-visualization-research.md` 不涉及
  但本文档需要的"图库选型"讨论留给单独 RFC。
- **`refs_raw` 是否进 PocketBase 还是单独 PG**：百万级 ref_edges 行
  PocketBase（SQLite 底）能扛，但分析查询慢。如果将来要在 metadata
  层跑 join 分析，考虑迁 PostgreSQL。

## 参考链接

- RustFS（S3-compatible Rust object storage）：<https://rustfs.com/>
- Neo4j admin import 文档：<https://neo4j.com/docs/operations-manual/current/tools/neo4j-admin/neo4j-admin-import/>
- APOC periodic.iterate：<https://neo4j.com/docs/apoc/current/overview/apoc.periodic/apoc.periodic.iterate/>
- MinerU（PDF 抽取）：<https://github.com/opendatalab/MinerU>
- OpenAlex API（reference resolve）：<https://docs.openalex.org/>
- CrossRef API：<https://api.crossref.org/>
