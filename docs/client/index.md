# Python 客户端 `qatlas`

`qatlas` 是 QuantumAtlas 的 Python 包：命令行客户端 + 库。它通过 HTTP 与 `qatlasd` 服务端通信，覆盖论文摄入、Wiki 写作、本地 MinerU 解析、电路设计与代码生成、凭据管理等工作流。

```bash
uv tool install quantum-atlas    # 或 pipx install / pip install
qatlas --help
```

读接口公开、无需 token；写操作（上传、贡献）需要一个 PAT，见 [管理凭据](manage-credentials.md)。客户端配置（server URL / token）写在平台原生 user-config 路径，详见 [入门指南](../getting-started.md)。

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

</div>

## Wiki 内容

<div class="grid cards" markdown>

-   :material-notebook-edit:{ .lg .middle } **[写 Wiki 页面](write-wiki-pages.md)**

    ---

    统一 concept 模型下的页面模板与最小可行示例（concept + category，source 仅作引用）。

-   :material-robot:{ .lg .middle } **[生成 Wiki 内容](generate-wiki-content.md)**

    ---

    多 subagent 读 paper → 提炼 concept → 去重合并的可复用流水线（prompt + `merge_concepts.py`）。

-   :material-shield-check:{ .lg .middle } **[Lint 与 校验](lint-wiki.md)**

    ---

    `qatlas wiki lint` 错误码 W001–W008 解释、典型修复模式。

-   :material-history:{ .lg .middle } **[贡献 Wiki 与 Raw](contribute-content.md)**

    ---

    Wiki 仓库 git 协作、server 端 fast-forward pull、ingest 鉴权与同步。

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

## 电路 / 代码

<div class="grid cards" markdown>

-   :material-vector-circle:{ .lg .middle } **[电路工具链](circuit-toolchain.md)**

    ---

    `designer → codegen → validator → estimator` 完整链路，含 IR 中间格式。

</div>

## CLI 参考

<div class="grid cards" markdown>

-   :material-console:{ .lg .middle } **[`qatlas` CLI](cli-qatlas.md)**

    ---

    全部子命令（`ingest` / `contrib` / `auth` / `wiki` / `designer` / `codegen` / `validator` / `estimator` / `config` …）+ 每个 flag 完整说明。

</div>
