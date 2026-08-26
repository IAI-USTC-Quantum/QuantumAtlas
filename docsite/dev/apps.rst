开发一个新的 App（主仓库对接指南）
==================================

本文以 `qatlas-search <https://github.com/Agony5757/qatlas-search>`_
（私有仓库）为例，说明一个外围 app 如何与 QuantumAtlas 主仓库对接，
以及从零开发一个新 app 的完整流程。背景与设计动机见 :doc:`plugins`。

对接模式总览
------------

外围 app **不**\ 作为 Python 包安装进主仓库，也不编译进 qatlasd 二进制；
它是独立仓库、独立部署的 HTTP 微服务，通过一份 wire 契约与主仓库对接：

- **独立仓库**——app 自带 ``pyproject.toml``、测试套件、Dockerfile
  与 README（README 中必须写明 wire 契约）；
- **HTTP wire 契约**——REST 接口 + ``GET /healthz`` 健康探针，
  认证一律 ``Authorization: Bearer <service_token>``；
- **配置驱动**——qatlasd 侧只在 ``~/.qatlas/config.yaml`` 中填写
  ``{enabled, url, token, timeout}``，缺失或故障不影响核心启动；
- **内网部署**——``deploy/docker-compose.yml`` 中以 profile 隔离，
  不暴露任何宿主机端口，qatlasd 是唯一合法调用方（统一认证、计量、配额）；
- **插件状态登记**——qatlasd 启动后探测 app 的 ``/healthz``，在
  ``internal/plugin`` registry 中标记 connected / disconnected，
  管理员页面可见。

qatlas-search 的实际接线（供对照）：

- 客户端：``internal/search/remote.go``（``RemoteProvider``，
  实现 ``search.Provider`` 接口）；
- 配置：``internal/config/config.go`` 的 ``search.remote`` 段，
  样例见 ``config.example.yaml`` 与 ``cmd/qatlasd/templates/config.yaml``；
- 装配与健康探测：``cmd/qatlasd/main.go``
  （``buildRemoteProvider`` / ``probeRemoteSearch``）；
- 路由：``internal/routes/search.go`` （fan-out）、
  ``internal/routes/search_agentic.go`` （计量端点）、
  ``internal/routes/admin_plugins.go`` （管理面窄代理）；
- 部署：``deploy/docker-compose.yml`` 的 ``qatlas-search`` 服务
  （``profiles: ["search"]``）。

开发流程
--------

第 1 步：创建 app 仓库
~~~~~~~~~~~~~~~~~~~~~~

在组织下新建独立仓库（如 ``IAI-USTC-Quantum/qatlas-<name>``），最小骨架：

.. code-block:: text

   qatlas-<name>/
   ├── pyproject.toml      # 包元数据 + 依赖；可选声明 qatlas.plugins entry-point
   ├── qatlas_<name>/
   │   ├── server.py       # FastAPI 服务入口
   │   └── config.py       # 自有 YAML 配置（注意：与 qatlasd 的 config.yaml 分离）
   ├── tests/
   ├── Dockerfile          # 构建镜像，如 docker build -t qatlas-<name>:local .
   └── README.md           # 必须包含 wire 契约全文

要点：

- 服务需有**独立的配置文件**（qatlasd 严格拒绝未知配置键，两个服务不能
  共享一个 config.yaml）；
- 配置中必须有 ``service_token`` 字段，与 qatlasd 侧的 token 配对；
- 健康探针 ``GET /healthz`` 返回 200，供 qatlasd 探测登记插件状态。

第 2 步：定义 wire 契约
~~~~~~~~~~~~~~~~~~~~~~~

契约一旦发布就是主仓库与 app 之间的稳定边界，写进 app 仓库的 README。
以 qatlas-search 为例（``internal/search/remote.go`` 头部注释）：

.. code-block:: text

   POST {url}/v1/search   Authorization: Bearer {token}
   req:  {"query", "max_results", "sources": [str]|null, "agent": bool}
   resp: {"hits": [{title, authors[], year?, doi?, arxiv_id?, url?,
                    venue?, citations?, source, score}],
          "conclusion": str|null, "usage": {"llm_tokens": int},
          "errors": {backend: msg}}
   GET  {url}/healthz     → 200 {"status":"ok",...}

约定：

- 业务接口放在 ``/v1/`` 前缀下；管理面接口（如 ``/v1/admin/manifest``、
  ``PUT /v1/admin/config``）同样以 Bearer token 保护，由 qatlasd 窄代理
  透传给管理员页面；
- 响应中的 ``usage`` 段供 qatlasd 计量端点做配额统计与失败退款；
- 超时按最慢路径取值（agentic 查询可能驱动 LLM，qatlasd 默认 60s）。

第 3 步：主仓库侧接入
~~~~~~~~~~~~~~~~~~~~~

参照 search 的接法，共五处改动：

1. **客户端/Provider**——在 ``internal/`` 下新建（或复用）调用方，
   实现对应的插件接口。search 的例子是 ``internal/search/remote.go``
   的 ``RemoteProvider``：普通查询实现 ``Provider`` 接口参与 fan-out，
   故障通过 ``BaseProvider.RecordFailure`` 降级为"该源无结果"而非整体
   报错；需要完整响应的调用方（计量端点）另暴露返回真实 error 的方法。
