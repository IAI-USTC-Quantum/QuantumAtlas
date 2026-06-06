# qatlas_search — 学术式检索（精准匹配 + 引用量）

> 与向量 RAG（`qatlas_rag` + `internal/routes/rag.go`）**并列**的一条检索路径，
> 同样是「提供搜索能力」的基础设施。区别：`rag` 走 bge-m3 向量相似度，`search`
> 走**严格文本匹配**（标题 / 摘要 / 元数据精准词项）+ 引用量加权，无需 GPU，无需
> 向量库。
>
> 本模块是**纯基础工具，不含任何 AI / LLM 依赖**——它把多个学术数据源各封装成一个
> backend，供上层使用。LLM 编排 / agentic 那一层在**独立仓库** `agentic-search`，
> 它 import 本模块的 backend 当工具用。

## 为什么是这个设计

| | 向量 RAG (`qatlas_rag`) | 本模块 (`qatlas_search`) |
|---|---|---|
| 排序信号 | bge-m3 向量相似度 | 词语/短语精准匹配 + log(引用量) + 时效 |
| 数据源 | 内部 Qdrant chunk | arXiv / OpenAlex / Semantic Scholar / Crossref / 内部 graph+wiki |
| 依赖 | GPU + Qdrant | 仅 `requests`（核心依赖）；无 AI |

## 安装

无需任何 extra——`requests` 是 `quantum-atlas` 的核心依赖。

```bash
cd QuantumAtlas
uv sync
```

## 用法

```bash
# 列出所有 backend 及其是否就绪（✓/✗）
qatlas-search --list-tools

# 检索（引号内是精确短语）
qatlas-search '"surface code" threshold'

# 只用部分 backend + JSON 输出（上层程序友好）
qatlas-search "VQE ansatz" --tools arxiv,openalex --json

# 看每个 backend 命中数 / 报错（partial failure 不会拖垮整次搜索）
qatlas-search "quantum error correction" -v
```

`--json` 输出结构化结果，是 `agentic-search` 等上层调用本模块的推荐方式之一。

## Backend（每个数据源 = 一个 backend）

| Backend | Key | 成本 | 引用量 | 说明 |
|---|---|---|---|---|
| `arxiv` | 无 | fast | — | arXiv Atom API，量子预印本的精准全文/标题匹配 |
| `openalex` | 无¹ | medium | ✓ | 主力，带 `cited_by_count`；倒排索引重建 abstract |
| `semantic_scholar` | 可选 | medium | ✓ | 相关性 + 引用量；无 key 易被 429 |
| `crossref` | 无¹ | medium | ✓ | 跨学科元数据；默认不在白名单，`--tools` 可开 |
| `internal` | 复用² | slow | ✓ | QuantumAtlas 内部：graph Cypher（`:PaperWork` 标题精准匹配 + 引用量）+ wiki 全文 + 可选本地 grep |

¹ 填 `QATLAS_SEARCH_OPENALEX_EMAIL` / `QATLAS_SEARCH_CROSSREF_EMAIL` 进 polite pool（更快更稳）。
² `internal` 复用 `qatlas` client 的 server URL + token（`qatlas auth login` 即可）；
  缺 token / Neo4j 未配置 / 无 wiki dir 时会优雅降级，不报错。

## 作为库使用

```python
from qatlas_search.config import get_settings
from qatlas_search.backends import select_backends
from qatlas_search.engine import run_direct
from qatlas_search.models import SearchQuery

settings = get_settings()
backends = select_backends(["arxiv", "openalex"], settings, only_available=True)
outcome = run_direct(SearchQuery.parse('"surface code" threshold'), backends, settings)
for p in outcome.papers[:10]:
    print(p.score, p.citations, p.title)
```

## 排序（核心的「修复」）

`ranking.rank` 的综合分（默认权重：词语 > 引用 > 时效）：

```
score = w_lex * lexical(query, title+abstract)   # 标题命中权重高于 abstract，短语逐字加权
      + w_cite * norm(log1p(citations))          # 集合内 min-max 归一；无引用量记为中性 0.5
      + w_recency * recency
      + 跨源一致性小加成                          # 被多个源命中本身就是相关信号
```

去重用 DOI/arXiv id 的并查集（一个源只给 DOI、另一个只给 arXiv id 也能合并），
标题归一只作弱合并候选。

## 配置

server URL + token 复用 `qatlas` client 的 YAML 配置；只有搜索专属项走
`QATLAS_SEARCH_` 前缀（见仓库根 [.env.example](../.env.example)）：

| Var | 默认 | 说明 |
|---|---|---|
| `QATLAS_SEARCH_SEMANTIC_SCHOLAR_API_KEY` | (空) | S2 提速 |
| `QATLAS_SEARCH_OPENALEX_EMAIL` / `_CROSSREF_EMAIL` | (空) | polite pool |
| `QATLAS_SEARCH_SERVER_URL` / `_TOKEN` | (复用 qatlas) | 内部检索覆盖 |
| `QATLAS_SEARCH_WIKI_DIR` | (空) | 本地 wiki checkout，开启 grep |
| `QATLAS_SEARCH_DEFAULT_TOOLS` | `arxiv,openalex,semantic_scholar,internal` | 默认白名单 |
| `QATLAS_SEARCH_WEIGHT_LEXICAL` / `_CITATION` / `_RECENCY` | 1.0 / 0.6 / 0.2 | 排序权重 |

## 测试

```bash
uv sync --extra dev
uv run pytest search/tests -m "not network and not e2e"
```

离线测试覆盖各 backend 的 `_parse`（固定 fixture）、排序、Cypher 注入安全。带
`network` 标记的是真打 arXiv/OpenAlex 的 live 用例，CI 默认跳过。
