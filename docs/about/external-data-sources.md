# 外部学术数据源

> QuantumAtlas 用来补全 / 索引论文 metadata、DOI 映射、引用关系的**外部上游数据源**。本文不是"我们用了什么库"（那部分见 [致谢](credits.md)），而是"我们查 / 同步 / 镜像哪些公开学术数据库"。

## 名称澄清（常见误解）

| 名词 | 全称 | 是什么 | 跟谁无关 |
|---|---|---|---|
| **OAI** | Open Archives Initiative | 一个 1999 年起的**协议标准**（OAI-PMH），全球学术档案库的统一元数据接口 | 跟 **OpenAI**（做 ChatGPT 的公司）**完全无关**，名字撞车 |
| **arxiv-metadata-oai-snapshot** | — | arxiv 通过 OAI-PMH 协议吐出来的全量 metadata 快照（Kaggle 每周更新） | — |
| **Crossref** | — | 学术 DOI **注册中心**，出版商交钱登记 DOI + metadata 的数据库 | 跟 OpenAlex 是部分重叠的两个数据源，不是被包含关系 |
| **OpenAlex** | — | 把 Crossref + arxiv + ORCID + MAG 等聚合的**开放索引**（OurResearch 非营利组织维护） | — |

## 三大上游对比

### 1. arxiv-metadata OAI snapshot

arxiv 自己通过 OAI-PMH 接口对外发布的全量论文 metadata 快照。

| 维度 | 值 |
|---|---|
| 数据源 | arxiv.org（Cornell 大学维护） |
| 体积 | ~4.9 GB JSONL（全量，所有学科） |
| 范围 | arxiv 上**所有**论文 ~301 万篇（2026-04 snapshot） |
| 更新 | Kaggle 每周一次 |
| 含 DOI | ✅ **73%**（作者投稿后回 arxiv 自己填的；team 抽样 5K 行 quant-ph 子集实测） |
| 含期刊 | ✅ `journal-ref` 字段（如 `Phys.Rev.D76:013009,2007`） |
| 含引用关系 | ❌ 完全没有 |
| 含作者机构 | ⚠️ 仅纯字符串，未解析 |
| 主键 | arxiv id (`0704.0001`) |

**怎么有 DOI 的**：作者投稿到 arxiv 时 DOI 字段一般为空（还没发表）。论文几个月／几年后正式发表，作者**主动回 arxiv 更新自己论文 metadata** 把 DOI 填进去——arxiv 不主动验证。所以 27% 缺失主要是 "作者懒得回填" 或 "论文从未投期刊"。

**在团队里已经有**：`/mnt/team/Papercrawl/arxiv-metadata/arxiv-metadata-oai-snapshot.json`（4.9 GB），团队 Papercrawl 项目下下来的。配套有 `quant-ph-download-manifest.json`（12 MB，从全量 filter 出 quant-ph 子集的下载清单）+ `stat.json`（统计结果，全 arxiv 共 3,015,145 篇 + 全部分类列表）。

### 2. Crossref

学术 DOI **注册中心**——出版商发表论文时来这里登记 DOI + metadata。出版商交钱，普通用户免费查。

| 维度 | 值 |
|---|---|
| 数据源 | Crossref（注册机构） |
| API | `https://api.crossref.org/works` |
| Bulk | ~200 GB JSON.gz，每年发一次 torrent |
| 范围 | ~1.83 亿条 DOI |
| 含 DOI | ✅ 所有论文都有（这就是它的存在意义） |
| 含正向引用（`reference[]`） | ✅ **完整**（含 unstructured 引文文本） |
| 含反向引用列表 | ⚠️ **只给数字** `is-referenced-by-count`；列表对普通用户**不开放**（要是 publisher 自己的 DOI prefix 才能用 Cited-by Service 拿） |
| 含 arxiv id | ❌ 没有（Crossref 不收 preprint） |
| 含摘要 | ❌ 没有 |
| 含作者机构 ROR | ⚠️ 部分有 |

**独家字段**（OpenAlex 不暴露）：撤稿声明（`update-to` / `update-policy`）、出版商 assertion（同行评议、数据可用性等正式声明）、archive（CLOCKSS / Portico 等数字保存信息）、TDM 链接、ISSN / member / prefix 等出版商内部 ID。

