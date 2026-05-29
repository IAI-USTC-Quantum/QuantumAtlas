# 写 Wiki 页面

Wiki 是 QuantumAtlas 的知识 source of truth。每个页面是一份带 YAML frontmatter 的 Markdown 文件，存放在 [QuantumAtlas-Wiki](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) 仓库。

## 前置条件

```bash
# 把 Wiki repo clone 到 QuantumAtlas 的兄弟目录（默认配置）
cd ~/projects   # 或你 QuantumAtlas 所在的父目录
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki.git

# client 端会自动读 ../QuantumAtlas-Wiki
qatlas wiki list
```

如果 Wiki 不在默认位置，设 `QATLAS_WIKI_DIR=/your/path/to/Wiki`。

## 四类页面

按用途分四类，互相通过 `[[page-id]]` 链接：

!!! note "2026-05 统一 concept 模型"
    Wiki 现以 **concept 词条**为唯一可浏览单位（Wikipedia 风格）：原 `entity` / `comparison`
    页面的 `type` 已统一改写为 `type: concept`，子类靠 `category` 区分
    （`algorithm` / `primitive` / `technique` / `comparison` / …）。`source`（论文）**不再作为
    可浏览条目**，只在词条「参考文献」里被 `[[paper-arxiv-*]]` 引用（列表 / 搜索 API 默认排除）。
    下表的 `type` 列为历史语义；**新页面一律 `type: concept` + 合适的 `category`**，目录沿用下表。
    批量追加内容见 [生成 wiki 内容](generate-wiki-content.md)。

| 类型 | 目录 | 文件名前缀 | 回答什么问题 | 同步到 Neo4j？|
|---|---|---|---|---|
| **Concept** | `wiki/concepts/` | `concept-*` | "这是什么概念" | ❌ |
| **Entity / Algorithm** | `wiki/entities/algorithms/` | `algo-*` | "这是哪个算法" | ✅ → `:Algorithm` |
| **Entity / Primitive** | `wiki/entities/primitives/` | `prim-*` | "这是哪个量子原语" | ✅ → `:Primitive` |
| **Entity / Person** | `wiki/entities/people/` | `person-*` | "这是谁" | ✅ → `:Person` |
| **Source / Paper** | `wiki/sources/papers/` | `paper-arxiv-*` | "这是哪篇论文" | ✅ → `:Paper` |
| **Comparison** | `wiki/comparisons/` | `comp-*` | "X 跟 Y 比较起来如何" | ❌ |

完整 schema 见 [Wiki schema 参考](../reference/wiki-schema.md)。

## 最快上手：用 CLI 生成模板

```bash
qatlas wiki create prim-grover \
    --title "Grover's Search Algorithm" \
    --type entity --category primitive \
    --tags search,oracle,amplification
```

生成 `wiki/entities/primitives/prim-grover.md`：

```markdown
---
id: prim-grover
title: Grover's Search Algorithm
type: entity
category: primitive
tags:
  - search
  - oracle
  - amplification
status: draft
created_at: 2026-05-29
---

# Grover's Search Algorithm

...在这里写内容...
```

## 四个最小可行示例

=== "Concept"

    ```markdown title="wiki/concepts/concept-quantum-phase-estimation.md"
    ---
    id: concept-quantum-phase-estimation
    title: Quantum Phase Estimation
    type: concept
    tags: [phase, eigenvalue, qft]
    status: published
    created_at: 2025-08-01
    related:
      - prim-qft
      - algo-shor
    ---

    # Quantum Phase Estimation

    Quantum Phase Estimation (QPE) 估计酉算子 $U$ 关于本征态 $|\psi\rangle$ 的相位 $\phi$，满足
    $U|\psi\rangle = e^{2\pi i \phi}|\psi\rangle$。

    它是 [[algo-shor]] 等算法的核心子例程，依赖 [[prim-qft]] 实现逆变换。
    ...
    ```

=== "Algorithm"

    ```markdown title="wiki/entities/algorithms/algo-shor.md"
    ---
    id: algo-shor
    title: Shor's Algorithm
    type: entity
    category: algorithm
    tags: [factoring, period-finding, qpe]
    status: published
    created_at: 2025-06-15
    related:
      - prim-qft
      - prim-modexp
      - concept-quantum-phase-estimation
      - paper-arxiv-quant-ph-9508027v1
    ---

    # Shor's Algorithm

    Shor 1994 算法在多项式时间内分解大整数。核心是把整数分解归约到 modular
    exponentiation 的周期发现，用 [[prim-qft]] 完成。

    ## 子例程
    - [[prim-modexp]]：受控模幂
    - [[prim-qft]]：量子傅里叶逆变换（用于读出周期）

    ## 实现
    `qatlas designer algo-shor` → IR；`qatlas codegen` → Qiskit；`qatlas estimator`
    → 资源估计。
    ```

