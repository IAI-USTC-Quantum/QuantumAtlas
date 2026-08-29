开发一个新的 App（主仓库接入指南）
==================================

本文以 `qatlas-search <https://github.com/Agony5757/qatlas-search>`_
（私有仓库）为例，介绍外围 app 如何接入 QuantumAtlas 主仓库，
以及从零开发一个新 app 的完整流程。背景与设计动机见 :doc:`plugins`。

接入方式总览
------------

外围 app **不**\ 作为 Python 包安装进主仓库，也不编译进 qatlasd 二进制；
app 是独立仓库、独立部署的 HTTP 微服务，它通过一份接口协议接入主仓库：

- 独立仓库：app 自带 ``pyproject.toml``、测试套件、Dockerfile
  与 README，app 的 README 中必须写明接口协议；
- HTTP 接口协议：app 对外提供 REST 接口和 ``GET /healthz`` 健康探针，
  所有请求的认证一律使用 ``Authorization: Bearer <service_token>``；
- 配置驱动：qatlasd 只需在 ``~/.qatlas/config.yaml`` 中填写
  ``{enabled, url, token, timeout}`` 四项配置；配置缺失或 app 发生故障时，
  qatlasd 的核心功能仍然正常启动；
- 内网部署：app 在 ``deploy/docker-compose.yml`` 中以 profile 隔离部署，
  不暴露任何宿主机端口；qatlasd 是 app 的唯一合法调用方，
  由 qatlasd 统一负责认证、计量与配额；
- 插件状态登记：qatlasd 启动后探测 app 的 ``/healthz``，并在
  ``internal/plugin`` registry 中将 app 标记为 connected 或 disconnected，
  管理员可以在管理员页面查看该状态。

qatlas-search 在主仓库中的接入位置（供对照）：

- 客户端：``internal/search/remote.go``（``RemoteProvider``，
  实现了 ``search.Provider`` 接口）；
- 配置：``internal/config/config.go`` 的 ``search.remote`` 段，
  配置样例见 ``config.example.yaml`` 与 ``cmd/qatlasd/templates/config.yaml``；
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

开发者在组织下新建独立仓库（如 ``IAI-USTC-Quantum/qatlas-<name>``），
仓库的最小骨架如下：

.. code-block:: text

   qatlas-<name>/
   ├── pyproject.toml      # 包元数据 + 依赖；可选声明 qatlas.plugins entry-point
   ├── qatlas_<name>/
   │   ├── server.py       # FastAPI 服务入口
   │   └── config.py       # 自有 YAML 配置（注意：与 qatlasd 的 config.yaml 分离）
   ├── tests/
   ├── Dockerfile          # 构建镜像，如 docker build -t qatlas-<name>:local .
   └── README.md           # 必须写明接口协议全文

要点：

- app 需要有**独立的配置文件**（qatlasd 严格拒绝未知配置键，因此两个
  服务不能共享一个 config.yaml）；
- app 的配置中必须有 ``service_token`` 字段，它与 qatlasd 配置中的
  token 配对；
- app 的健康探针 ``GET /healthz`` 应返回 200，供 qatlasd 探测并登记
  插件状态。

第 2 步：定义接口协议
~~~~~~~~~~~~~~~~~~~~~

接口协议一旦发布，就是主仓库与 app 之间的稳定边界。开发者应将协议全文
写进 app 仓库的 README。下面以 qatlas-search 为例
（``internal/search/remote.go`` 的头部注释）：

.. code-block:: text

   POST {url}/v1/search   Authorization: Bearer {token}
   req:  {"query", "max_results", "sources": [str]|null, "agent": bool}
   resp: {"hits": [{title, authors[], year?, doi?, arxiv_id?, url?,
                    venue?, citations?, source, score}],
          "conclusion": str|null, "usage": {"llm_tokens": int},
          "errors": {backend: msg}}
   GET  {url}/healthz     → 200 {"status":"ok",...}

约定：

- app 应将业务接口放在 ``/v1/`` 前缀下；管理面接口（如
  ``/v1/admin/manifest``、``PUT /v1/admin/config``）同样使用 Bearer
  token 保护，qatlasd 的窄代理将这些接口的响应透传给管理员页面；
- 响应中的 ``usage`` 段供 qatlasd 的计量端点做配额统计与失败退款；
- 超时应按最慢路径取值（agentic 查询可能驱动 LLM，qatlasd 默认 60s）。

第 3 步：从主仓库接入
~~~~~~~~~~~~~~~~~~~~~

开发者可以参照 search 的接入方式修改主仓库，共五处改动：

1. **客户端/Provider**：开发者在 ``internal/`` 下新建（或复用）调用方，
   由调用方实现对应的插件接口。search 的例子是
   ``internal/search/remote.go`` 的 ``RemoteProvider``：普通查询实现
   ``Provider`` 接口参与 fan-out；provider 发生故障时，
   ``BaseProvider.RecordFailure`` 将故障降级为"该源无结果"，而不是让
   整个请求报错；需要完整响应的调用方（计量端点）另有一个返回真实
   error 的方法。
2. **配置段**：开发者在 ``internal/config/config.go`` 中增加字段，
   并在 ``config.example.yaml`` 与 ``cmd/qatlasd/templates/config.yaml``
   中同步补充带注释的样例，说明 url / token / timeout 的含义。
3. **装配**：``cmd/qatlasd/main.go`` 按配置开关构造 provider；
   未启用时 qatlasd 跳过该 provider，不会因为缺少插件而启动失败。
