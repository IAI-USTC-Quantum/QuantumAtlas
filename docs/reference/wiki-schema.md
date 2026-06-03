# Wiki Conventions

本文档定义 QuantumAtlas Wiki 的页面类型、frontmatter schema、命名规范、工作流以及 lint 规则。Wiki 与图谱、论文资产、应用代码的分层关系参见 [architecture.md](../concepts/architecture.md)。

## 四层结构回顾

| 层级 | 默认目录 | 职责 | 变化方式 |
|------|---------|------|----------|
| 应用层 | `atlas/`、`tests/`、`scripts/`、`examples/` | 代码、模板、schema、自动化 | 仓库内审阅 |
| Wiki | `wiki/`（或 `WIKI_DIR`） | 人和 LLM 可编辑的知识页面 | 人审阅 / LLM 协作 |
| 论文资产 | `$RAW_DIR` | PDF、解析 Markdown、元数据 JSON、抽取出的图片 | 资产存储，不入 Git |
| 派生运行时 | Neo4j、`data/` | 图查询、任务/分享/摄入状态 | 从 Wiki 与资产派生 |

核心原则：**分类和关联是两回事**。Wiki 回答“这是什么”，Graph 回答“它和什么有关”。

## 统一 concept 模型（2026-05 迁移）

> **破坏性变更**：原先的 `entity` / `comparison` 页面类型已合并进 **`concept`**。
> 现在 wiki 以「concept 词条」为唯一可浏览单位（Wikipedia 风格），子类靠 `category`
> 区分（`algorithm` / `primitive` / `technique` / `problem` / `framework` / `comparison` / …）。
> `source`（论文）仍存在，但**不作为可浏览条目**——它只在词条的「参考文献」里被
> `[[paper-arxiv-*]]` 引用，列表/搜索 API 默认排除（`/api/pages`、`/api/search`），
> 详情仍可经引用点入。Graph 入口暂时在前端隐藏（路由与 `/api/graph/*` 保留）。
>
> 迁移脚本：`scripts/wiki_pipeline/`（内容生成）与 `scripts/migrate_wiki_types.py`
> （type 改写）。下面各模板里的 `type: entity` / `type: comparison` 为**历史记录**，
> 新页面一律 `type: concept` + 合适的 `category`；旧 type 常量在 Go 侧仍可解析以兼容
> 未迁移数据，但统计与 UI 统一按 concept 处理。批量追加内容见
> [generate-wiki-content.md](../guides/generate-wiki-content.md)。

## Wiki 页面类型

### Concepts (`wiki/concepts/`)

定义和解释量子计算概念。

```markdown
---
id: concept-{name}
title: Page Title
type: concept
tags: [tag1, tag2]
created_at: YYYY-MM-DD
updated_at: YYYY-MM-DD
status: draft | review | published
related: [concept-other]
---

## Summary

Brief explanation of the concept.

## Definition

Formal or mathematical definition.

## Examples

- Example 1
- Example 2

## See Also

- [[concept-related-1]]
- [[concept-related-2]]
```

### Entities (`wiki/entities/`)

记录算法、原语、人物、机构等实体。

**为什么分成 algorithm / primitive / person 三类**：

- **algorithm**：问题导向的"完整配方"。生命周期短，会因新论文衍生新变体（如多种 Shor 变体、HHL 改进版本）。
- **primitive**：可被多个算法复用的子例程（QFT、QPE、Amplitude Amplification、Block Encoding、QSVT…）。复杂度/定义稳定，是图谱里的复用节点。
- **person**：作者节点，让 paper → author → paper 的二跳查询成立。

之所以**必须把算法和原语分开**：算法 ↔ 原语是 N:M 多对多关系，分开后更新一个原语不会污染所有引用它的算法页，也避免在每个算法页里重复粘贴 QFT 这样的定义。这是 Quantum Algorithm Zoo / Qiskit / QuICT 等主流知识库的通行切法。

