# 关于

> 这一节讲项目本身的背景：它从哪儿来、为什么这么设计、相关研究、谁在维护。

## 项目快照

- **名字**：QuantumAtlas
- **客户端安装包**：[`qatlas-cli`](https://pypi.org/project/qatlas-cli/)，提供 `qatlas` 命令
- **旧 PyPI 包**：最终迁移版 [`quantum-atlas 0.21.0`](https://pypi.org/project/quantum-atlas/0.21.0/) 已发布，此后不再发版；它仅含元数据和迁移说明，不含 Python 模块、parser、命令入口或运行时依赖，不会自动安装新 CLI。请按[迁移指南](../getting-started.md#migrate-quantum-atlas)切换
- **服务端 GitHub**：<https://github.com/IAI-USTC-Quantum/QuantumAtlas>
- **客户端 GitHub**：<https://github.com/IAI-USTC-Quantum/qatlas-cli>（独立维护和发版）
- **生产入口**：<https://quantum-atlas.ai>
- **协议**：[Apache-2.0](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)
- **维护方**：[IAI-USTC-Quantum](https://github.com/IAI-USTC-Quantum)

## 仓库概览

```text
QuantumAtlas/
├── cmd/qatlasd/           Go server 入口（main + 各 cobra 子命令）
├── internal/              Go server 内部包（registry / search / ingest / auth / objstore / config 等）
├── web/                   React SPA 前端（Vite + TanStack Router）
├── scripts/               初始化与维护脚本（rustfs_bootstrap.sh 等）
├── tests/                 测试套件
├── docs/                  本文档（你正在看的）
├── docsite/               Sphinx 使用/开发文档
└── go.mod / go.sum        原生 Go 模块、依赖与工具版本声明
```

客户端代码不在此目录树中；主仓已移除根 Python/Pixi 聚合环境，使用原生 Go 模块开发。Python 只用于独立的文档构建和 CI 辅助脚本，不形成 Python 发行包。需要 `qatlas` 命令请使用独立的 `qatlas-cli`。主仓旧 `qatlas/` helpers 已退役，不再作为 parser 库保留。最终发行元数据、检查器、测试和 workflow 留在固定历史 tag [`quantum-atlas-v0.21.0`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0) 供审计，main 仅保留迁移说明及常规服务端流程。

!!! info "状态目录不在仓库里"
    `raw/`、`data/`、`pb_data/` 已**不在**仓库内——默认落到 `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/` 之下，可通过 YAML 的 `paths.*` 显式覆盖。详见 [存储布局迁移](../server/migration-storage-layout.md)。

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

