# 架构决策记录（ADR）

本目录用 [Architecture Decision Record (ADR)](https://adr.github.io/) 体例，记录 QuantumAtlas
项目里**重要、难回退、且当下做了取舍**的架构决策——重点是 _why 我们选了这条而不是那条_，
跟 [概念](../concepts/index.md) 里写的 _what 系统现在长这样、怎么用_ 互为表里。

## 阅读规则

- **编号 = 发明顺序，不是优先级**。0001 是最早做的决定，靠后的编号是后做的。一旦写下永不重排，
  以保证跨 ADR 互引（如 _见 ADR 0004_）永远有效。
- **状态约定**：默认是 _accepted_；若被后来的 ADR 取代，原 ADR 留在原位、加一行 _Superseded by
  ADR-NNNN_，不删除。**已废弃的 ADR 也是档案的一部分**，记录的是"我们曾经怎么想"。
- **格式参考** [grill-with-docs / ADR-FORMAT](https://github.com/...) ——大多数 ADR 是 1-3
  段，覆盖背景 + 决策 + 理由；只有少数复杂场景需要 _Considered Options_ 或 _Consequences_ 段。

## 当前清单

见左侧目录树。
