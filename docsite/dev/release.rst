发布与部署工作流（标准化方案）
==============================

QuantumAtlas 的线上形态由三类组件构成：**服务端 qatlasd**\ （本仓库）、
**命令行客户端 qatlas-cli**\ （独立仓库）、**app 微服务**\ （以
qatlas-search 为代表，每个 app 一个独立仓库，见 :doc:`apps`）。三者
独立编号、独立发布，它们靠两条协议协作：qatlasd 与 qatlas-cli 之间的
``(major, minor)`` 兼容协议（见 :doc:`versioning`），以及 qatlasd 与
app 微服务之间的 HTTP 接口协议。主仓还包含独立部署的下载 worker，
其当前构建分发边界与 v2 接入协议在下文单列。本文介绍从代码到线上的流程。

组件与发布通道
--------------

.. list-table::
   :header-rows: 1
   :widths: 18 24 26 32

   * - 组件
     - 版本唯一来源
     - 发布触发
     - 产物
   * - ``qatlasd``\ （本仓库）
     - 根目录 ``VERSION`` + ``v*`` tag（release.yml prep 强校验一致）
     - push tag ``v*.*.*``
     - ghcr 镜像 ``:{vX.Y.Z, X.Y.Z, latest}`` + 三平台二进制 +
       GitHub Release + PyPI ``quantum-atlas``
   * - ``qatlas-cli``
     - 其仓库 ``pyproject.toml``\ （commitizen）
     - ``cz bump`` 打 tag ``v*``
     - PyPI ``qatlas-cli``\ （trusted publishing）
   * - app 微服务（qatlas-search 等）
     - 其仓库根目录 ``VERSION`` + ``v*`` tag（release.yml prep 强校验一致）
     - push tag ``v*.*.*``
     - ghcr 镜像 ``ghcr.io/iai-ustc-quantum/<app>:{vX.Y.Z, X.Y.Z, latest}``
       + GitHub Release
   * - 文档站（qatlas-docs 镜像）
     - 构建时的 commit sha（docs.yml 写入镜像内的 ``VERSION`` 文件）
     - push main 且 ``docsite/**`` 变更（或手动触发 docs.yml）
     - ghcr 镜像 ``ghcr.io/iai-ustc-quantum/qatlas-docs:{<sha>, latest}``
       （只含静态文件，见下文"文档的独立更新"）
   * - ``qatlas-rag``
     - 其仓库根目录 ``VERSION`` + ``v*`` tag（release.yml prep 强校验一致）
     - push tag ``v*.*.*``
     - ghcr 镜像 ``ghcr.io/iai-ustc-quantum/qatlas-rag:{vX.Y.Z, X.Y.Z, latest}``
       + GitHub Release（GPU 镜像）
   * - ``downloaderworker``\（主仓 ``cmd/downloaderworker``）
     - 跟随包含该实现的主仓提交 / tag，与主服务验证 v2 协议兼容
     - 当前无独立 CI 发布通道；按指定 checkout 手动构建
     - ``Dockerfile.downloaderworker`` 镜像或 Go 二进制；不假定已有 ghcr tag
   * - ``downloaderproxy``\（主仓 ``cmd/downloaderproxy``，旧协议）
     - 跟随主仓 ``VERSION``\（同一 tag）
     - 主仓 push tag ``v*.*.*``\（无独立 release 产物）
     - **无 registry 产物**：部署方在 campus-egress 主机上用主仓
       ``Dockerfile.downloaderproxy`` 现场构建（见下文例外与
       :doc:`prod-deploy` 的 runbook）

.. rubric:: 下载执行端的当前分发边界

``downloaderworker`` 与旧 ``downloaderproxy`` 都复用主仓策略梯；当前
提供 Dockerfile，但未为新 worker 增加独立的镜像发布 workflow。因此它们
是下面 registry-only 原则的**已记录例外**，不能把“源码可构建”写成“某个
发布镜像已经可拉取”。使用明确的提交/tag 与自建镜像标记，在受控构建机
构建后分发，或在目标机构建；运行时网络权限由 worker 所在出口决定，
不由构建位置或镜像名称决定。

新 worker 只出站接入 ``/api/downloader/workers/v2``，升级保留节点身份和
待交付文件的数据卷；旧 proxy 仍是主服务主动调用 ``/v1/jobs`` 与
``/v1/files/{token}``。二者不是改名即可互换的协议。迁移顺序、管理员审批与
回退路由见 :doc:`prod-deploy`；加入 fleet/admission 迁移后，旧主服务二进制
会被 schema-version guard 拒绝，不能笼统承诺 checkout 旧 tag 即可回退。

标准化原则
----------

1. **每个组件有且只有一个版本唯一来源**；常规软件发布由 tag 触发，
   不以部署机现场 build 替代正式发布。文档的独立更新和下载执行端的
   当前构建例外见上表，后者必须记录明确的源码 revision；
2. **产物一律进入 registry**：Docker 镜像推送到 ghcr，Python 包发布到
   PyPI；部署机只执行 ``pull``，不执行 ``build``
   （下载执行端 downloaderworker / 旧 downloaderproxy 是当前例外，见上表）；
3. **部署机的版本一律显式 pin 在** ``deploy/.env`` 中（如
   ``QATLAS_VERSION=v0.22.1``），不使用 ``latest``，这样保证部署
   可回滚、可审计；
4. **接口协议的演进采用 expand-contract 方式**：先增加字段或端点
   （旧版本仍可工作），待所有部署升级后再删除旧形态。qatlasd 对 app
   故障做了隔离（provider 降级 + 插件探测标记 disconnected），因此
   **升级顺序默认先升级 app，后升级 qatlasd**；app 之间存在依赖时
   同样先下游后上游（例如先 qatlas-rag，再 qatlas-search，最后
   qatlasd）。只有"新 qatlasd 依赖 app 的新协议字段"这一种情况需要
   反过来，而开发者应在协议设计阶段就用 expand 步骤消除这种情况。

app 微服务的发布基建（以 qatlas-search 为例）
---------------------------------------------

qatlas-search 已按本方案接入标准化发布流程（首个 release：``v0.1.0``）：

1. 仓库根目录的 ``VERSION`` 文件是版本唯一来源，发布由 push tag
   ``v*.*.*`` 触发；
2. ``.github/workflows/release.yml`` 复用主仓的 prep + docker 模式：
   workflow 先校验 tag == ``VERSION``，然后构建多架构镜像并推送到
   ``ghcr.io/iai-ustc-quantum/qatlas-search:{vX.Y.Z, X.Y.Z, latest}``，
   最后创建 GitHub Release。注意：**私有仓库的 SLSA attestation 是
   付费的组织功能**，因此 attest 步骤已标记 ``continue-on-error``，
   仓库转为公开或组织升级后该步骤会自动生效；
3. 主仓 ``deploy/docker-compose.yml`` 的 qatlas-search 服务引用
   ``ghcr.io/iai-ustc-quantum/qatlas-search:${QATLAS_SEARCH_VERSION}``，
   版本在 ``deploy/.env`` 中显式 pin（样例见 ``.env.docker.example``）；
   ``tests/test_docker_compose.py`` 中的结构测试锁定了 ghcr 来源与
   插值约定；
4. app 仓库的 README 记录了接口协议的版本化说明（从哪个 app 版本
   开始提供哪个端点或字段）。

后续的新 app 仓库只需直接复制 qatlas-search 的 ``VERSION`` +
``release.yml`` 模式，即可接入同一套流程。

文档的独立更新
--------------

文档站（公开站 ``/doc`` 与开发站 ``/devdoc``）的更新与 qatlasd 的发布
相互独立，更新文档不需要重新部署服务。qatlasd 启动时检查文档目录
``~/.qatlas/docs``：目录中存在非空的 ``doc/`` 或 ``devdoc/`` 子目录时，
qatlasd 从磁盘的目录取材；目录缺失或为空时，qatlasd 回落到二进制
内嵌的文档副本（实现见 ``internal/routes/docs.go``，compose 模板把
该目录以只读方式挂载进容器）。

文档产物由 ``.github/workflows/docs.yml`` 独立构建：main 分支上
``docsite/`` 发生变更时，workflow 构建两个 sphinx 站点，并把它们打成
一个只含静态文件的镜像推送到
``ghcr.io/iai-ustc-quantum/qatlas-docs:{<sha>, latest}``（镜像内的
``VERSION`` 文件记录文档出自哪个 commit）。

部署机更新文档（qatlasd 全程运行）：

.. code-block:: bash

   ./deploy/update-docs.sh                  # 拉取 latest 并写入 ~/.qatlas/docs
   DOCS_REF=<sha> ./deploy/update-docs.sh   # pin 到指定 commit，可回滚

开发者验证尚未推送的文档改动时，在仓库 checkout 内运行
``./deploy/update-docs.sh --build-local``，脚本在一次性容器中构建
sphinx 站点并直接写入文档目录。

两点注意：

- 文档来源在 qatlasd 启动时确定一次。``~/.qatlas/docs`` 从空变为有内容
  （或反向清空）后，需要重启一次 qatlasd 才能切换来源；磁盘目录**内部**
  的内容更新则实时生效，这正是 ``update-docs.sh`` 的路径；
- 回滚到内嵌版本：清空 ``~/.qatlas/docs`` 下的对应子目录并重启 qatlasd
  即可。

线上升级标准流程
----------------

前置检查（每次必做）：

- 运维方应阅读目标版本的 CHANGELOG / Release notes，确认是否有
  BREAKING CHANGE 以及前置条件（如 v0.22.0 要求 PostgreSQL 先行就绪）；
- 运维方应备份数据目录（``data/``，即 ``pb_data`` 和 ``raw``）；
- 运维方应确认当前版本与目标版本之间的兼容协议允许直接跳到目标版本
  （qatlasd 跨 minor 升级时应逐段检查 changelog）。

升级（在部署机上执行）：

.. code-block:: bash

   # 1. pin 目标版本（qatlasd 与 app 微服务各自的版本变量）
   $EDITOR deploy/.env            # QATLAS_VERSION=v0.22.1
                                  # QATLAS_SEARCH_VERSION=v0.1.0

   # 2. 先 app 后 core
   docker compose --profile search pull qatlas-search
   docker compose --profile search up -d qatlas-search
   docker compose pull qatlasd
   docker compose up -d qatlasd

   # 3. 验证：版本、依赖探针、插件连接状态
   curl -s http://127.0.0.1:4200/api/health | jq .data.version
   curl -s http://127.0.0.1:4200/api/health | jq .data.checks
   # 管理员页面确认 search-remote 插件为 connected

   # 4. 冒烟：SPA 关键页面（dashboard、search）+ 一次 agentic 搜索

回滚时，运维方把 ``deploy/.env`` 中的版本 pin 改回旧值，再执行
``docker compose up -d <service>`` 即可。PocketBase 与 goose 迁移都会
在启动时幂等 apply，因此 patch 级回滚总是安全的；跨 minor 回滚前，
运维方必须先查看对应版本的 release notes 是否声明了 schema breaking。

客户端（qatlas-cli）升级：

.. code-block:: bash

   uv tool upgrade qatlas-cli     # 跟随 qatlasd 的 (major, minor) 线
   qatlas --version               # 确认落在同一 x.y 线的最新 patch

版本 bump 决策
--------------

- **patch**：兼容修复、文档更新、内部重构。qatlasd 与 qatlas-cli 的
  兼容性修复只发布 patch 版本（协议保证同一 ``x.y`` 线内可以自由漂移）；
- **minor**：新功能、接口协议的 expand（新增端点或可选字段）、依赖的
  大版本升级；
- **pre-1.0 阶段的 breaking change 也发布 minor 版本**，但开发者必须在
  CHANGELOG 的 ``BREAKING CHANGE`` 小节中写明前置条件与迁移步骤，
  部署方按上文的前置检查执行。

发版 checklist
--------------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - 步骤
     - 验收
   * - 本地 CI mirror
     - ``go vet`` / ``go test ./internal/... ./cmd/...`` /
       ``uv run pytest -m "not e2e and not network"`` /
       ``cd web && npm run build`` / ``swagger-check`` 全绿
       （release.yml 不跑测试，发版前自行保证）
   * - ``VERSION`` + CHANGELOG
     - ``## vX.Y.Z (YYYY-MM-DD)`` 段落；breaking 写明迁移步骤
   * - tag
     - annotated ``vX.Y.Z``，与 ``VERSION`` 完全一致
   * - release workflow
     - run 全绿；ghcr 出现 ``:vX.Y.Z`` / ``:X.Y.Z`` / ``:latest``
       三个 tag
   * - 部署
     - pin 版本 → pull → up -d → ``/api/health`` 版本与探针正确 →
       冒烟通过
   * - 客户端
     - ``uv tool upgrade qatlas-cli`` 后 ``(major, minor)`` 与 qatlasd
       对齐