=== "Paper"

    ```markdown title="wiki/sources/papers/paper-arxiv-quant-ph-9508027v1.md"
    ---
    id: paper-arxiv-quant-ph-9508027v1
    title: "Polynomial-Time Algorithms for Prime Factorization and Discrete Logarithms on a Quantum Computer"
    type: source
    category: paper
    tags: [factoring, foundational]
    status: published
    created_at: 1995-08-01
    doi: 10.1137/S0036144598347011
    doi_source: arxiv
    external_links:
      - {label: arXiv abstract, url: "https://arxiv.org/abs/quant-ph/9508027"}
      - {label: PDF, url: "/share/.../9508027v1.pdf"}
    related:
      - algo-shor
      - person-peter-shor
    ---

    # Polynomial-Time Algorithms for Prime Factorization ...

    ## 关键贡献
    1. 给出 polynomial-time 量子因子分解算法
    2. ...

    ## 与 [[algo-shor]] 的对应

    本论文是 [[algo-shor]] 的原始 ref。
    ```

=== "Comparison"

    ```markdown title="wiki/comparisons/comp-grover-vs-amplitude-amp.md"
    ---
    id: comp-grover-vs-amplitude-amp
    title: "Grover Search vs Amplitude Amplification"
    type: comparison
    tags: [search, amplification]
    status: published
    created_at: 2025-09-10
    related:
      - prim-grover
      - prim-amplitude-amplification
    ---

    # Grover vs Amplitude Amplification

    | 维度 | [[prim-grover]] | [[prim-amplitude-amplification]] |
    |---|---|---|
    | oracle 形式 | 标记 marked items | 一般 projector |
    | 已知"好"概率 $p$？| 否 | 是 |
    | 推广 | — | Grover 的推广 |
    ```

## 链接语法

正文里用 `[[page-id]]` 引用其他页面：

```markdown
本算法是 [[prim-qft]] 的应用，由 [[person-peter-shor]] 提出，
最初发表于 [[paper-arxiv-quant-ph-9508027v1]]。
```

frontmatter 里 `related: [page-id, page-id, ...]` 也产生关系。两种方式都会变成 Neo4j 边。

## frontmatter 必填字段

最少需要：`id` / `title` / `type`。其他根据 type 不同有最佳实践（lint 会提示）：

```yaml
---
id: prim-foo                    # 必填，唯一标识，跟文件名（去 .md）一致
title: Foo's Primitive          # 必填，显示标题
type: entity                    # 必填: concept / entity / source / comparison
category: primitive             # entity 必填: algorithm / primitive / person
tags: [search, oracle]          # 推荐：用于过滤 / Neo4j
status: draft                   # draft / review / published
created_at: 2025-06-15          # 创建时间
updated_at: 2026-05-29          # 可选
related: [prim-grover, algo-x]  # 关联页面 id 数组
external_links:                 # 外链
  - {label: arXiv, url: "..."}
neo4j_synced: false             # 不要手填，sync 自动管理
---
```

完整 schema 见 [Wiki schema 参考](../reference/wiki-schema.md)。

## 检查写得对不对

```bash
qatlas wiki lint
# 或单页
qatlas wiki lint --verbose | grep prim-foo
```

错误码 W001–W008 解释见 [Lint 与校验](lint-wiki.md)。

## 提交流程

Wiki 是独立 Git 仓库，**任何人都可以 clone / commit / PR**：

```bash
cd ~/projects/QuantumAtlas-Wiki
git checkout -b add-grover
# 写你的页面
qatlas wiki lint                 # 本地预检
git add . && git commit -m "feat: add prim-grover"
git push origin add-grover
# 在 GitHub 上发 PR
```

合并到 main 后，**触发 server 侧 fast-forward pull**：

```bash
curl -X POST https://<server>/api/wiki/sync/pull
# 无需 auth — 这是 fast-forward only，没法搞破坏
```

不调这个接口的话，server 端 cache 也会 60s 自动检测一次 git HEAD 变化（拉一次）。

## 推回 Neo4j

知识图谱的同步是**服务端职责**。`POST /api/wiki/sync/pull` 触发 server 端
git fast-forward 拉取 Wiki 并刷新缓存；图谱的派生由 Go ``qatlas-server`` 基于
canonical Wiki 重建。Python 客户端不再直连 Neo4j，也没有客户端 sync 命令。

## 下一步

- 看完整 schema：[Wiki schema 参考](../reference/wiki-schema.md)
- 修 lint 错误：[Lint 与校验](lint-wiki.md)
- 想知道 Wiki 怎么变成图谱：[数据流](../concepts/data-flow.md#wiki-neo4j)
