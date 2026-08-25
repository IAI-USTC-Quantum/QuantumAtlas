插件化架构与路线图
==================

设计方向
--------

QuantumAtlas 主仓库正在收敛为 **核心的论文基础设施**——论文收集、注册表
数据库、基础检索，其余一切外源功能（semantic scholar、向量检索、wiki、
定理库等已经移除或计划移出的能力）都将以 **微服务 / 插件 / 独立模块**
的形态存在：

- 独立仓库、独立发行节奏、独立部署；
- 主仓库只定义稳定的 **插件接口** 与发现机制；
- 装了就有，没装就提示怎么装——核心永远不会因为缺插件而启动失败。

现有插件点
----------

.. list-table::
   :header-rows: 1
   :widths: 28 72

   * - 插件点
     - 机制
   * - 搜索 provider（服务端）
     - ``internal/search`` 的 Provider 接口；catalog / arxiv / openalex /
       qdrant 均为可插拔实现，配置启用，故障互相隔离
   * - 服务插件平台（服务端）
     - ``internal/hostapi`` 宿主 API + 配置驱动的启用开关
       （当前内置插件为空）
   * - CLI 命令（客户端）
     - ``qatlas.plugins`` Python entry-point group：第三方包安装后即可
       贡献 ``qatlas <name>`` 顶层命令或 ``qatlas contrib <name>`` 子命令

qatlas search 插件化（已落地）
------------------------------

search 已按插件式设计拆出主仓库，独立仓库
`qatlas-search <https://github.com/Agony5757/qatlas-search>`_（私有）承载：

- **独立仓库**——搜索引擎在独立仓库演进：多源 backend（arXiv / OpenAlex /
  Semantic Scholar / Crossref / catalog）、排序、FastAPI 服务、CLI 插件；
- **即装即用**——安装插件包后，qatlas CLI 通过 ``qatlas.plugins`` entry-point
  自动出现 ``qatlas search`` 命令，无需修改主仓库任何配置或代码；
- **未安装时友好提示**——执行 ``qatlas search`` 会提示该功能由独立插件提供
  并给出安装方法；
- **服务端对称接入**——qatlasd 通过通用 ``remote`` provider
  （``internal/search/remote.go``）按 wire 契约调用微服务：配置
  ``search.remote: {enabled, url, token, timeout}`` 即可插入任何符合契约的
  搜索微服务；provider 故障与其余源互相隔离；
- **微服务部署**——``deploy/docker-compose.yml`` 的 ``search`` profile
  （``docker compose --profile search up -d``），服务只接 ``shared-infra``
  内网、不暴露端口；终端用户的 agentic 请求一律经 qatlasd
  ``/api/search/agentic`` 代理，完成认证、每日限额计量与论文锚定。

wire 契约（``POST /v1/search``、``GET /healthz``）见 qatlas-search 仓库的
README。再往后，同样的模式会推广到其余外源能力：主仓库保持小而稳，
功能生态在外围生长。
