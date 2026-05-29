# 参考手册

> 当你已经知道想做什么，只想查"那个 flag 叫什么"/"那个字段是必填的吗"/"哪个 endpoint 返回什么 status code"。

## CLI

<div class="grid cards" markdown>

-   :material-console:{ .lg .middle } **[`qatlas` 客户端 CLI](cli-qatlas.md)**

    ---

    全部子命令（`ingest` / `upload` / `mineru` / `auth` / `wiki` / `designer` / `codegen` / `validator` / `estimator` / ...）+ 每个 flag 完整说明。

-   :material-server-network:{ .lg .middle } **[`qatlas-server` 服务端 CLI](cli-qatlas-server.md)**

    ---

    `serve` / `service install` / `pat mint` / `storage prune` / `superuser upsert` 等运维命令。

</div>

## API & 配置

<div class="grid cards" markdown>

-   :material-api:{ .lg .middle } **[REST API](rest-api.md)**

    ---

    `/api/papers/*` / `/api/wiki/*` / `/api/shares/*` / `/api/pat/*` / `/api/graph/*` / `/api/health` 全 endpoint 参考（method / path / auth / payload / response / status codes）。

-   :material-upload:{ .lg .middle } **[Upload API 详解](upload-api.md)**

    ---

    `POST /api/papers/{id}/upload-pdf` 完整流程：sha256 dedup、in-transit guard、`If-None-Match` 并发安全、覆盖语义。

-   :material-cog:{ .lg .middle } **[环境变量](env-vars.md)**

    ---

    全部 `QATLAS_*` + 第三方 SDK 标准名（`NEO4J_*` / `MINERU_*` 等），分 client / server / 共享。

-   :material-file-tree:{ .lg .middle } **[Wiki Schema](wiki-schema.md)**

    ---

    页面类型、frontmatter 字段、文件名约定、lint 错误码、`[[page-id]]` 链接语义。

-   :material-format-letter-matches:{ .lg .middle } **[arXiv ID 格式](arxiv-ids.md)**

    ---

    新旧两种 arXiv ID 格式、版本后缀规则、对象寻址映射。

</div>
