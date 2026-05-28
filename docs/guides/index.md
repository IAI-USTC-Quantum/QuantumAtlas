# 操作指南

按"我想做什么"组织的 how-to 指南。每篇都是一个具体任务，从前置条件到完整命令再到常见错误。

## 论文与资产

<div class="grid cards" markdown>

-   :material-cloud-download:{ .lg .middle } **[从 arXiv 摄入论文](ingest-papers.md)**

    ---

    `qatlas ingest` 让 server 自动抓 PDF + 元数据 + 可选解析。

-   :material-upload-network:{ .lg .middle } **[上传 PDF / Markdown](upload-assets.md)**

    ---

    `qatlas upload pdf/markdown` 手动推送资产，sha256 dedup、冲突处理、`--overwrite` 语义。

-   :material-file-document-edit:{ .lg .middle } **[用 MinerU 解析 PDF](parse-with-mineru.md)**

    ---

    `qatlas mineru` 本地跑 MinerU 并推回。单篇 / 队列模式 / 多人并发 claim。

</div>

## Wiki 内容

<div class="grid cards" markdown>

-   :material-notebook-edit:{ .lg .middle } **[写 Wiki 页面](write-wiki-pages.md)**

    ---

    Concepts / Entities / Sources / Comparisons 四类页面的模板与最小可行示例。

-   :material-shield-check:{ .lg .middle } **[Lint 与 校验](lint-wiki.md)**

    ---

    `qatlas wiki lint` 错误码 W001–W008 解释、典型修复模式。

-   :material-history:{ .lg .middle } **[贡献 Wiki 与 Raw](contribute-content.md)**

    ---

    Wiki 仓库 git 协作、server 端 fast-forward pull、ingest 鉴权与同步。

</div>

## 协作

<div class="grid cards" markdown>

-   :material-share-variant:{ .lg .middle } **[分享与下载](share-and-download.md)**

    ---

    `POST /api/shares/` 创建分享链接、TTL、单文件 vs 目录、presign 转发。

-   :material-key-chain:{ .lg .middle } **[管理凭据](manage-credentials.md)**

    ---

    PAT 创建 / 撤销 / 轮换、`qatlas auth login` 多 host 切换、shell vs CI 配置。

</div>

## 电路 / 代码

<div class="grid cards" markdown>

-   :material-vector-circle:{ .lg .middle } **[电路工具链](circuit-toolchain.md)**

    ---

    `designer → codegen → validator → estimator` 完整链路，含 IR 中间格式。

</div>