三层中只有 **primitive 被代码消费**（`atlas/knowledge_graph/primitives/*.yaml` 给 loader / designer 使用）。algorithm 和 person 只活在 Wiki + Neo4j，新增/修改算法和人物只改 Wiki；新增/修改原语必须同步更新 YAML 与 Wiki 两层。

子目录：

- `entities/algorithms/` — 算法实体
- `entities/primitives/` — 量子原语
- `entities/people/` — 研究者/作者

**算法实体模板：**

```markdown
---
id: algo-{name}
title: Algorithm Name
type: entity
category: algorithm
tags: [quantum-algorithm, category]
created_at: YYYY-MM-DD
status: published
related: [prim-qft, paper-arxiv-id]
neo4j_synced: true
---

## Overview

**Problem**: What problem does this algorithm solve?

**Complexity**:
- Time: O(?)
- Space: O(?)
- Gates: O(?)

## Primitives Used

- [[prim-qft]] - Used for phase estimation
- [[prim-qpe]] - Core component

## Algorithm Description

Step-by-step explanation...

## Source

- [[paper-arxiv-9508027]]

## Implementations

*Auto-generated from knowledge graph*
```

#### 算法页面写作规范（重要）

Wiki 不是论文综述。`entities/algorithms/algo-*.md` 的 `## Algorithm Description` 一节**不是**复述论文做了什么或讲故事，而是要做到：

1. **中文**写作。下游使用者（实现者、综合器、designer）以中文沟通，算法描述要直接可读。
2. **面向落地**。读完这一节，应当能据此写出 OriginIR / OpenQASM / cirq 等可运行电路，或至少能给电路综合器一份清晰的步骤说明。不要停留在"该算法实现了 Shor 因子分解"这种综述句。
3. **讲"怎么做"而不是"做了什么"**。必须包含：
   - **输入与输出**：寄存器划分、量子比特数、经典输入/输出格式；
   - **子例程拼装**：调用了哪些 `[[prim-*]]`，按什么顺序、传什么参数；
   - **关键步骤**：每一步对应的酉算子或测量，必要时写出矩阵 / 电路图 / 伪代码；
   - **复杂度与资源**：门数、深度、辅助比特、查询次数（区分 query 模型与门模型）。
4. **不重复 RAW**。论文原文的机器解析在 `$RAW_DIR/markdown/{key}.md`，wiki 不要粘贴整段原文；引用具体步骤时给 `[[paper-arxiv-{id}#section]]` 即可。
5. **如果某一步落不到具体酉算子或经典处理上，必须落到一个 `[[prim-*]]`**——既能说清"这一步靠某原语做"，也暴露出该原语暂缺的话需要新建。`[[prim-*]]` 是落地链路的最低粒度兜底。
6. **找不到落地细节时**保持 `status: draft` 并在页面顶部用 `> TODO:` 行标记缺口，不要用论文摘要凑数。

**反例**（不要这样写）：

> Shor 算法在 1994 年由 Peter Shor 提出，是首个能在多项式时间内分解整数的量子算法，对现代密码学造成重大影响……

**正例**：

> **输入**：待分解整数 N（n = ⌈log₂ N⌉ bit），随机选 a < N 与 N 互素。
> 
> **寄存器**：工作寄存器 2n 量子比特，目标寄存器 n 量子比特。
> 
> **步骤**：
> 1. 工作寄存器 H^⊗2n 制备均匀叠加；
> 2. 调用 [[prim-modular-exp]] 计算 |x⟩|0⟩ → |x⟩|aˣ mod N⟩；
> 3. 对工作寄存器作 [[prim-qft]]^†；
> 4. 测量工作寄存器，得 c/2^(2n)，连分数展开求阶 r；
> 5. 经典后处理：若 r 偶且 a^(r/2) ≠ −1 mod N，则 gcd(a^(r/2) ± 1, N) 给出非平凡因子。
> 
> **复杂度**：O(n³) 门，O(n) 调用 [[prim-modular-exp]]。

