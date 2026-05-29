# API Explorer（OpenAPI / Swagger）

下面是 QuantumAtlas server 全部 `/api` endpoint 的交互式 OpenAPI 文档。它由
[swaggo](https://github.com/swaggo/swag) 从服务端代码注解
（`internal/routes/openapi.go`）**自动生成**，与 [REST API 总览](rest-api.md)
那张手维护的表同源互补：这里给出完整的参数 / 请求体 / 响应 schema，可展开浏览、
全文搜索。

!!! tip "想实际调用（Try it out）？"
    本页是 Read the Docs 上的**静态文档镜像**，与 API server 不同源，已禁用
    "Try it out"。要在线点测请用 server 自带的 Swagger UI（与 API 同源、host
    正确、点 **Authorize** 填 `Bearer <token>` 即可带鉴权调写口）：

    - RackNerd（默认线路）：<https://quantum-atlas.ai/swagger/>
    - Alibaba（国内线路，自签证书需信任）：`https://47.102.36.175/swagger/`

!!! info "spec 怎么保持同步"
    单一数据源是 `internal/apidocs/swagger.json`（`pixi run swagger` 生成、编译进
    二进制）。文档构建时由 `hooks/openapi_spec.py` 拷进本页渲染，不另存提交副本；
    CI（`pixi run swagger-check`）对注解与 spec 做 generate-and-diff 防漂移。
    详见 [REST API 总览 › 交互式 API 文档](rest-api.md#api-swagger-ui)。

<swagger-ui src="openapi.json"/>

<!-- RTD auto-build connectivity probe: 2026-05-29 -->

