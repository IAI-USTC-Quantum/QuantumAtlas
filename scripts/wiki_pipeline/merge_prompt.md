# Merge prompt — dedup & consolidate concept entries

> 流水线第二阶段。多个 subagent 各自产出 concept 后，用 `merge_concepts.py` 算出**候选对**
> （标题/标签/正文相似度高的两两概念），再把每个候选对交给 LLM（或人）按下面规则裁决。

## 合并原则

对每一对候选概念 `A` / `B`：

1. **概念相似或可视作同一概念** → **合并**。
   - 例：variational quantum circuit ≈ parameterized quantum circuit。
   - 动作：保留一个 id（更通用/更常用的那个），把两边正文**整合**进去（取并集、去重、
     统一公式与符号），另一个 id 转为 redirect/别名（在 `related` 与正文里指过去），
     所有指向被合并 id 的 `[[...]]` 链接改指保留 id。
2. **A 是 B 的子概念 / 延伸**（不是同一概念）→ **不合并**，建**交叉链接**。
   - 例：hamiltonian simulation 是 quantum simulation 的子类。
   - 动作：两边各自保留，在 `related` 与正文「交叉链接」里互相 `[[...]]`，用一句话点明
     上下位/延伸关系（"X 是 Y 的特例 / 子问题 / 推广"）。
3. **无关** → 不动。

## 裁决输入（每个候选对）

```
A: <id>  title=<...>  category=<...>  tags=[...]
   摘要: <A 的 ## 摘要 段>
B: <id>  title=<...>  category=<...>  tags=[...]
   摘要: <B 的 ## 摘要 段>
similarity: <merge_concepts.py 给的分数>
```

## 裁决输出（结构化）

```json
{
  "a": "concept-...",
  "b": "concept-...",
  "decision": "merge | crosslink | unrelated",
  "keep": "concept-...",        // decision=merge 时保留的 id
  "relation": "same | a-subset-of-b | b-subset-of-a",
  "note": "一句话理由 / 整合要点"
}
```

下游：`merge` → 人工/LLM 编辑保留页正文 + 改链接 + 删/重定向另一页；
`crosslink` → 两页 `related` 互加 + 正文补一行 `[[...]]`。