**原语实体模板：**

```markdown
---
id: prim-{name}
title: Primitive Name
type: entity
category: primitive
tags: [primitive, category]
created_at: YYYY-MM-DD
status: published
related: [algo-shors]
neo4j_synced: true
---

## Summary

Brief description of the primitive.

## Definition

Mathematical definition...

## Complexity

- **Gate Count**: O(n²)
- **Depth**: O(n)
- **Qubits**: n

## References

- [[paper-arxiv-9508027]]
- [[person-author-name]]

## Prerequisites

- [[prim-qft]] - Required foundation
```

### Sources (`wiki/sources/`)

源文献的 Wiki 化表示。

**论文 source 模板：**

```markdown
---
id: paper-arxiv-{id}
title: Paper Title
type: source
category: paper
tags: [arxiv, quant-ph]
created_at: YYYY-MM-DD
status: published
related: [algo-introduced]
doi: 10.xxxx/xxxxx                # bare DOI, no scheme/host
doi_source: arxiv                 # arxiv | crossref | openalex | semantic-scholar | manual | unresolved
doi_confidence: high              # high | medium | low
doi_resolved_at: YYYY-MM-DD       # when a resolver last tried
---

## Metadata

- **arXiv ID**: [{arxiv_id}](https://arxiv.org/abs/{arxiv_id})
- **Authors**: Author 1, Author 2
- **Published**: YYYY-MM-DD
- **DOI**: 10.xxxx/xxxxx (if available)

## Abstract

Paper abstract text...

## Key Contributions

1. Contribution 1
2. Contribution 2

## Algorithms Introduced

- [[algo-algorithm-name]]

## Key Insights

Important insights from the paper...

## See Also

- [[paper-cited-paper]]
```

**DOI 字段语义**（详见 `atlas/parser/doi/`）：

- `doi`：去掉 `https://doi.org/` 前缀的裸 DOI（如 `10.1103/PhysRevLett.103.150502`）。模板和 Neo4j 同步自己负责拼回 URL。
- `doi_source`：标记 DOI 来源。`unresolved` 表示"`qatlas wiki enrich-doi` 跑过但没匹配到"——用来跟"从没尝试过"区分，重跑 enrich 时不会浪费 API 配额。
- `doi_confidence`：`high` = 标题精确匹配 + 至少一个作者姓氏交集；`medium` = 仅标题匹配；`low` = 当前没用到。
- `doi_resolved_at`：上一次跑 resolver 的日期。

四个字段都是可选的；没有 DOI 也不影响页面渲染，只是 lint W009（INFO 级）会提醒一下。运行 `qatlas wiki enrich-doi --mailto you@example.org` 即可批量补全。

#### 什么样的论文该进 wiki

`wiki/sources/papers/` 是**人工 curation 节点**，不是 RAW 资产的索引。RAW 已经在 `$RAW_DIR/markdown/{key}.md` 提供论文全文的机器解析，wiki 不要重复这件事。

满足**任一**条件才该建 `paper-arxiv-{id}.md`：

- **被 ≥1 个 `algo-*.md` / `prim-*.md` 用 `[[paper-arxiv-*]]` 形式引用**，且该 wiki 链接确实有 curation 价值（例如指向论文里的具体小节、说明该实现选用了哪个版本）；
- 论文本身**引入一个原语或算法的新原始定义**，需要在 wiki 里挂一个稳定锚点供未来引用；
- 有人工写下的 **`## Key Contributions` 或 `## Key Insights`** 内容（哪怕一两句），即真正添加了 RAW 之外的信息。

下列情况**不要**为论文单独建 wiki 页：