4. **健康探测**：开发者将 provider 注册进 ``internal/plugin`` registry；
   qatlasd 启动后探测 app 的 ``/healthz`` 并调用 ``SetProbeResult``
   记录结果，管理员页面据此显示连接状态。
5. **路由**\ （按需）：终端用户的流量经过 qatlasd 的代理端点
   （如 ``POST /api/search/agentic``），qatlasd 在代理层完成认证、
   每日限额计量与失败退款；管理面请求经过
   ``internal/routes/admin_plugins.go`` 式的窄代理，窄代理将 2xx
   响应体原样透传给管理员页面。

第 4 步：部署
~~~~~~~~~~~~~

开发者在 ``deploy/docker-compose.yml`` 中增加 app 服务，并遵循
qatlas-search 已有的约束（``tests/test_docker_compose.py`` 会强制检查
这些约束）：

.. code-block:: yaml

   qatlas-<name>:
     image: qatlas-<name>:local        # 在 app 仓库自行构建
     restart: unless-stopped
     profiles: ["<name>"]              # profile 隔离，默认 up 不带它
     networks:
       - shared-infra                  # 只接内网
     volumes:
       - ${HOME}/.qatlas/<name>.yaml:/etc/qatlas-<name>/config.yaml

- 服务**不声明** ``ports:``，即不暴露宿主机端口，app 的唯一合法调用方
  是 qatlasd；
- 服务只接入 ``shared-infra`` 外部网络（与 qatlasd 互通）；
- app 自有的配置文件以**读写**方式挂载（去掉 ``:ro``），这样管理端点
  （``PUT /v1/admin/config``）才能把修改持久化回文件；
- 注意：``tests/test_docker_compose.py`` 目前硬断言 compose 中只允许
  ``qatlasd`` 和 ``qatlas-search`` 两个服务。新增 app 时，开发者必须
  同步放宽该测试，并为新 app 补上同样的 profile-gated / no-ports 断言。

启动方式：

.. code-block:: bash

   docker compose --profile <name> up -d

第 5 步：本地联调
~~~~~~~~~~~~~~~~~

开发者不启动完整的 compose 环境也能联调：

1. 开发者在 app 仓库本地启动服务，例如
   ``uv run uvicorn qatlas_<name>.server:app --port 8600``；
2. 开发者在 qatlasd 的 ``~/.qatlas/config.yaml`` 中将 url 指向本地端口
   （如 ``http://localhost:8600``），并让 token 与 app 配置中的
   ``service_token`` 保持一致；
3. 开发者启动 qatlasd，然后检查管理员页面上的插件状态是否为
   connected；
4. 开发者通过 qatlasd 的代理端点发送请求，验证认证、计量与故障降级
   行为是否符合预期。

可选：从 CLI 接入
~~~~~~~~~~~~~~~~~

如果 app 还需要提供命令行入口，开发者可以通过 Python entry-point
group ``qatlas.plugins`` 声明该命令。用户安装 app 的包之后，
``qatlas <name>`` 命令会自动出现在 qatlas CLI 中；用户未安装该包时，
CLI 会给出安装提示（详见 :doc:`plugins`）。qatlas-search 就是采用这种
方式提供 ``qatlas search`` 命令的。

对照：qatlas-rag 已拆为独立仓库
-------------------------------

早期版本的主仓库曾以 monorepo 内嵌形态携带 ``rag/`` 目录
（``qatlas_rag`` 包，embed worker），该包的发行节奏与主仓库绑定。
目前 qatlas-rag 已拆分为独立仓库，它遵循本文介绍的接入方式：
它对外提供 HTTP 接口协议（``POST /v1/index`` 触发论文索引构建，
``GET /healthz`` 供健康探测），qatlasd 通过 ``rag.remote`` 配置段
（``{enabled, url, token, timeout}``）接入它，并在论文置 ready 时
向它推送索引构建任务。也就是说：**代码放在 monorepo 目录里还是独立
仓库里，只影响发行节奏；app 接入 qatlasd 的方式一律是 HTTP + Bearer
token**。开发者选择独立仓库的判据是：app 需要独立的发行节奏、独立的
依赖栈（如依赖 GPU 或 LLM），或者开发者希望核心仓库永远不携带这部分
代码。

app 之间的依赖
--------------

qatlas-search 依赖 qatlas-rag（语义检索是搜索 fan-out 的一个 backend），
这是生态里第一对 app→app 依赖。依赖机制保持轻量，qatlasd 不参与：

- 依赖在**调用方的配置**里声明。qatlas-search 的配置文件增加
  ``rag: {enabled, url, token, timeout}`` 段后，它即可按 qatlas-rag 的
  接口协议（``POST /v1/retrieve``）调用下游；被依赖方不需要知道自己
  有哪些调用方；
- 依赖检查复用既有机制：调用方的 ``GET /healthz`` 在 ``backends`` 里
  报告下游的就绪状态（qatlas-search 的 healthz 已是这个格式），
  qatlasd 的插件探测逻辑不需要任何改动；
- 故障隔离复用既有模式：下游故障只记入响应的 ``errors`` 段，调用方的
  其余功能不受影响；
- 调用链上的协议各自独立版本化、按 expand-contract 演进；升级顺序
  是先下游后上游（先 qatlas-rag，再 qatlas-search，最后 qatlasd），
  详见 :doc:`release`。

开发者设计新的 app→app 依赖时，应把被依赖方的协议全文写进被依赖方
仓库的 README，并在调用方仓库的 README 里记录该依赖（从哪个版本起
需要下游的哪个协议字段）。

检查清单
--------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 位置
     - 事项
   * - app 仓库
     - ``pyproject.toml`` / 测试 / Dockerfile / README 写明接口协议；
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
