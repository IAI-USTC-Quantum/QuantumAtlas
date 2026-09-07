插件化架构与路线图
==================

设计方向
--------

QuantumAtlas 主仓库正在收敛为**核心的论文基础设施**：主仓库只负责论文
收集、注册表数据库和基础检索，其余外源功能（semantic scholar、向量
检索、wiki、定理库等已经移除或计划移出的能力）都将以**微服务 / 插件 /
独立模块**的形态存在：

- 每个外源功能都有独立的仓库、独立的发行节奏和独立的部署方式；
- 主仓库只定义稳定的**插件接口**与发现机制；
- 用户安装了插件就能使用对应功能，未安装时程序会提示安装方法；
  核心仓库永远不会因为缺少插件而启动失败。

现有插件点
----------

.. list-table::
   :header-rows: 1
   :widths: 28 72

   * - 插件点
     - 机制
   * - 搜索 provider（服务端）
     - ``internal/search`` 的 Provider 接口；catalog / arxiv / openalex
       均为可插拔实现，配置启用，故障互相隔离
   * - 检索微服务 qatlas-rag（服务端，独立仓库）
     - qatlasd 经 ``rag.remote`` 配置段接入：论文置 ready 时推送索引
       （``POST /v1/index``），启动后探测 ``/healthz`` 登记状态；
       qatlas-search 经自己的 ``rag`` 配置段调用 ``POST /v1/retrieve``，
       把语义检索作为 fan-out 的一个 backend
   * - 服务插件平台（服务端）
     - ``internal/plugin``\（manifest / registry / external JSON-RPC）
       + ``internal/hostapi`` 宿主 API + 配置驱动的启用开关。内置
       （kind=builtin）插件现有三个，manifest 在
       ``cmd/qatlasd/main.go`` 构造：

       - ``search-remote``\（capability ``search``）：qatlas-search
         微服务的进程内客户端，enabled 镜像
         ``search.remote.enabled``，后台 healthz 探测保持
         connected/disconnected 诚实；
       - ``rag-remote``\（capability ``rag``）：qatlas-rag 索引推送
         客户端，enabled 镜像 ``rag.remote.enabled``，同样受
         healthz 探测；
       - ``downloader``\（capability ``download``）：内部模块
         ``internal/downloader`` 经插件面暴露，SPA 的 Robust
         Downloader 页面按其 enabled 状态显隐（架构见
         :doc:`downloader`）。
   * - CLI 命令（客户端）
     - ``qatlas.plugins`` Python entry-point group：第三方包安装后即可
       贡献 ``qatlas <name>`` 顶层命令或 ``qatlas contrib <name>`` 子命令

qatlas search 插件化（已实施）
------------------------------

search 已按插件式设计从主仓库拆出，它的代码放在独立仓库
`qatlas-search <https://github.com/Agony5757/qatlas-search>`_ （私有）
中维护：

- **独立仓库**：搜索引擎在独立仓库中演进，包括多源 backend（arXiv /
  OpenAlex / Semantic Scholar / Crossref / catalog）、排序、FastAPI
  服务和 CLI 插件；
- **即装即用**：用户安装插件包后，``qatlas search`` 命令会通过
  ``qatlas.plugins`` entry-point 自动出现在 qatlas CLI 中，用户不需要
  修改主仓库的任何配置或代码；
- **未安装时友好提示**：用户执行 ``qatlas search`` 时，CLI 会提示该
  功能由独立插件提供，并给出安装方法；
- **服务端接入**：qatlasd 通过通用 ``remote`` provider
  （``internal/search/remote.go``）按接口协议调用微服务；运维方配置
  ``search.remote: {enabled, url, token, timeout}`` 后，qatlasd 即可
  接入任何符合协议的搜索微服务；provider 发生故障时不影响其余搜索源；
- **微服务部署**：微服务通过 ``deploy/docker-compose.yml`` 的
  ``search`` profile 部署（``docker compose --profile search up -d``），
  服务只接入 ``shared-infra`` 内网，不暴露端口；终端用户的 agentic
  请求一律经过 qatlasd 的 ``/api/search/agentic`` 代理，qatlasd 在
  代理层完成认证、每日限额计量与论文锚定。

接口协议（``POST /v1/search``、``GET /healthz``）的完整内容见
qatlas-search 仓库的 README。后续同样的模式会推广到其余外源能力：
主仓库保持小而稳，功能生态在外围生长。
