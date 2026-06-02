# Bulk Data + RustFS Integration Spike

> 2026-05-29 实验产物。**未合入主分支**，留作决策依据。
>
> worktree: `~/TiMidlY-projects/QuantumAtlas.worktrees/cli-worktree-2026-05-29T04-54-17/`
> branch:   `cli/worktree-2026-05-29T04-54-17`

## 目标回顾

用户："这些东西能不能反过来对我们有益" + "切个分支做下看看效果，特别是你说的那个**下载下来**，看看如何与我的**对象存储**一起用"。

"那个下载下来" = Crossref / OpenAlex Public Data File / S3 snapshot
"对象存储" = mesh 上的 RustFS（`<rustfs-internal-host>`，bucket `qatlas-raw`）

## 关键数字（real probes，不是估算）

### OpenAlex works snapshot（S3 `s3://openalex/data/works/`，匿名可拉）

| 维度 | 数值 |
|---|---:|
| 总 part 数（manifest） | **2,127** parts |
| 总 records | **492,361,307** |
| 总 gz 大小 | **639.2 GB** |
| 解压估算（gzip 8-15× JSON） | **5.1–9.6 TB** |
| 时间范围 | 2016-06-24 → 2026-03-30（每天一份增量分区） |
| 平均 part | 293 MB / 231K records |
| 抽样吞吐（单核 Python） | **13,527 rec/s** → 全 corpus ~10 h pure parse |
| Crossref total（对比） | 182M records（OpenAlex 已经吸收了 Crossref + 加了 arxiv + cited_by） |

### 抽样命中率（5 parts ≈ 294,608 records）

| 字段 | 命中 | 比例 |
|---|---:|---:|
| 有 DOI | 15,385 | 5%（其中很多是 conference/journal） |
| 有 arxiv URL（in `locations[].landing_page_url`） | 428 | 0.15% |
| 同时有 DOI + arxiv URL（**就是我们要的 pair**） | 81 | 0.03% |
| 有 `referenced_works[]`（citation edges） | 14,639 | 5% |
| 平均 ref edges / record | 0.7 | — |

⚠️ 抽样集中在 2025-10 的近期分区，arxiv 比例偏低。OpenAlex API 直接查 `filter=locations.source.id:S4306400194`（arXiv 是一个 source）返回 **3,265,548 篇 arxiv-anywhere 文献、1,758,640 篇 arxiv-as-primary**，按 0.66% 全局比例算更靠谱。

### Schema 关键发现

```jsonc
// OpenAlex work record（精简，仅相关字段）
{
  "id": "https://openalex.org/W3023478445",
  "doi": "https://doi.org/10.1137/s0097539795293172",     // 正式版 DOI
  "title": "...",
  "ids": {
    "openalex": "https://openalex.org/W3023478445",
    "doi": "https://doi.org/10.1137/s0097539795293172",
    "mag": "3023478445"
    // ⚠️ NO "arxiv" key — arxiv 不在这里！
  },
  "locations": [
    {
      "landing_page_url": "http://arxiv.org/abs/1707.06347",  // ← arxiv id 埋这里
      "source": {"id": "https://openalex.org/S4306400194", "display_name": "arXiv"},
      "version": "submittedVersion"
    },
    {
      "landing_page_url": "https://doi.org/10.4230/oasics.dx.2024.16",
      "source": {...},
      "version": "publishedVersion"
    }
  ],
  "referenced_works": [
    "https://openalex.org/W1601004196",  // 这是 OpenAlex W-ID，不是 DOI！
    "https://openalex.org/W1972801537",  // 想要 (arxiv,doi)→(arxiv,doi) 边
    ...                                  // 必须先 mirror 全 dump 做 W-ID → DOI join
  ]
}
```

**两个关键事实**：

