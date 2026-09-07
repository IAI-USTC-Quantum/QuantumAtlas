# Python 客户端 `qatlas`

`qatlas` 是 QuantumAtlas 的命令行客户端，独立发布为 PyPI 包 **`qatlas-cli`**（CLI 自 0.22.0 起从主仓拆出；主仓的 `quantum-atlas` 包现在只含 parser 库）。它通过 HTTP 与 `qatlasd` 服务端通信，覆盖论文摄入、资产上传、本地 MinerU 解析、论文 Markdown / 图片拉取、凭据管理等工作流。

```bash
uv tool install qatlas-cli    # 或 pipx install / pip install
qatlas --help
```

论文数据不匿名可读——读和写都需要一个 PAT（`papers:read` / `papers:write` scope），见 [管理凭据](manage-credentials.md)。客户端配置（server URL / token）写在平台原生 user-config 路径，详见 [入门指南](../getting-started.md)。

每篇指南都是一个具体任务，从前置条件到完整命令再到常见错误。

## 论文与资产

<div class="grid cards" markdown>

-   :material-cloud-download:{ .lg .middle } **[从 arXiv 摄入论文](ingest-papers.md)**

    ---

    `qatlas ingest` 让 server 自动抓 PDF + 元数据 + 可选解析。

-   :material-upload-network:{ .lg .middle } **[上传 PDF](upload-assets.md)**

    ---

    `qatlas contrib pdf` 手动推送 PDF，sha256 dedup、冲突处理、`--overwrite` 语义。

-   :material-file-document-edit:{ .lg .middle } **[用 MinerU 解析 PDF](parse-with-mineru.md)**

    ---

    `qatlas contrib mineru` 本地跑 MinerU 并推回。单篇 / 队列模式 / 多人并发 claim。

-   :material-history:{ .lg .middle } **[贡献流程](contribute-content.md)**

    ---

    三条贡献路径、鉴权与审计、推荐协作节奏。

</div>

## 凭据与集成

<div class="grid cards" markdown>

-   :material-key-chain:{ .lg .middle } **[管理凭据](manage-credentials.md)**

    ---

    PAT 创建 / 撤销 / 轮换、`qatlas auth login` 多 host 切换、shell vs CI 配置。

-   :material-connection:{ .lg .middle } **[外部插件集成](external-plugins.md)**

    ---

    kind×transport 插件模型、外部 JSON-RPC（socket/stdio）握手、host capabilities、smoke test。

</div>

## CLI 参考

<div class="grid cards" markdown>

-   :material-console:{ .lg .middle } **[`qatlas` CLI](cli-qatlas.md)**

    ---

    全部子命令（`ingest` / `contrib` / `paper` / `auth` / `config` …）+ 每个 flag 完整说明。

</div>