2. **配置段**——``internal/config/config.go`` 增加字段，
   ``config.example.yaml`` 与 ``cmd/qatlasd/templates/config.yaml``
   同步加注释样例（含 url / token / timeout 的说明）。
3. **装配**——``cmd/qatlasd/main.go`` 中按配置开关构造 provider，
   未启用时跳过，绝不因缺插件而启动失败。
4. **健康探测**——注册进 ``internal/plugin`` registry，启动后探测
   ``/healthz`` 并 ``SetProbeResult``，管理员页面据此显示连接状态。
5. **路由**\ （按需）——终端用户流量走 qatlasd 代理端点
   （如 ``POST /api/search/agentic``），在代理层完成认证、每日限额
   计量与失败退款；管理面请求走 ``internal/routes/admin_plugins.go``
   式的窄代理，原样透传 2xx 响应体。

第 4 步：部署接线
~~~~~~~~~~~~~~~~~

在 ``deploy/docker-compose.yml`` 增加 app 服务，遵循 qatlas-search
立下的约束（``tests/test_docker_compose.py`` 会强制检查）：

.. code-block:: yaml

   qatlas-<name>:
     image: qatlas-<name>:local        # 在 app 仓库自行构建
     restart: unless-stopped
     profiles: ["<name>"]              # profile 隔离，默认 up 不带它
     networks:
       - shared-infra                  # 只接内网
     volumes:
       - ${HOME}/.qatlas/<name>.yaml:/etc/qatlas-<name>/config.yaml

- **不声明** ``ports:`` ——不暴露宿主机端口，唯一合法调用方是 qatlasd；
- 只接 ``shared-infra`` 外部网络（与 qatlasd 互通）；
- app 自有配置文件以**读写**方式挂载（去掉 ``:ro``），使管理端点
  （``PUT /v1/admin/config``）能把修改持久化回文件；
- 注意 ``tests/test_docker_compose.py`` 目前硬断言 compose 中只允许
  ``qatlasd`` + ``qatlas-search`` 两个服务——新增 app 时必须同步放宽
  该测试，并为新 app 补上同样的 profile-gated / no-ports 断言。

启动方式：

.. code-block:: bash

   docker compose --profile <name> up -d

第 5 步：本地联调
~~~~~~~~~~~~~~~~~

不依赖完整 compose 也能联调：

1. 在 app 仓库本地起服务，如
   ``uv run uvicorn qatlas_<name>.server:app --port 8600``；
2. qatlasd 的 ``~/.qatlas/config.yaml`` 中将 url 指到本地端口
   （如 ``http://localhost:8600``），token 与 app 配置的
   ``service_token`` 保持一致；
3. 启动 qatlasd，检查管理员页面的插件状态是否为 connected；
4. 走 qatlasd 的代理端点发请求，验证认证、计量与故障降级行为。

可选：CLI 侧接入
~~~~~~~~~~~~~~~~

如果 app 还需要命令行入口，通过 Python entry-point group
``qatlas.plugins`` 声明，安装包后 ``qatlas <name>`` 命令自动出现，
未安装时 CLI 给出安装提示（详见 :doc:`plugins`）。qatlas-search 即采用
这种方式提供 ``qatlas search`` 命令。

对照：monorepo 内嵌形态
-----------------------

并非所有 app 都必须拆成独立仓库。``rag/``（``qatlas_rag``）是另一种形态：
Python 包仍随主仓库 ``quantum-atlas`` wheel 一起构建（见根
``pyproject.toml``），但对 qatlasd 的暴露方式同样是 HTTP 微服务
（embed worker，``uvicorn qatlas_rag.embed.worker:app --port 8801``，
见 ``rag/README.md``）。即：**对内是 monorepo 目录还是独立仓库，
只影响发行节奏；对 qatlasd 的对接方式一律是 HTTP + Bearer token**。
选择独立仓库的判据是：独立的发行节奏、独立的依赖栈（如 LLM 驱动）、
或希望核心永远不携带这部分代码。

检查清单
--------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 位置
     - 事项
   * - app 仓库
     - ``pyproject.toml`` / 测试 / Dockerfile / README 写明 wire 契约；
       自有配置文件含 ``service_token``；``GET /healthz`` 返回 200
   * - ``internal/``
     - 客户端实现对应插件接口；故障隔离降级
   * - ``internal/config/config.go``
     - 新增配置段；``config.example.yaml`` 与
       ``cmd/qatlasd/templates/config.yaml`` 同步样例
   * - ``cmd/qatlasd/main.go``
     - 按开关装配；注册 ``internal/plugin`` 健康探测
   * - ``internal/routes/``
     - 用户流量代理端点（认证/计量/退款）；管理面窄代理（按需）
   * - ``deploy/docker-compose.yml``
     - profile-gated、无 ``ports:``、只接 ``shared-infra``、
       配置文件读写挂载
   * - ``tests/test_docker_compose.py``
     - 放宽服务集合断言；为新 app 补 profile / no-ports 断言
   * - ``docsite/``
     - 本文档登记新 app；``guide/`` 补用户使用页
