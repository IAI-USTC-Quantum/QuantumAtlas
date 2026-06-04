# Subagent shared instructions — algo placeholder rewrite

> 这是 QuantumAtlas wiki Wave 3 (algo rewrite) **共享规范**。每个 subagent 单独负责
> 重写一个 `entities/algorithms/algo-*.md` 占位页。先 view 这份文件理解通用规则,
> 再按 call site 给的 target 算法做。

## 总目标

把 75 个 `entities/algorithms/algo-*.md` 从 "Quantum Algorithm Zoo backfill 占位"
重写成符合 wiki-schema §算法页面写作规范的**落地版**算法描述。

## 硬规则

1. **只改 target 文件,不动其它文件 / 不 commit / 不 git add**。
2. **保留 frontmatter 全部字段**(id, title, type, category, tags, related, external_links,
   source, source_section_id, speedup, neo4j_synced, neo4j_id 等)。**不要改 frontmatter**,
   除非:
   - `related:` 可以**追加** prim-* / concept-* 新链接(用户写入 prim 和 concept 词条
     时多数 algo 还未追加引用);
   - `status:` 保持 `draft`(不要改 published)。
3. **删除 `> **TODO**: 本页尚未按 wiki-conventions 重写...` 那一行**(或整段)。
4. **保留 `## 参考文献` 段** —— 它已经列了所有 paper-arxiv 链接,改写时**保留** 不动。
5. **可以删 / 替换 `## 描述` 段、`## 概述` 段、`## 整理状态` 段** —— 替换成下面的标准结构。
6. **若已有的 Zoo 翻译有用信息**(具体复杂度、关键论文要点),融合进新结构,不要全扔。
7. **status: draft**(保持 frontmatter);落不到具体细节就在顶部留 `> TODO: 缺什么`,
   不要复述论文凑字数。

## 重写后标准结构

```markdown
> (上面的 frontmatter 保持不变)

## 摘要

2-3 句中文,说算法解决什么问题、达到什么复杂度、相对经典的加速类型。

## 输入与输出

- **输入**:问题数据格式(经典输入 / 量子谕示器 / 量子态制备)。
- **输出**:经典答案 / 量子态 / 期望值 / 估计精度 / 成功概率。

## 寄存器划分

- 工作寄存器: ? 量子比特,做什么。
- 谕示 / 辅助寄存器: ? 量子比特,用途。
- (若有相位寄存器、记账寄存器,分别列出)

## 算法步骤

1. <第 1 步:具体酉算子 / 测量 / 经典处理>
2. <第 2 步:调用 `[[prim-<id>|原语名]]`,参数 ...>
3. ...

## 子例程 / 原语

- [[prim-<id>|原语名]] - 本算法怎么用它。
- ...

(若某 primitive 还没词条,先用 `[[prim-<拟用 id>]]` 留钩子,后续合并阶段处理)

## 复杂度

- **时间 / 门数**: O(?)
- **深度**: O(?)
- **量子比特**: ?
- **谕示器调用次数**(如适用): O(?)
- **加速类型**: <Superpolynomial | Polynomial(二次 / 三次等) | None>
- **经典最优对照**: O(?) (给出对比文献)

## 与其它算法的关系

- [[algo-...|相关算法]] - 同类 / 改进版 / 子问题 / 推广。
- [[concept-...|相关概念]] - 一句话点关系。

## 参考文献

(保留原文档中已有的 `[[paper-arxiv-...]]` 列表,**不要删** [pdf-only] / [md] 标记)
```

## 子例程库(可用的 prim-* id)

到 2026-06-03 为止已存在的 primitive 列表(必要时可链接):

- prim-qft, prim-qpe, prim-hamiltonian-simulation, prim-grover-oracle,
- prim-amplitude-estimation, prim-amplitude-amplification, prim-quantum-walk,
- prim-modular-exp, prim-block-encoding, prim-phase-kickback,
- prim-lcu, prim-qsvt, prim-qsp, prim-trotter-suzuki, prim-swap-test,
- prim-state-preparation, prim-jordan-wigner, prim-bravyi-kitaev,
- prim-classical-shadow, prim-oracle-construction, prim-quantum-teleportation,
- prim-solovay-kitaev, prim-variational-circuit, prim-superdense-coding,
- prim-quantum-counting, prim-fourier-sampling

## 概念库(可链接 concept-* id)

- concept-quantum-supremacy, concept-nisq, concept-bqp, concept-qma,
- concept-quantum-query-complexity, concept-quantum-communication-complexity,
- concept-quantum-error-correction, concept-stabilizer-formalism, concept-surface-code,
- concept-fault-tolerant-quantum-computation, concept-quantum-threshold-theorem,
- concept-decoherence, concept-quantum-noise-model, concept-pauli-group,
- concept-clifford-group, concept-universal-gate-set, concept-quantum-circuit-model,
- concept-no-cloning-theorem, concept-variational-quantum-algorithm, concept-vqe,
- concept-qaoa, concept-quantum-machine-learning, concept-quantum-chemistry,
- concept-fermionic-simulation, concept-quantum-simulation,
- concept-adiabatic-quantum-computation, concept-quantum-annealing,
- concept-measurement-based-quantum-computation, concept-cluster-state,
- concept-amplitude-encoding, concept-quantum-key-distribution, concept-bb84,
- concept-random-circuit-sampling, concept-boson-sampling,
- concept-tensor-network-states, concept-quantum-state-tomography,
- (旧的:concept-adiabatic-theorem, concept-hidden-subgroup-problem,
  concept-quantum-linear-systems, concept-span-program, concept-general-adversary-bound,
  concept-junta-learning-testing, concept-variational-principle)

## 源材料访问

- target 文件已有 `paper-arxiv-*.md` 引用列表;`sources/papers/paper-arxiv-XXX.md`
  里若有 `## Excerpt` 段就是 RAW 解析的论文首段,直接读;
- 若 Excerpt 缺失或 `raw_status: pdf-only`,可以拉 arxiv PDF (`https://arxiv.org/pdf/<id>.pdf`)
  作参考,但**不要**整段复制论文文字,要用中文抽象成算法描述。
- Wikipedia (中 / 英) 是融合骨架的主来源,优先看;关键概念命中 Wikipedia 主流条目就
  采用它的定义 / 记号 / 复杂度表述,用中文重写。

## 体量与质量

- 重写后总长度 ~3000-5500 字符(不含 `## 参考文献`)。
- 公式 KaTeX (`$...$` / `$$...$$`)。
- 链接密度:每个段落至少 1-2 个 `[[...]]`,但不要塞太多。

最后 subagent 报告时一句话总结改了哪个文件 / 大致体量 / 关键缺口(若有)。