- 仅仅因为论文进了 RAW 就为它建占位页；
- frontmatter 里 `raw_status: pdf-only` 且正文只有 `## Metadata / ## Curation note: PDF is present but markdown has not been parsed`——这种页面没有 curation 价值，应让算法页通过 frontmatter 字段 `raw_paper_key` 直接指向 RAW，不必走 wiki 节点。

> 历史上的 zoo backfill 批量生成了 400+ 个不满足条件的占位 source 页，构成数据治理债务；新增内容必须按本规则执行。

### Comparisons (`wiki/comparisons/`)

横向比较多个实体。

```markdown
---
id: comp-{name}
title: Comparison Title
type: comparison
tags: [comparison, category]
created_at: YYYY-MM-DD
status: published
related: [algo-1, algo-2]
---

## Overview

Brief description of what's being compared.

## Comparison Criteria

| Criterion | [[algo-1]] | [[algo-2]] |
|-----------|------------|------------|
| Complexity | O(n²) | O(n log n) |
| Qubits | n | 2n |
| Depth | O(n) | O(log n) |

## Analysis

Detailed comparison analysis...

## Recommendations

When to use each algorithm...
```

## 核心工作流

### 1. Ingest 工作流

服务端 ingest 是 **ff-only**：只 fetch + parse，不写 wiki，不写 Neo4j。LLM 抽取与 Wiki 页面生成由独立的人工/客户端流程完成（提交 PR），服务端不再产生 wiki 写入。

```
Paper (arXiv ID)
    │
    ├─► Fetch PDF → RAW_DIR/{paper_key}/pdf/{paper_key}.pdf
    │
    ├─► Parse PDF → RAW_DIR/{paper_key}/markdown/{paper_key}.md
    │
    └─► Store Metadata → RAW_DIR/{paper_key}/json/{paper_key}.json

(downstream, OUT of server scope)
    │
    ├─► (Manual / future CLI) LLM Extraction → AlgorithmIR
    │
    ├─► (Manual / future CLI) Create Wiki Pages:
    │     ├─ wiki/sources/papers/paper-arxiv-{id}.md
    │     ├─ wiki/entities/algorithms/algo-{name}.md
    │     └─ wiki/entities/primitives/prim-{name}.md (if new)
    │
    ├─► (Manual) Update wiki/index.md, append wiki/log.md
    │
    └─► (Manual) Wiki PR review → fast-forward merge → CI syncs Neo4j
```

服务端把 fetch + parse 包装成异步 ingest 任务。每个阶段独立维护 status、message、时间戳和 progress 负载，客户端可以轮询 `GET /api/ingest/{task_id}`，不必阻塞在长时下载或解析任务上。

按阶段可恢复执行：

- 只拉取 PDF：`stop_after=fetch`。
- 解析后停下来：`stop_after=parse`（这是最后一个阶段，等价于跑完）。
- 用本地资产续跑：调用 `POST /api/ingest/{task_id}/continue`；服务端会复用 `RAW_DIR/{paper_key}/pdf`、`RAW_DIR/{paper_key}/json`、`RAW_DIR/{paper_key}/markdown` 中已有的产物。
- 基于已有 PDF 重解析：`stages=["parse"]`、`force_parse=true`。
- 强制重新下载/重新解析：`force_fetch=true` 或 `force_parse=true`。
- 使用 MinerU 解析：`parser=mineru`；服务端会把公网 raw URL 提交给 MinerU，OCR 关闭。

### 2. Query 工作流

```
User Query
    │
    ├─► Search wiki/index.md for relevant pages
    │
    ├─► Read matching wiki pages
    │
    ├─► Optional: Traverse Neo4j for relationships
    │
    ├─► Synthesize answer (LLM)
    │
    └─► Optional: Save Q&A as new wiki page
```

### 3. Lint 工作流

```
Wiki Pages
    │
    ├─► Check frontmatter validity
    │     └─ Missing required fields
    │
    ├─► Detect orphan pages
    │     └─ Pages with no incoming links
    │
    ├─► Detect broken links
    │     └─ [[links]] to non-existent pages
    │
    ├─► Check for contradictions
    │     └─ Same algorithm with different complexity
    │
    ├─► Detect missing concepts
    │     └─ Linked but not defined
    │
    └─► Report issues
```

