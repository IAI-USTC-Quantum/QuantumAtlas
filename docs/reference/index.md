# 参考 / 数据格式

跨组件的稳定约定：环境变量、Wiki 页面 schema、arXiv ID 格式。需要查具体字段 / flag / 格式时来这里。

!!! tip "CLI 与 API 参考在组件分节下"
    - `qatlas` 客户端 CLI → [Python 客户端 › CLI 参考](../client/cli-qatlas.md)
    - `qatlasd` 服务端 CLI → [Go 服务端 › CLI 参考](../server/cli-qatlasd.md)
    - REST API / Upload API / API Explorer → [Go 服务端 › API](../server/rest-api.md)

<div class="grid cards" markdown>

-   :material-book-alphabet:{ .lg .middle } **[术语表](glossary.md)**

    ---

    懒加载 / 缓存词族（cache-aside、写穿、singleflight、LRO）、同步 vs 异步物化、领域术语、弃用词对照。

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
