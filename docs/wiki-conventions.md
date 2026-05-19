# Wiki Conventions

本文档定义 QuantumAtlas Wiki 的页面类型、frontmatter schema、命名规范、工作流以及 lint 规则。Wiki 与图谱、论文资产、应用代码的分层关系参见 [architecture.md](architecture.md)。

## 四层结构回顾

| 层级 | 默认目录 | 职责 | 变化方式 |
|------|---------|------|----------|
| 应用层 | `atlas/`、`tests/`、`scripts/`、`examples/` | 代码、模板、schema、自动化 | 仓库内审阅 |
| Wiki | `wiki/`（或 `WIKI_DIR`） | 人和 LLM 可编辑的知识页面 | 人审阅 / LLM 协作 |
| 论文资产 | `$RAW_DIR` | PDF、解析 Markdown、元数据 JSON、抽取出的图片 | 资产存储，不入 Git |
| 派生运行时 | Neo4j、`data/` | 图查询、任务/分享/摄入状态 | 从 Wiki 与资产派生 |

核心原则：**分类和关联是两回事**。Wiki 回答“这是什么”，Graph 回答“它和什么有关”。

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

```
Paper (arXiv ID)
    │
    ├─► Fetch PDF → RAW_DIR/pdf/{paper_key}.pdf
    │
    ├─► Parse PDF → RAW_DIR/markdown/{paper_key}.md
    │
    ├─► Store Metadata → RAW_DIR/json/{paper_key}.json
    │
    ├─► LLM Extraction → AlgorithmIR
    │
    ├─► Create Wiki Pages:
    │     ├─ wiki/sources/papers/paper-arxiv-{id}.md
    │     ├─ wiki/entities/algorithms/algo-{name}.md
    │     └─ wiki/entities/primitives/prim-{name}.md (if new)
    │
    ├─► Update wiki/index.md
    │
    ├─► Append to wiki/log.md
    │
    └─► Sync to Neo4j (async)
```

服务端会把这条流水线包装成异步 ingest 任务。每个阶段独立维护 status、message、时间戳和 progress 负载，客户端可以轮询 `GET /api/ingest/{task_id}`，不必阻塞在长时下载或解析任务上。

按阶段可恢复执行：

- 只拉取 PDF：`stop_after=fetch`。
- 解析后停下来交给客户端审阅：`stop_after=parse`。
- 用本地资产续跑：调用 `POST /api/ingest/{task_id}/continue`，或者把请求里 `fetch=false`；服务端会复用 `RAW_DIR/pdf`、`RAW_DIR/json`、`RAW_DIR/markdown` 中已有的产物。
- 基于已有 PDF 重解析：`stages=["parse"]`、`fetch=false`、`force_parse=true`。
- 强制重新下载/重新解析：`force_fetch=true` 或 `force_parse=true`。
- 使用 MinerU 解析：`parser=mineru`；服务端会把公网 raw URL 提交给 MinerU，OCR 关闭。
- 使用客户端审阅过的 LLM 抽取结果：调用 `POST /api/ingest/{task_id}/continue` 或 `POST /api/ingest/paper/reviewed-extraction`，附带审阅好的 `algorithm` 或 `algorithm_ir`；服务端将跳过自带的 LLM 抽取器，只写 Wiki 页面并按需同步 Neo4j。

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
│   ├── log.md                        # 活动日志
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

`RAW_DIR` 是 canonical 论文资产根。本地开发可以设 `RAW_DIR=raw`，生产环境建议指向应用 checkout 之外的路径。目录下必须直接包含 `pdf/`、`markdown/`、`json/`、`images/`。Share API 路径如 `papers/pdf/{key}.pdf` 是 share 相对的虚拟路径，会解析到 `RAW_DIR/pdf/` 下的实际文件。

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

## 附录：Crossref 元数据参考

写 paper source 页面时如果想补充期刊、卷期、DOI、被引数等较硬的元数据，可以查 [Crossref](https://www.crossref.org/) ——它是学术 DOI 的最大注册商（约 1.5 亿条），出版商发表论文时会把元数据（标题、作者、期刊、卷期、日期等）灌进去。相比 arXiv 的 `Journal reference`（作者自报），Crossref 的数据更可靠一档。

> 这只是 paper source 页面的可选元数据补充参考，目前 QuantumAtlas 代码里**没有**做 Crossref 集成；以下用法是给手工写 Wiki 的人提供的参考。

REST API 完全免费、无需 token：

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

**局限**：Crossref 只收交了登记费的出版商，arXiv preprint 不在库；MDPI 等开放获取期刊覆盖较好，部分会议论文集和较老期刊可能查不到。