## Wiki ↔ Graph 同步规则

| Wiki 页面类型 | Neo4j 节点类型 | 同步方向 | 关系 |
|--------------|---------------|---------|------|
| `entity/algorithm` | Algorithm | Wiki → Neo4j | `[[prim-*]]` → DEPENDS_ON |
| `entity/primitive` | Primitive | Wiki → Neo4j | prerequisites 字段 |
| `entity/people` | Author | Wiki → Neo4j | `[[paper-*]]` → AUTHORED |
| `source/paper` | Paper | Wiki → Neo4j | `[[algo-*]]` → PUBLISHES |
| `comparison` | （不同步） | - | 只用于查询 |

**Wiki 是实体数据的 source of truth。**

- 实体属性（名称、描述、复杂度）来自 Wiki。
- Neo4j 存储并查询关系。
- 同步单向：Wiki → Neo4j。

## 页面命名规范

- **kebab-case**：`quantum-fourier-transform.md`
- 实体页加 **类型前缀**：
  - 算法：`algo-{name}.md`
  - 原语：`prim-{name}.md`
  - 人物：`person-{name}.md`
- 论文 source：`paper-arxiv-{id}.md`
- 比较页用描述性名字：`comp-{topic}.md`

## Wiki 链接格式

```
[[page-id]]                    # 基础链接
[[page-id|display text]]       # 带别名
[[#section]]                   # 同页 section
[[page-id#section]]            # 跨页 section
```

## 目录结构

```
QuantumAtlas/
├── $RAW_DIR/                         # 论文资产根（环境变量指定）
│   ├── pdf/                          # 原始 PDF
│   ├── markdown/                     # 解析后的 Markdown
│   ├── json/                         # 元数据 JSON
│   └── images/                       # 抽取的图片
│
├── wiki/                             # Wiki 页面
│   ├── index.md                      # 主索引
│   ├── log.md                        # 活动日志（gitignore，本地未跟踪）
│   ├── concepts/                     # 概念定义
│   ├── entities/
│   │   ├── algorithms/               # 算法实体
│   │   ├── primitives/               # 原语实体
│   │   └── people/                   # 人物实体
│   ├── sources/
│   │   └── papers/                   # 论文摘要
│   └── comparisons/                  # 横向比较
│
└── atlas/wiki/                       # Wiki 引擎模块
    ├── engine.py                     # 核心 WikiEngine
    ├── page.py                       # WikiPage 模型
    ├── templates.py                  # 页面模板
    ├── ingester.py                   # Ingest 流程
    ├── querier.py                    # Query 流程
    ├── linter.py                     # Lint 流程
    └── sync/                         # Neo4j 同步
        └── neo4j_sync.py
```

`RAW_DIR` 是 canonical 论文资产根。本地开发可以设 `RAW_DIR=raw`，生产环境建议指向应用 checkout 之外的路径。目录下必须直接包含 `pdf/`、`markdown/`、`json/`、`images/`。Object key 在 RustFS / LocalStore 后端均按 `<asset>/<arxiv-prefix>/<arxiv_id>v<n>.<ext>` 结构组织（由 `paperassets.AssetKey` 构造）。

## Frontmatter Schema

所有 Wiki 页面必须包含 YAML frontmatter：

```yaml
---
id: string                    # Required: Unique page identifier
title: string                 # Required: Page title
type: concept | entity | source | comparison  # Required
category: string              # Optional: Sub-type (algorithm, primitive, etc.)
tags: [string]                # Optional: Tags for classification
created_at: YYYY-MM-DD        # Required: Creation date
updated_at: YYYY-MM-DD        # Optional: Last update date
status: draft | review | published  # Required: Publication status
related: [string]             # Optional: Related page IDs
neo4j_synced: boolean         # Optional: Whether synced to Neo4j
neo4j_id: string              # Optional: Corresponding Neo4j node ID
---
```