### 3. OpenAlex

把 Crossref + arxiv + ORCID + MAG（微软关停的学术图谱）等聚合起来的开放索引。

| 维度 | 值 |
|---|---|
| 数据源 | OurResearch（非营利） |
| API | `https://api.openalex.org/works`（polite pool: header 加 `mailto=...` 提速且优先级高） |
| **Bulk** | **~639 GB gz / 5–10 TB 解压**（2127 个 part），AWS S3 `s3://openalex/`，匿名可拉、**无 egress 费用** |
| 范围 | **4.92 亿** works（全学科，含 preprint + 期刊 + 会议 + 书 + thesis...） |
| 更新 | 每天增量，Bulk 每月 refresh |
| 主键 | OpenAlex W-id (`W3023478445`) |
| 含 DOI | ✅ Crossref 同步 + 自家算法补全 |
| 含正向引用（`referenced_works[]`） | ⚠️ **少于 Crossref**（同一篇 Shor 1997: OpenAlex 45 条 vs Crossref 51 条；OpenAlex 只算能解析到 W-id 的，丢失 unstructured 条目） |
| 含反向引用列表（`cited_by`） | ✅ **完整 W-id 列表**——**这是 OpenAlex 最大独家价值** |
| 含 arxiv id | ⚠️ **不在 `ids.arxiv`**，埋在 `locations[].landing_page_url`（形如 `http://arxiv.org/abs/1707.06347`，需正则提取） |
| 含摘要 | ✅ `abstract_inverted_index`（倒排索引格式，反映原文需重组） |
| 含作者机构 ROR | ✅ 已解析到 ROR ID |
| 含主题分类 | ✅ topics / concepts / keywords / mesh / SDG（自家 ML 跑出来的） |
| 含 fwci | ✅ field-weighted citation impact |
| **MAG 继承** | ✅ 微软 MAG 2021 年关停时把数据**捐给了 OpenAlex** — OpenAlex 这个项目就是为了接 MAG 留下的空白而成立的 |

## Crossref vs OpenAlex 不是"包含关系"，是"部分重叠"

⚠️ **常见误解**："OpenAlex 把 Crossref 全包了，用 OpenAlex 就够。" 不对。

同一篇 Shor 1997 SIAM（DOI `10.1137/S0097539795293172`）字段对照实测：

| 字段 | Crossref | OpenAlex |
|---|---:|---:|
| 总字段数 | 38 | 49 |
| `reference[]` 数 | **51** | **45**（丢 6 条） |
| `cited_by` **数字** | 5544 | 5803 |
| `cited_by` **列表** | ❌ | ✅ |
| 摘要 | ❌ | ✅ inverted index |
| 撤稿 / 勘误 | ✅ | ❌ |
| 出版商 assertion | ✅ | ❌ |
| 作者机构 ROR | ⚠️ 部分 | ✅ |
| 主题分类 | ❌ | ✅ topics / concepts / keywords / mesh |

**两个互补**：OpenAlex 缺 unstructured reference 文本 / 撤稿声明 / 部分引用边；Crossref 缺反向 cited_by 列表 / 摘要 / 机构解析 / 主题分类。

## 引用关系的两个方向

```
       reference (正向)              cited_by (反向)
  "这篇论文引用了谁"               "谁引用了这篇论文"

  Shor 1997                       Shor 1997
     │                               ▲
     ▼ 45-51 条                      │ 5803 篇
  Feynman 1982                    后人论文
  Deutsch 1985
  Bennett 1973
  ...
```

| 方向 | Crossref | OpenAlex |
|---|---|---|
| **正向**（A → A 引用谁） | ✅ **更全**（含 unstructured 引文文本） | ⚠️ 少几条（只算 W-id 能解析的） |
| **反向**（谁 → A，cited_by） | ⚠️ **只给数字**，列表锁着 | ✅ **完整列表** |

→ **要建完整引用图必须两个都查**：Crossref 拿正向 + OpenAlex 拿反向。这也是 `qatlas wiki enrich-doi` chain 把 Crossref 和 OpenAlex 都接进来的原因——**互补不是冗余**。

