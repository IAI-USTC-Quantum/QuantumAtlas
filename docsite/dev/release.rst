发布与部署工作流（标准化方案）
==============================

QuantumAtlas 的线上形态由三类组件构成：**服务端 qatlasd**（本仓库）、
**命令行客户端 qatlas-cli**（独立仓库）、**app 微服务**（以 qatlas-search
为代表，每个 app 一个独立仓库，见 :doc:`apps`）。三者独立版本、独立发布，
靠两条契约协作：qatlasd ↔ qatlas-cli 的 ``(major, minor)`` 兼容契约
（见 :doc:`versioning`），以及 qatlasd ↔ app 微服务的 HTTP wire 契约。
本文给出从代码到线上的标准化流程。

组件与发布通道
--------------

.. list-table::
   :header-rows: 1
   :widths: 18 24 26 32

   * - 组件
     - 版本唯一来源
     - 发布触发
     - 产物
   * - ``qatlasd``（本仓库）
     - 根目录 ``VERSION`` + ``v*`` tag（release.yml prep 强校验一致）
     - push tag ``v*.*.*``
     - ghcr 镜像 ``:{vX.Y.Z, X.Y.Z, latest}`` + 三平台二进制 +
       GitHub Release + PyPI ``quantum-atlas``
   * - ``qatlas-cli``
     - 其仓库 ``pyproject.toml``（commitizen）
     - ``cz bump`` 打 tag ``v*``
     - PyPI ``qatlas-cli``（trusted publishing）
   * - app 微服务（qatlas-search 等）
     - 现状：无版本来源（无 tag）
     - 现状：无 CI，镜像在部署机本地 build
     - 现状：仅本地镜像 ``qatlas-search:local``

标准化原则
----------

1. **每个组件有且只有一个版本唯一来源**，发布只由 tag 触发，不允许
   "部署机上现 build" 成为发布路径；
2. **产物一律进 registry**——Docker 镜像进 ghcr，Python 包进 PyPI；
   部署机只 ``pull``，不 ``build``；
3. **部署机的版本一律显式 pin** 在 ``deploy/.env``（如
   ``QATLAS_VERSION=v0.22.1``），不用 ``latest``，保证可回滚、可审计；
4. **wire 契约演进走 expand-contract**：先加字段/端点（旧版仍可工作），
   待所有部署升级后再删旧形态；qatlasd 对 app 故障隔离（provider 降级 +
   插件探测标 disconnected），因此**升级顺序默认先 app 后 qatlasd**
   ——只有"新 qatlasd 依赖 app 的新契约字段"这一种情况反过来，且这种
   情况应在契约设计阶段就用 expand 步骤消除。

补齐 qatlas-search 的发布基建
------------------------------

qatlas-search 目前无 tag、无 CI、镜像靠部署机本地 build，是不可回滚、
不可审计、无法多机部署的状态。补齐步骤（后续 app 仓以此为模板）：

1. 仓库根加 ``VERSION`` 文件，发布改为 push tag ``v*.*.*`` 触发；
2. 新增 ``.github/workflows/release.yml``——可整体复用主仓 release.yml
   的 prep + docker 两个 job：校验 tag == ``VERSION``，构建多架构镜像推到
   ``ghcr.io/iai-ustc-quantum/qatlas-search:{vX.Y.Z, X.Y.Z, latest}``；
3. 主仓 ``deploy/docker-compose.yml`` 的 qatlas-search 服务从
   ``image: qatlas-search:local`` 改为
   ``image: ghcr.io/iai-ustc-quantum/qatlas-search:${QATLAS_SEARCH_VERSION}``，
   并在 ``deploy/.env.docker.example`` 增加 ``QATLAS_SEARCH_VERSION``；
   同步放宽/更新 ``tests/test_docker_compose.py`` 的相关断言；
4. app 仓 README 记录 wire 契约的版本化说明（哪个 app 版本起提供哪个
   端点/字段）。

线上升级标准流程
----------------

前置检查（每次必做）：

- 阅读目标版本的 CHANGELOG / Release notes，确认有无 BREAKING CHANGE
  及前置条件（如 v0.22.0 要求 PostgreSQL 先行就绪）；
- 备份数据目录（``data/``，即 ``pb_data`` + ``raw``）；
- 确认当前版本与目标版本的兼容契约允许直跳（qatlasd 跨 minor 时逐段
  检查 changelog）。

升级（在部署机上执行）：

.. code-block:: bash

   # 1. pin 目标版本（qatlasd 与 app 微服务各自的版本变量）
   $EDITOR deploy/.env            # QATLAS_VERSION=v0.22.1
                                  # QATLAS_SEARCH_VERSION=v0.x.y（基建补齐后）

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

回滚：把 ``deploy/.env`` 的版本 pin 改回旧值，
``docker compose up -d <service>`` 即可——PocketBase 与 goose 迁移都是
启动时幂等 apply，patch 级回滚总是安全；跨 minor 回滚须先查对应版本的
release notes 是否声明 schema breaking。

客户端（qatlas-cli）升级：

.. code-block:: bash

   uv tool upgrade qatlas-cli     # 跟随 qatlasd 的 (major, minor) 线
   qatlas --version               # 确认落在同一 x.y 线的最新 patch

版本 bump 决策
--------------

- **patch**：兼容修复、文档、内部重构。qatlasd 与 qatlas-cli 的兼容性
  修复只走 patch（契约保证同 ``x.y`` 线内自由漂移）；
- **minor**：新功能、wire 契约 expand（新增端点/可选字段）、依赖大版本；
- **pre-1.0 的 breaking change 也走 minor**，但必须在 CHANGELOG 的
  ``BREAKING CHANGE`` 小节写明前置条件与迁移步骤，部署侧按上文前置
  检查执行。

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
