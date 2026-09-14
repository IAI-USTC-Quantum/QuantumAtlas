# API Explorer（OpenAPI / Swagger）

## 当前 Sphinx 文档的只读入口

当前可直接阅读的 API 静态参考是 [REST API 总览](rest-api.md)。本页不迁移旧版
`<swagger-ui>` 组件、主题 JavaScript / CSS 或 `hooks/openapi_spec.py` 构建 hook。
下方保留旧版镜像的说明，作为历史上下文；它不表示当前 Sphinx 构建仍生成该镜像。

需要查看完整参数和 schema，可使用
{download}`下载 OpenAPI spec <../../../internal/apidocs/swagger.json>`。
Sphinx 原生下载角色在构建时复制 `internal/apidocs/swagger.json` 这一唯一来源，
取代旧版 hook 的镜像复制方式，不另存维护副本。

<a href="/swagger/">打开当前实例的 Swagger UI（/swagger/）</a> 是当前文档站同源的
普通链接，不会自动执行 API 操作；纯静态托管站可能没有此服务路由。Swagger UI
本身不是只读界面：实际调用须由用户主动选择目标与操作，并通过服务端鉴权。
请在打开你部署的实例后核对环境、权限和操作影响，再决定是否发出真实请求。

## 历史说明：旧版静态 OpenAPI 镜像

下面是 QuantumAtlas server 全部 `/api` endpoint 的交互式 OpenAPI 文档。它由
[swaggo](https://github.com/swaggo/swag) 从服务端代码注解
（`internal/routes/openapi.go`）**自动生成**，与 [REST API 总览](rest-api.md)
那张手维护的表同源互补：这里给出完整的参数 / 请求体 / 响应 schema，可展开浏览、
全文搜索。

```{admonition} 想实际调用（Try it out）？
:class: tip

本页是 Read the Docs 上的**静态文档镜像**，与 API server 不同源，已禁用
"Try it out"。要在线点测请用 server 自带的 Swagger UI（与 API 同源、host
正确、点 **Authorize** 填 `Bearer <token>` 即可带鉴权调写口）—— 浏览器
打开你部署的 `https://<your-server>/swagger/` 即可。
```

```{admonition} spec 怎么保持同步
:class: note

单一数据源是 `internal/apidocs/swagger.json`（通过根 `go.mod` 声明的
`go tool swag init` 生成，编译进二进制）。文档构建时由 `hooks/openapi_spec.py`
拷进本页渲染，不另存提交副本；Go CI 对注解与 spec 做 generate-and-diff 防漂移。
详见 [REST API 总览 › 交互式 API 文档](rest-api.md#交互式-api-文档swagger-ui)。
```