## arxiv ↔ 正式 DOI 各家覆盖率

抽样 + 实测得出（不是官方数字，仅供参考）：

| 来源 | quant-ph 论文 arxiv→DOI 覆盖率 | 性质 |
|---|---:|---|
| arxiv OAI snapshot 自报字段 | ~73% | 作者自报，可信但有缺失 |
| OpenAlex（含 Crossref 反向匹配 + MAG 历史） | 不确定，**抽样 3 篇 1 命中** | 用 publisher deposit + 算法补全，命中率看 publisher（Nature/PRL 高，小期刊/中文期刊低） |

> ⚠️ **重要**：OpenAlex 对 arxiv 没 DOI 的论文**经常 fallback 给 `10.48550/arxiv.XXXX` 这种 arxiv 自家 mint 的 self-DOI**——这不是正式期刊版 DOI，使用时要按 `doi` 前缀过滤掉。

## 在 QuantumAtlas 里的应用规划

### 现状（2026-05）

- ✅ **arxiv OAI snapshot 在团队 NAS**（`/mnt/team/Papercrawl/arxiv-metadata/`），4.9 GB，但**还没在 qatlas 代码里用**
- ✅ **CLI `qatlas wiki enrich-doi`** 已支持 `arxiv-self → Crossref → OpenAlex` chain（按 title 匹配作 fallback；详见 [Wiki Schema 文档 Crossref 附录](../reference/wiki-schema.md#附录crossref--openalex-元数据参考)）
- ✅ **RustFS 对象存储**（S3 兼容）已经在生产用于 paper-asset PDF/Markdown/JSON 三件套
- ❌ OpenAlex bulk dump 还没拉到 RustFS

### 短期（P1）：读本地 OAI snapshot

写脚本扫 4.9 GB → filter quant-ph 子集 → 提 `(arxiv_id, doi, journal-ref, title)` → 落 SQLite → 灌 PocketBase 一张 `arxiv_doi_map` collection。**73% 命中率、零外部 API 调用**。client 的 `enrich-doi` chain 加最高优先级 resolver `PocketBaseMapResolver`，命中即 high-confidence。

### 中期（P2）：OpenAlex bulk mirror 到 RustFS

把 OpenAlex works dump（639 GB gz）镜像到 RustFS 新 bucket（如 `qatlas-openalex/`，保留 `data/works/updated_date=*/part_*.gz` 原结构，便于增量）。配套：

- 一次性 sync ~14–18 小时（按当前测速）
- 月增量 ~10 GB
- 本地建索引（DuckDB / SQLite indexed）
- W-id → DOI 映射表
- arxiv URL → (arxiv_id, doi) 映射表（覆盖 OAI 缺的 27%）

**意义**：从此 client / server 查 DOI / 元数据全本地，不依赖外网 API、不限速、可以做大规模分析。**这是把 QuantumAtlas 从"工具"升级成"学术数据基础设施"的关键一步**。

### 长期（P3）：OpenAlex 引用关系灌 Neo4j

OpenAlex `referenced_works[]` × W-id → DOI join → ~3.3 亿条 `(:Paper)-[:CITES]->(:Paper)` 边，灌 Neo4j。开 PageRank / co-citation analysis / 论文推荐等大规模图算法。Crossref `reference[]` 补正向缺失（unstructured 引文）。

## 引用

如果在论文里引用了 OpenAlex 数据，他们的方法学论文是：

> Priem, J., Piwowar, H., & Orr, R. (2022). _OpenAlex: A fully-open index of scholarly works, authors, venues, institutions, and concepts_. arXiv [https://arxiv.org/abs/2205.01833](https://arxiv.org/abs/2205.01833)

## 调研原始记录

字段对照实测 + spike 探索（API 抓 1000 篇 arxiv work、Shor 1995 引用子图、RustFS mirror 通路验证、跨洋吞吐测速等）记录在 issue [IAI-USTC-Quantum/QuantumAtlas#16](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/16)（OpenAlex → Neo4j 引用图构建尝试）。
