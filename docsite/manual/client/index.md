# Python 客户端 `qatlas`

`qatlas` 是 QuantumAtlas 的命令行客户端，由 [IAI-USTC-Quantum/qatlas-cli](https://github.com/IAI-USTC-Quantum/qatlas-cli) 独立维护和发版，PyPI 包名为 **[`qatlas-cli`](https://pypi.org/project/qatlas-cli/)**。它通过 HTTP 与 `qatlasd` 服务端通信，覆盖论文摄入、资产上传、本地 MinerU 解析、论文 Markdown / 图片拉取、凭据管理等工作流。本节保留服务端集成指南；客户端开发与发行请到独立仓库。

旧包 `quantum-atlas 0.21.0` 是仅含元数据的最终迁移版：不含 `qatlas` 模块、parser 库或命令入口，没有运行时依赖，不会自动安装 `qatlas-cli`。安装主仓不会得到 CLI。已装旧包的用户请先看[迁移指南](../getting-started.md#从旧包-quantum-atlas-迁移)，保留 `~/.config/qatlas` 配置和凭据。

```bash
uv tool install qatlas-cli    # 或 pipx install / pip install
qatlas --help
```

论文数据不匿名可读——读和写都需要一个 PAT（`papers:read` / `papers:write` scope），见 [管理凭据](manage-credentials.md)。客户端配置（server URL / token）写在平台原生 user-config 路径，详见 [入门指南](../getting-started.md)。

每篇指南都是一个具体任务，从前置条件到完整命令再到常见错误。

## 论文与资产

- **[从 arXiv 摄入论文](ingest-papers.md)**

  `qatlas ingest` 让 server 自动抓 PDF + 元数据 + 可选解析。

- **[上传 PDF](upload-assets.md)**

  `qatlas contrib pdf` 手动推送 PDF，sha256 dedup、冲突处理、`--overwrite` 语义。

- **[用 MinerU 解析 PDF](parse-with-mineru.md)**

  `qatlas contrib mineru` 本地跑 MinerU 并推回。单篇 / 队列模式 / 多人并发 claim。

- **[贡献流程](contribute-content.md)**

  三条贡献路径、鉴权与审计、推荐协作节奏。

## 凭据与集成

- **[管理凭据](manage-credentials.md)**

  PAT 创建 / 撤销 / 轮换、`qatlas auth login` 多 host 切换、shell vs CI 配置。

- **[外部插件集成](external-plugins.md)**

  kind×transport 插件模型、外部 JSON-RPC（socket/stdio）握手、host capabilities、smoke test。

## CLI 参考

- **[`qatlas` CLI](cli-qatlas.md)**

  全部子命令（`ingest` / `contrib` / `paper` / `auth` / `config` …）+ 每个 flag 完整说明。