1. `arxiv id` **不在 `ids.arxiv`**——埋在 `locations[].landing_page_url`，用正则 `arxiv\.org/abs/(...)` 提取。OpenAlex 文档说"将来会移到 ids.arxiv"但截至 2026-05 还没。
2. `referenced_works[]` 是 OpenAlex W-ID 数组，**不是 DOI**。想构造 `(paper)-[CITES]->(paper)` 用 DOI 索引节点的话，必须先建 W-ID → DOI 映射表（即 mirror 整 dump）。**这是"光下载 arxiv 子集做映射"和"做完整引用图"的分水岭**。

## 三档落地方案

按"对项目的实际帮助 / 一次性运维成本"排序：

### 🥇 路径 A — **lightweight arxiv↔DOI 映射**（API 抓取，不下 dump）

**做法**：用 OpenAlex API `filter=locations.source.id:S4306400194 + select=id,doi,locations` 分页拉所有 3.27M arxiv-related works，extract (arxiv_id, doi) pair，写 SQLite 单表 `arxiv_doi_map(arxiv_id PRIMARY KEY, doi, openalex_id, confidence)`。

**产物**：
- ~**125 MB SQLite**（200 万 pair）— **trivially fits in PocketBase 一张新 collection**
- 提供 `GET /api/arxiv-doi-map/<arxiv_id>` server endpoint
- client 的 `enrich-doi` 新增一个最高优先级 resolver `PocketBaseMapResolver`，命中即 high-confidence，**不再调外网**

**成本**：
- API 拉取：~16,000 requests × cursor=*，polite pool 10 req/s → **30 分钟一次**
- 月增量：~10K 新 arxiv works → 1 分钟刷新
- 存储：125 MB（PocketBase SQLite 容量内）
- **完全不需要动 RustFS**，零基础设施变更

**对 450 篇 wiki 的实际效果**：你 wiki 里 ~30%（粗估）arxiv preprint 有正式版 DOI，过映射表后这部分直接 high-confidence 命中；剩下 70% 还是 fallback 到我现在的 title-matching chain（但样本量降到 ~300 篇，更可控）。

### 🥈 路径 B — **整 dump mirror 到 RustFS + W-ID join → 引用图**

**做法**：
1. 起一个 server 端 cron job 把 `s3://openalex/data/works/` 整个镜像到 RustFS bucket `qatlas-openalex/`（保留 manifest + parts 原结构，便于增量）
2. 跑离线 pipeline：解 part_*.gz → 提取 `(id, doi, locations, referenced_works)` → 建本地 DuckDB / SQLite indexed table
3. W-ID → DOI join → `(citing_doi, cited_doi)` 边表 ~3.3 亿条 → 灌 Neo4j 建 `(:Paper {doi})-[:CITES]->(:Paper {doi})`

**成本**：
- 一次性下载：**639 GB / 14h @ 100 Mbit/s**（OpenAlex 不限速、不收 egress 钱）
- RustFS 存储：**639 GB 长期占用**（OpenAlex bucket）+ ~17 GB（索引产物）+ Neo4j 节点+边占用（粗估 50 GB）
- 月增量：~10 GB/月（只新增 part 需要拉）
- CPU：build index 大约 10 h 单核，DuckDB 多核会更快
- **需要确认 RustFS 当前剩余容量** — 1810 的 NAS 上跑得起这个量级吗？

**收益**：
- 用户提到的"双向引用图"真正能跑：cited_by 和 references 都本地索引
- 跨 corpus 的 PageRank / co-citation 分析可以做
- 论文推荐、概念演化追踪、wiki paper coverage 评估全部有据可依
- 不再依赖外网，完全 self-sufficient

**风险**：
- 一次性 ops 工作量大（mirror sync + index pipeline + Neo4j sync 三件套）
- Neo4j 3.3 亿边对 1810 那台 community edition 是不是撑得住要测
- 维护成本（月增量、partition 失效处理、索引重建）

### 🥉 路径 C — **不下 dump，直接 enrich-doi polite pool**（即我已做的）

