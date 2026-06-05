# qatlas_agentic_search — 学术式检索（精准匹配 + 引用量）

> 与向量 RAG（`qatlas_rag` + `internal/routes/rag.go`）**互补**的一条检索路径。
> 向量相似度对很多概念匹配不好；学术检索的特性是**词语精准匹配**和**引用量**。
> 这个包把多个学术数据源各封装成一个 *tool*，用一个 Pydantic-AI agent（或一个
> 不依赖 LLM 的确定性 direct 模式）把它们组装成 agentic-search 工作流。

## 为什么是这个设计

| | 向量 RAG (`qatlas_rag`) | 本包 (`qatlas_agentic_search`) |
|---|---|---|
| 排序信号 | bge-m3 向量相似度 | 词语/短语精准匹配 + log(引用量) + 时效 |
| 数据源 | 内部 Qdrant chunk | arXiv / OpenAlex / Semantic Scholar / Crossref / 内部 graph+wiki |
| 依赖 | GPU + Qdrant | 仅 `requests`（direct 模式）；agent 模式才需 LLM |

## 两种运行模式

- **direct（默认）**：并发跑选中的 backend → 合并去重 → `ranking` 排序。纯
  `requests`，**不需要 LLM key，也不需要装额外依赖**——最快最省。
- **agent（`--agent`）**：用 LLM 编排「启用的」工具（用户的 `--tools` 白名单仍然
  是硬边界，LLM 不能越权）。需要 `agentic-search` extra。

## 安装

```bash
cd QuantumAtlas
uv sync                          # direct 模式已经够用（requests 是核心依赖）
uv sync --extra agentic-search   # 仅 agent 模式需要（pydantic-ai-slim[openai]）
```

> agent 模式只内置 **OpenAI provider**。Pydantic-AI 的 Anthropic provider 需要
> `anthropic>=0.61`，与本仓库的 `anthropic<0.22` 核心 pin 冲突。OpenAI 兼容端点
> （OpenRouter / Groq / 本地 vLLM·Ollama，通过 `OPENAI_BASE_URL`）覆盖了
> 「平衡 cost」的需求；要原生 Anthropic 得先抬 `anthropic` 的 pin。

## 用法

```bash
# 列出所有工具及其是否就绪（✓/✗）
qatlas-search --list-tools

# direct 模式（默认）。引号内是精确短语
qatlas-search '"surface code" threshold'

# 只用部分工具 + JSON 输出
qatlas-search "VQE ansatz" --tools arxiv,openalex --json

# 看每个 backend 命中数 / 报错（partial failure 不会拖垮整次搜索）
qatlas-search "quantum error correction" -v

# agent 模式（需 agentic-search extra + LLM key）
qatlas-search "magic state distillation cost" --agent --model openai:gpt-4o
```

## 工具（每个数据源 = 一个 tool）

| 工具 | Key | 成本 | 引用量 | 说明 |
|---|---|---|---|---|
| `arxiv` | 无 | fast | — | arXiv Atom API，量子预印本的精准全文/标题匹配 |
| `openalex` | 无¹ | medium | ✓ | 主力，带 `cited_by_count`；倒排索引重建 abstract |
| `semantic_scholar` | 可选 | medium | ✓ | 相关性 + 引用量；无 key 易被 429 |
| `crossref` | 无¹ | medium | ✓ | 跨学科元数据；默认不在白名单，`--tools` 可开 |
| `internal` | 复用² | slow | ✓ | QuantumAtlas 内部：graph Cypher（`:PaperWork` 标题精准匹配 + 引用量）+ wiki 全文 + 可选本地 grep |

¹ 填 `QATLAS_SEARCH_OPENALEX_EMAIL` / `QATLAS_SEARCH_CROSSREF_EMAIL` 进 polite pool（更快更稳）。
² `internal` 复用 `qatlas` client 的 server URL + token（`qatlas auth login` 即可）；
  缺 token / Neo4j 未配置 / 无 wiki dir 时会优雅降级，不报错。

## 排序（核心的「修复」）

`ranking.rank` 的综合分（默认权重：词语 > 引用 > 时效）：

```
score = w_lex * lexical(query, title+abstract)   # 标题命中权重高于 abstract，短语逐字加权
      + w_cite * norm(log1p(citations))          # 集合内 min-max 归一；无引用量记为中性 0.5
      + w_recency * recency
      + 跨源一致性小加成                          # 被多个源命中本身就是相关信号
```

去重优先按 DOI > arXiv id，标题归一只作弱合并候选。

## 配置

server URL + token 复用 `qatlas` client 的 YAML 配置；只有搜索专属项走
`QATLAS_SEARCH_` 前缀（见仓库根 [.env.example](../.env.example)）：

| Var | 默认 | 说明 |
|---|---|---|
| `QATLAS_SEARCH_SEMANTIC_SCHOLAR_API_KEY` | (空) | S2 提速 |
| `QATLAS_SEARCH_OPENALEX_EMAIL` / `_CROSSREF_EMAIL` | (空) | polite pool |
| `QATLAS_SEARCH_SERVER_URL` / `_TOKEN` | (复用 qatlas) | 内部检索覆盖 |
| `QATLAS_SEARCH_WIKI_DIR` | (空) | 本地 wiki checkout，开启 grep |
| `QATLAS_SEARCH_LLM_MODEL` | `openai:gpt-4o-mini` | agent 模式模型 |
| `QATLAS_SEARCH_DEFAULT_TOOLS` | `arxiv,openalex,semantic_scholar,internal` | 默认白名单 |
| `QATLAS_SEARCH_WEIGHT_LEXICAL` / `_CITATION` / `_RECENCY` | 1.0 / 0.6 / 0.2 | 排序权重 |

## 测试

```bash
uv sync --extra dev --extra agentic-search
uv run pytest agentic_search/tests -m "not network and not e2e"
```

离线测试覆盖各 backend 的 `_parse`（固定 fixture）、排序、Cypher 注入安全、
agent 工具注册（用 pydantic-ai `TestModel`，不打网络）。带 `network` 标记的是
真打 arXiv/OpenAlex 的 live 用例，CI 默认跳过。
