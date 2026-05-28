# 关于

## 仓库概览

```text
QuantumAtlas/
├── atlas/                 核心代码
├── cmd/                   Go server 入口
├── internal/              Go server 内部包
├── web/                   React SPA 前端
├── examples/              可独立运行的 demo
├── scripts/               初始化与维护脚本
├── tests/                 测试套件
├── docs/                  补充文档
└── pyproject.toml         项目配置
```

> 状态目录（`wiki/`、`raw/`、`data/`、`pb_data/`）已**不在**仓库内——
> 它们默认落到 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/`
> 之下，或通过 `.env` 显式覆盖到挂载盘 / `/var/lib/...`。详见
> [Migration: storage layout](migration-storage-layout.md)。

## 贡献

欢迎以下方向的贡献：

- 新增或完善 primitive、algorithm、paper 页面。
- 改进解析、提取、图谱同步和 API。
- 补充测试、修正文档、优化协作体验。

提交说明请使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`、`refactor:`、`test:`、`chore:`。版本发布由 Commitizen 统一维护，细节见 [Development](development.md)。

## 致谢

QuantumAtlas 最初的三层知识库设计受到 [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) 启发，并基于 Go、PocketBase、Neo4j、Pydantic、Qiskit 等开源生态继续演化。

## 许可证

[MIT License](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/LICENSE)

GitHub: <https://github.com/IAI-USTC-Quantum/QuantumAtlas>

<p align="center"><i>构建量子算法的活文档，让知识持续增值。</i></p>