## Lint 错误码

| 代码 | 等级 | 说明 |
|------|------|------|
| W001 | ERROR | 缺少必需 frontmatter 字段 |
| W002 | ERROR | frontmatter 字段值不合法 |
| W003 | INFO | 孤立页面（无入链） |
| W004 | WARNING | 断链（目标页不存在） |
| W005 | WARNING | 缺少概念定义 |
| W006 | ERROR | 页面 ID 重复 |
| W007 | INFO | 页面过期（30 天未更新） |
| W008 | WARNING | 实体页缺少 tags |

## 迁移说明

### 从 YAML 原语迁移到 Wiki

`atlas/knowledge_graph/primitives/` 下的旧 YAML 原语迁移到：

- `wiki/entities/primitives/prim-{name}.md`
- YAML 文件以只读方式保留作备份
- Wiki 页面成为 source of truth

### 从 `papers/` 迁移到论文资产 + Wiki

旧 `papers/` 目录的内容迁移到：

- `$RAW_DIR/` 保存 canonical 的 PDF、Markdown、JSON、图片资产
- `wiki/sources/papers/` 保存可追踪的 Wiki 化论文摘要

---

## 附录：Crossref / OpenAlex 元数据参考

写 paper source 页面时如果想补充期刊、卷期、DOI、被引数等较硬的元数据，可以查 [Crossref](https://www.crossref.org/) ——它是学术 DOI 的最大注册商（约 1.5 亿条），出版商发表论文时会把元数据（标题、作者、期刊、卷期、日期等）灌进去。相比 arXiv 的 `Journal reference`（作者自报），Crossref 的数据更可靠一档。

> **DOI 解析已集成到 CLI**：跑 `qatlas wiki enrich-doi --mailto you@example.org` 会按 `arxiv-self → Crossref → OpenAlex` 顺序匹配所有 paper 页面的 DOI，结果写回 frontmatter 的 `doi*` 四个字段。匹配策略是**严格的**：标题归一化后精确相等才算命中，再叠加作者姓氏交集做 high/medium 分级——不做 Levenshtein 等模糊匹配，宁可漏不可错。`doi_source=unresolved` 是显式的"试过但没匹配到"标记，下一次 enrich 时不会重复浪费 API 配额。详见 `atlas/parser/doi/`。

REST API 完全免费、无需 token，下面是手工查询的常用配方：

```bash
# 用 DOI 查单篇论文元数据
curl -s "https://api.crossref.org/works/10.1073/pnas.2026805118" | python3 -c "
import sys, json
d = json.load(sys.stdin)['message']
print('Title  :', d['title'][0])
print('Journal:', d['container-title'][0])
print('Year   :', d['issued']['date-parts'][0][0])
print('Authors:', ', '.join(a['given']+' '+a['family'] for a in d.get('author',[])))
print('Cited  :', d.get('is-referenced-by-count', '?'))
"

# 按标题/作者搜索
curl -s "https://api.crossref.org/works?query=quantum+error+mitigation&rows=5" \
  | python3 -c "import sys,json; [print(w['DOI'], w['title'][0][:80]) for w in json.load(sys.stdin)['message']['items']]"
```

常用字段：`container-title`（期刊名）、`volume/issue/article-number`、`issued.date-parts`、`publisher`、`reference`（参考文献列表）、`is-referenced-by-count`（被引数）。

**局限**：Crossref 只收交了登记费的出版商，arXiv preprint 不在库；MDPI 等开放获取期刊覆盖较好，部分会议论文集和较老期刊可能查不到。`enrich-doi` 命令会自动 fallback 到 [OpenAlex](https://openalex.org/)（覆盖面更广，包含 preprint、conference proceedings）兜底。
