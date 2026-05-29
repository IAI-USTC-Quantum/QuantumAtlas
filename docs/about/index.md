# 关于

> 这一节讲项目本身的背景：它从哪儿来、为什么这么设计、相关研究、谁在维护。

## 项目快照

- **名字**：QuantumAtlas
- **PyPI**：[`quantum-atlas`](https://pypi.org/project/quantum-atlas/)
- **GitHub**：<https://github.com/IAI-USTC-Quantum/QuantumAtlas>
- **Wiki repo**：<https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki>
- **生产入口**：<https://quantum-atlas.ai>
- **协议**：[MIT](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
- **维护方**：[IAI-USTC-Quantum](https://github.com/IAI-USTC-Quantum)

## 仓库概览

```text
QuantumAtlas/
├── atlas/                 Python 客户端 + Wiki / 电路工具
│   ├── cli.py             qatlas CLI 分发器
│   ├── client/            HTTP client（ingest / upload / mineru / auth）
│   ├── wiki/              wiki 引擎 + lint + sync
│   ├── designer/          算法 → Quantum IR
│   ├── codegen/           IR → Qiskit / QPanda
│   ├── validator/         电路等价 / 仿真验证
│   ├── estimator/         资源估计
│   └── parser/            arXiv fetch + MinerU / PyMuPDF 解析
├── cmd/qatlas-server/     Go server 入口（main + 各 cobra 子命令）
├── internal/              Go server 内部包（路由 / auth / 存储 / config 等）
├── web/                   React SPA 前端（Vite + TanStack Router）
├── examples/              可独立运行的 demo
├── scripts/               初始化与维护脚本（rustfs_bootstrap.sh 等）
├── tests/                 测试套件
├── docs/                  本文档（你正在看的）
├── pyproject.toml         Python 项目配置
├── go.mod                 Go 项目
└── pixi.toml              跨语言开发环境（pixi）
```

!!! info "状态目录不在仓库里"
    `wiki/`、`raw/`、`data/`、`pb_data/` 已**不在**仓库内——它们默认落到 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/` 之下，或通过 `.env` 显式覆盖。详见 [存储布局迁移](../deployment/migration-storage-layout.md)。

## 这一节的内容

<div class="grid cards" markdown>

-   :material-lightbulb-on:{ .lg .middle } **[设计哲学](design-philosophy.md)**

    ---

    为什么分三层、为什么 Wiki 是 source of truth、为什么图谱不是。

-   :material-help-circle:{ .lg .middle } **[FAQ](faq.md)**

    ---

    最常见的 20 个问题。

-   :material-hand-heart:{ .lg .middle } **[致谢](credits.md)**

    ---

    灵感、生态依赖、维护者名单。

-   :material-chart-tree:{ .lg .middle } **[图谱可视化调研](graph-visualization-research.md)**

    ---

    Neo4j 图谱前端选型调研（仍待实现）。

</div>

