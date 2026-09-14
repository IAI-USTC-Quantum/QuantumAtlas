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
       GitHub Release；不再发布旧 PyPI 包
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

旧 PyPI 包退役记录
----------------------

`quantum-atlas 0.21.0 最终迁移版
<https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/tag/quantum-atlas-v0.21.0>`_
已发布到 PyPI 和 GitHub。它只含元数据与迁移说明，不含 ``qatlas`` 模块、
命令入口或运行时依赖，也不会自动安装 ``qatlas-cli``。旧帮助库已退役，
不承诺在独立 CLI 中提供等价 Python API；CLI 用户应先卸载旧包，再安装
``qatlas-cli``，保留已有配置。

固定历史 tag
`quantum-atlas-v0.21.0 <https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0>`_
保留当时的发行元数据、一次性检查器、测试及 workflow 供审计，不能移动
或覆盖该 tag。main 已移除这些一次性工具和旧包发布入口，不再发布后续
``quantum-atlas`` 版本；根目录 uv 项目仅用于开发，不分发 Python 包。

常规 ``v*`` 服务端流程继续发布二进制、镜像和 GitHub Release，不包含
PyPI 产物。这次旧包退役没有发布服务端或容器，服务端 ``VERSION`` 及
GitHub Latest 保持为 ``0.34.0`` / ``v0.34.0``。

下载执行端的当前分发边界
----------------------------

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

服务端源码与预编译分发
------------------------

从采用此流程的下一个新版本开始，GoReleaser OSS **v2.18.1** 发布
``qatlasd_<version>_<os>_<arch>.tar.gz``（Linux amd64/arm64、Darwin arm64），
以及 ``qatlasd_<version>_web.zip`` 和默认 ``qatlasd_<version>_checksums.txt``。
不自定义 archive/checksum 的命名、flags 或 ldflags；通过
``tags: [embedui]`` 显式内嵌完整 UI，版本适配默认 ``main.version``。
不为旧裸二进制附件添加兼容层、不重发 ``v0.34.0``。

发布流程：tag/VERSION/SemVer/已公开 Release 保护检查 → 同 SHA 的
``go.yml``（Go test/vet、OpenAPI、前端与两文档站双干净构建一致性）→
上传唯一 UI 包 → GoReleaser 创建 draft、嵌入此包解出的同一树 →
归档校验与 attestation、三平台原生运行 ``--version``、Docker 发布并验证 →
公开 Release → 仅稳定版更新 GitHub/GHCR latest。原生 smoke 只下载与运行，
不重复编译；Docker 保留独立 job，仅承诺 ``linux/amd64``。

Git **不保存前端构建产物**，Go 模块也不包含 dist。消费者执行
``go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z``
后，首次 ``serve`` 自动从对应 Release 下载并校验 UI，保存在
``os.UserCacheDir()/qatlas/ui/v<version>``；后续校验缓存后离线启动。
GoReleaser 二进制已经内嵌该 UI，不需首次联网。两条路径最终进入相同的
``fs.FS`` 静态服务与 SPA fallback，没有两套业务服务。

资源生成顺序仍是 Sphinx 两站 → npm Web build → 完整性检查 →
``go run ./internal/cmd/uibundle -version <version> -output build/ui``。
Node 使用 ``web/.node-version``，npm 使用锁文件，Sphinx 依赖使用
``docsite/requirements.txt``；设置 ``SOURCE_DATE_EPOCH`` 为提交时间、
``TZ=UTC`` / ``PYTHONHASHSEED=0``，doctree 缓存放 ``build/`` 而非 bundle。
修改文档或前端后必须重新构建用于验证的资源，但只提交源码。
本地完整 UI/包准备后运行 ``goreleaser check`` 和
``goreleaser release --snapshot --clean``；snapshot 不是正式 Release，
其运行版本与 VERSION 不同，不用于普通源码安装的资源发现。

校验清单覆盖三个归档和 UI 包，attestation 的验证对象也是 **归档**：
``gh attestation verify ./qatlasd_<version>_linux_amd64.tar.gz --repo IAI-USTC-Quantum/QuantumAtlas``。
同源 SHA256 是完整性检查，不是独立签名；证明来源也不保证源码无漏洞。
公开的开发文档可从包直接读取，不得包含机密。

迁移时安装脚本必须取自 **同一新 tag** 的
``https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/<tag>/cmd/qatlasd/install-qatlasd.sh``，
再传入 ``--version <tag>``。旧线上实例内嵌的脚本不识别新归档，只有
维护者验证并授权升级后该入口才切换。安装器不改配置、注册服务或重启生产。

Git tag 一旦推送便可能被 Go proxy 发现，draft 不能阻挡模块安装；
UI 尚未公开时首次启动会明确失败，公开后重试。正确性检查必须在打 tag 前完成。
GitHub 与 GHCR 不是原子事务：失败要分别核对 draft/镜像状态，不能移动 tag、
覆盖公开 Release 或擅自升级线上。

标准化原则
----------

1. **每个组件有且只有一个版本唯一来源**；常规软件发布由 tag 触发，
   不以部署机现场 build 替代正式发布。文档的独立更新和下载执行端的
   当前构建例外见上表，后者必须记录明确的源码 revision；
2. **按组件选择分发渠道**：Docker 镜像进入 GHCR，独立 CLI 包进入
   PyPI；服务端预编译包和 UI 包沿用 GitHub Release，另支持 Go 模块源码
   安装。不增加包仓库或自建 Go 模块服务；镜像部署机只需 pull。
   下载执行端 downloaderworker / 旧 downloaderproxy 的例外见上表；
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
   ``tests/compose_test.go`` 中的结构测试锁定了 ghcr 来源与
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

回滚前先检查目标版本的 schema 兼容性与 Release notes。修改镜像 pin
或换回旧 binary 只回滚程序，不会逆转 PocketBase/goose 数据迁移；
即使 patch 版本也不能保证数据库回滚安全。必要时停止服务并按升级前
备份恢复数据库及相关状态，确认版本守卫允许后再启动。

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

服务端发版 checklist
----------------------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - 步骤
     - 验收
   * - 本地 CI mirror
     - Go test/vet（含 web 与 tests）、OpenAPI 同步、完整 UI 双构建一致，
       GoReleaser check/snapshot 全绿；发布也复用同 SHA 的检查
   * - ``VERSION`` + CHANGELOG
     - ``## vX.Y.Z (YYYY-MM-DD)`` 段落；breaking 写明迁移步骤
   * - tag
     - annotated ``vX.Y.Z``，与 ``VERSION`` 完全一致
   * - release workflow
     - draft、归档证明、三平台运行与镜像验证通过后公开；稳定版才更新
       ``:latest``，RC 仅版本 tag
   * - 部署
     - pin 版本 → pull → up -d → ``/api/health`` 版本与探针正确 →
       冒烟通过
   * - 客户端
     - ``uv tool upgrade qatlas-cli`` 后 ``(major, minor)`` 与 qatlasd
       对齐