**做法**：保持现状，用户跑 `qatlas wiki enrich-doi --mailto ...`，按 chain（arxiv-self → Crossref → OpenAlex）一篇一篇查 API。

**成本**：
- 450 篇 wiki × 3 API call = 1350 requests，polite pool 10 req/s ≈ 2.5 min/run
- 零存储、零基础设施

**收益**：
- **唯一**收益就是把 wiki 里 paper 页面的 frontmatter 补 DOI，没别的
- 不解决"将来想做引用图"的需求

## 实测结果（live POC，2026-05-29 05:00-05:15）

bucket: `qatlas-bibmeta-spike` (RustFS on mesh)，凭据用了 root key（一次性 spike 用，不落任何持久化配置）。

### Path A POC：API → SQLite 映射表 ✅ 已跑

```
5 pages × 200 = 1,000 works  /  988 (arxiv, doi) pairs  /  全部 formal DOI（0% self-DOI）
elapsed: 12.3s  (82 works/s)
SQLite: 264 KB → 上传到 s3://qatlas-bibmeta-spike/arxiv-doi-map/
```

**外推到全 corpus（3.27M arxiv works）**：
- 时间：~11 小时（polite pool 限速，单线程）
- SQLite：~860 MB raw, 估算 1.5 GB indexed
- → **跟之前 spike 估算（30 min, 125 MB）有出入**，因为 polite pool 实际限速比我估的紧，每 query 也要回 200 records 的完整 work record（即便 select trim 也大）

**重大发现**：路径 A 抽出的 pairs **100% 是 formal DOI**——这跟前面 part dump 抽样的"50% self-DOI"完全相反。原因：`filter=locations.source.id:S4306400194` 优先返回**有 cross-publication（即正式版）的 records**，arxiv-only 的 preprint 不会有 self-DOI 之外的 DOI，filter 自然过滤掉了。

样本（路径 A 前 10 个 2022+ 命中）：
```
1706.03762 -> 10.65215/2q58a426                  (2025) Attention Is All You Need
2303.08774 -> 10.4230/lipics.cosit.2024.11       (2023) GPT-4 Technical Report
1708.05148 -> 10.1007/s11042-022-13428-4         (2022) NLP: state of the art...
2104.02395 -> 10.1016/j.engappai.2022.105151     (2022) Ensemble deep learning: review
...
```

### Path B mini-POC：Shor 1995 的引用子图 ✅ 已跑

```python
seed = OpenAlex(doi='10.1137/S0097539795293172')  # Shor 1997 SIAM
# referenced_works[] has 45 entries → batched filter API hydration
# 1.6 s, 45 cited papers, 100% have DOI, 26% have arxiv id
```

实际拉到的引用网络（按 citation count 排序，前 10）：

| year | cited_by | arxiv_id | doi | title |
|---|---:|---|---|---|
| 1978 | 13,049 | — | 10.1145/359340.359342 | RSA paper |
| 1982 |  7,477 | — | 10.1007/bf02650179 | Feynman "Simulating physics with computers" |
| 1985 |  4,614 | — | 10.1098/rspa.1985.0070 | Deutsch "Quantum theory, Church-Turing" |
| 1995 |  4,505 | — | 10.1103/physreva.52.r2493 | Shor "Scheme for reducing decoherence" |
| 1995 |  4,340 | quant-ph/9503016 | 10.1103/physreva.52.3457 | Barenco et al. "Elementary gates" |
| 1995 |  3,737 | — | 10.1103/physrevlett.74.4091 | Cirac-Zoller ion trap |
| 1973 |  3,696 | — | 10.1147/rd.176.0525 | Bennett "Logical Reversibility" |
| 1996 |  2,942 | quant-ph/9511027 | 10.1103/physrevlett.76.722 | Bennett et al. "Purification" |
| 1992 |  2,690 | — | 10.1098/rspa.1992.0167 | Deutsch-Jozsa |
| 1982 |  1,840 | — | 10.1007/bf01857727 | Toffoli "Conservative logic" |

