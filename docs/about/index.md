# 关于

> 这一节讲项目本身的背景：它从哪儿来、为什么这么设计、相关研究、谁在维护。

## 项目快照

- **名字**：QuantumAtlas
- **PyPI**：[`quantum-atlas`](https://pypi.org/project/quantum-atlas/)
- **GitHub**：<https://github.com/IAI-USTC-Quantum/QuantumAtlas>
- **生产入口**：<https://quantum-atlas.ai>
- **协议**：[Apache-2.0](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
- **维护方**：[IAI-USTC-Quantum](https://github.com/IAI-USTC-Quantum)

## 仓库概览

```text
QuantumAtlas/
├── qatlas/                Python 客户端
│   ├── cli.py             qatlas CLI 分发器
│   ├── client/            HTTP client（ingest / upload / mineru / auth / paper）
│   └── parser/            arXiv fetch + MinerU 解析
├── cmd/qatlasd/           Go server 入口（main + 各 cobra 子命令）
├── internal/              Go server 内部包（registry / search / ingest / auth / objstore / config 等）
├── web/                   React SPA 前端（Vite + TanStack Router）
├── scripts/               初始化与维护脚本（rustfs_bootstrap.sh 等）
├── tests/                 测试套件
├── docs/                  本文档（你正在看的）
├── pyproject.toml         Python 项目配置
├── go.mod                 Go 项目
└── pixi.toml              跨语言开发环境（pixi）
```

!!! info "状态目录不在仓库里"
    `raw/`、`data/`、`pb_data/` 已**不在**仓库内——默认落到 `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/` 之下，可通过 `.env` 显式覆盖。详见 [存储布局迁移](../server/migration-storage-layout.md)。

## 这一节的内容

<div class="grid cards" markdown>

-   :material-lightbulb-on:{ .lg .middle } **[设计哲学](design-philosophy.md)**

    ---

    项目的设计取舍与演化史（含早期 wiki / 图谱时代的决策记录）。

-   :material-help-circle:{ .lg .middle } **[FAQ](faq.md)**

    ---

    最常见的 20 个问题。

-   :material-hand-heart:{ .lg .middle } **[致谢](credits.md)**

    ---

    灵感、生态依赖、维护者名单。

</div>

