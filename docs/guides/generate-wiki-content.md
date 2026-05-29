# Wiki content pipeline — paper → concept → merge

> 一条可复用的「多 subagent 读 paper → 提炼 concept → 去重合并」流水线，用来按
> Wikipedia 风格**追加** wiki 词条。本文档是流水线总览；具体 prompt / 工具在
> `scripts/wiki_pipeline/`。

## 设计目标

QuantumAtlas wiki 以 **concept 词条**为核心单位（见
[wiki-schema.md](../reference/wiki-schema.md) 顶部的统一 concept 模型说明）。source
论文只作为处理过的引用出现在词条内部，不再作为可浏览条目。要规模化扩充内容，就需要：

1. **并行**：尽可能多地分出 subagent，每个独立读**一篇** paper，互不阻塞。
2. **概念粒度**：产出以「概念」为单位，不是论文摘要。
3. **去重合并**：不同 subagent 难免产出相似概念，需要一个合并阶段按既定原则收敛。

## 三个阶段

```
                ┌─ subagent: paper A ─→ concept-a.md ─┐
 选 paper 子集 ─┼─ subagent: paper B ─→ concept-b.md ─┼─→ merge_concepts.py ─→ 候选对
 (zoo / sources)└─ subagent: paper C ─→ concept-c.md ─┘        │
                                                                ▼
                                                  按 merge_prompt.md 裁决
                                                  merge / crosslink / unrelated
                                                                │
                                                                ▼
                                            人工/LLM 编辑保留页 + 改链接 → commit
```

### 阶段 1 — paper → concept（并行 subagent）

- **输入**：已处理的 source markdown（`$WIKI_DIR/sources/papers/paper-arxiv-*.md`，
  优先选非 `pdf-only` 的，正文有 Excerpt）。paper 子集从 quantum algorithm zoo 对应的
  source 里挑。
- **每个 subagent** 拿 [`concept_prompt.md`](../../scripts/wiki_pipeline/concept_prompt.md)
  填好占位符后的 prompt，独立产出**一个** `concepts/concept-<slug>.md`。
- **硬约束**：一个 subagent 只产一个概念；概念粒度；交叉链接优先复用已有 id；
  公式用 KaTeX；`status: draft`；只写 `concepts/` 不碰别的词条。

### 阶段 2 — 候选检测（`merge_concepts.py`）

- 扫描 `concepts/`（可选 `--include-entities`），对两两概念按
  **标题 token Jaccard (0.5) + 标签 Jaccard (0.2) + 摘要 difflib (0.3)** 算相似度，
  超过阈值的输出为候选对。**只报告、不改文件**。
- `--json` 输出给阶段 3 的裁决喂数据。stdlib-only，无 embedding 依赖。

```bash
uv run --no-project scripts/wiki_pipeline/merge_concepts.py --threshold 0.45
uv run --no-project scripts/wiki_pipeline/merge_concepts.py --json > /tmp/candidates.json
```

### 阶段 3 — 合并裁决（`merge_prompt.md`）

按合并原则对每个候选对裁决（见
[`merge_prompt.md`](../../scripts/wiki_pipeline/merge_prompt.md)）：

- **相似/同一概念**（variational ≈ parameterized quantum circuit）→ **合并**：保留一个 id，
  整合两边正文，把指向被合并 id 的 `[[...]]` 改指保留 id。
- **A 是 B 的子概念/延伸**（hamiltonian simulation ⊂ quantum simulation）→ **不合并**，
  双向 `[[...]]` 交叉链接，正文点明上下位关系。
- **无关** → 不动。

裁决产出结构化 JSON，下游由人工/LLM 执行实际编辑。

## 运行一次 pilot

```bash
# 1. 选 3-5 篇有 MD 的 paper（示例）
ls $WIKI_DIR/sources/papers | grep -v pdf-only | head

# 2. 为每篇起一个 subagent，prompt 用 concept_prompt.md 模板填好
#    （并行；每个 subagent 写一个 concepts/concept-*.md）

# 3. 跑候选检测
uv run --no-project scripts/wiki_pipeline/merge_concepts.py --threshold 0.45

# 4. 对候选对按 merge_prompt.md 裁决并执行编辑

# 5. review 后再 commit 到 QuantumAtlas-Wiki 仓库（独立 repo；提交需显式确认）
```

## 注意

- wiki 是**独立仓库** `QuantumAtlas-Wiki`。pilot 产出先留在工作树供 review，
  确认无误后再提交。
- 阶段 1 产出一律 `status: draft`，由人工/合并阶段提升到 `review`/`published`。
- 相似度是启发式，不是判决；阶段 3 的语义裁决（尤其「子概念 vs 同概念」）仍需 LLM/人。