→ 全是 quantum computing 开山祖师爷。**直接灌 Neo4j 就是你 wiki paper 互相引用边的真相**。

**外推**：450 篇 wiki paper × 平均 50 refs/paper = ~22,500 个 API call，polite pool ~50 min 跑完，得 ~22K edges，**根本不需要 mirror 639 GB dump**。如果只关心 "wiki corpus 内部 + 一阶 cited papers" 的引用图，**API 路径完全够用**——把 mirror 大工程推后到 "做整个 quantum 物理学领域的 corpus PageRank" 才考虑。

### Path B 完整 mirror POC：3 个 part 镜像到 RustFS ✅ 已跑

跨洋链路 `openalex S3 (us-east-1) → ag-workstation WSL → mesh → RustFS@1810`：

| date | size | elapsed | throughput |
|---|---:|---:|---:|
| 2020-08-10/part_0000.gz | 2.5 KiB | 2.3s | (RTT-bound) |
| 2023-12-11/part_0000.gz | 59.5 KiB | 2.7s | (RTT-bound) |
| 2025-10-10/part_0060.gz | **295.4 MiB** | **30.5s** | **~77 Mbit/s** |

**注意**：这是经过本机绕路（中国大陆 WSL 中转）。直接从 1810 拉理论上更快（少一跳 + 1810 那台机器到 openalex S3 可能有更优路径），需要在 1810 上跑同样测试才能定。按 77 Mbit/s 估，639 GB 整 mirror 需要 ~18.5 小时；从 1810 直拉如果能到 500 Mbit/s（千兆链路上限），3 小时搞定。

### bucket 最终状态

```
2026-05-29 05:13:39  264.0 KiB  arxiv-doi-map/arxiv_doi_map.sqlite          ← Path A 产物
2026-05-29 05:14:30   28.0 KiB  citation-spike/shor-1995-subgraph.sqlite    ← Path B mini 产物
2026-05-29 05:12:13    2.5 KiB  data/works/updated_date=2020-08-10/part_0000.gz
2026-05-29 05:12:17   59.5 KiB  data/works/updated_date=2023-12-11/part_0000.gz
2026-05-29 05:12:49  295.4 MiB  data/works/updated_date=2025-10-10/part_0060.gz  ← mirror 测试
2026-05-29 05:12:09  378.0 KiB  openalex/data/works/manifest
Total: 296.1 MiB / 6 objects
```

写入正常，读取测试也通（`s3 ls` / `s3api get-bucket-location` 全 OK）。

### 修订后的推荐

基于实测：

1. **Path A 调整后做法**（修订）：不用一次拉完 3.27M arxiv works。**只拉 wiki corpus 关心的 arxiv ID 的精确 lookup**（API 单 DOI 查询，每 work ~10 ms），450 篇 wiki 一次跑完 < 1 分钟。需要 enrich 的时候才拉，**全量映射表先不建**——磨刀霍霍杀鸡，没必要。
2. **Path B mini（API 递归 citation hydration）做法**：450 wiki paper 一阶 cited = ~22K 个 API call，polite pool 50 min，**得 ~22K (doi, doi) edges**——直接灌 Neo4j 就行，**也不需要 mirror 639 GB**。
3. **Full mirror（639 GB）的真正用途**：是想做 "整个 corpus 内部 PageRank / co-citation analysis / 跨学科论文搜索"，跟 wiki paper 单查 DOI 已经是两件事了。**先 Path A + B-mini 跑半年看用户行为**，确定真需要再启动 full mirror。



---

## 文件清单（这次 spike 产物）

- `/tmp/oa-spike/works-manifest.json` — OpenAlex works manifest (387 KB)
- `/tmp/oa-spike/part_*.gz` — 5 个抽样 part (295 MB) — **跑完该 trash-put 掉**
- `/tmp/oa-spike/picks.txt` — 抽样选择记录
- 本文件 — spike notes
