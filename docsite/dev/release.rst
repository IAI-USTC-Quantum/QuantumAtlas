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
     - 唯一取自已审核的 Git release tag ``vX.Y.Z[-rc.N]``，去 ``v`` 派生版本
     - push tag ``v*.*.*``，或在该 tag 上手动运行 release.yml
     - ghcr 双架构镜像（版本标签，非预发布另更新 ``latest``）+
       三平台二进制 + UI ZIP + GitHub Release；不再发布旧 PyPI 包
   * - ``qatlas-cli``
     - 由 qatlas-cli 独立仓库管理
     - 按该仓库的发布流程执行
     - PyPI ``qatlas-cli``\ （独立仓库 CI 发布）
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
     - 跟随主仓同一 Git tag 派生的版本
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
``quantum-atlas`` 版本；保留 ``PYPI_README.md`` 的退役说明入口，不保留
根 Python 项目、uv/Pixi 锁文件或旧包构建后端。文档的独立 requirements、
Sphinx/MkDocs hook 与 CI 标准库辅助脚本继续维护。

常规 ``v*`` 服务端流程继续发布二进制、镜像和 GitHub Release，不包含
PyPI 产物。这次旧包退役没有发布服务端或容器，当时服务端最新发行及
GitHub Latest 保持为 ``v0.34.0``；本次主仓版本流程调整也不构成新发行。

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

服务端分发由 GoReleaser OSS **v2.18.1** 发布
``qatlasd_<version>_<os>_<arch>.tar.gz``（Linux amd64/arm64、Darwin arm64），
以及 ``qatlasd_<version>_web.zip`` 和默认 ``qatlasd_<version>_checksums.txt``。
不自定义 archive/checksum 的命名、flags 或 ldflags；通过
``tags: [embedui]`` 显式内嵌完整 UI，版本适配默认 ``main.version``。
不为旧裸二进制附件添加兼容层、不重发 ``v0.34.0``。
``.goreleaser.yaml`` 的 ``git.ignore_tags`` 精确忽略
``quantum-atlas-v0.21.0``，这是 OSS 精确匹配，不是 glob 匹配；保留历史 tag
不移动、不删除。工具链/文档清理不构成新发行或发布授权。

主仓正式版本唯一从 Git release tag 派生，不维护根版本文件，不需要版本文件
bump commit，也不为版本专门修改 Go 源码/版本字段。维护者先人工选择尚未发布的
SemVer，推荐标准 ``vX.Y.Z[-rc.N]``，不使用 PEP 440 或 ``+build`` 标签。
移除自定义 gate 不代表任意 tag 都能用于 Go 模块、UI 或 Docker；这些消费者仍有
各自约束。完成待发源码与必要迁移文档的检查、提交和 review，确认候选 commit
的 CI 全绿，再在该已审核 SHA 上创建 annotated tag。Conventional Commits
只约定提交格式，不自动决定/bump 版本或触发发布。只推送这一个已审核 tag，
不批量推送历史 tag；完整命令见
`贡献指南 / 服务端发版 <https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/docs/contributing.md#release>`_。

现行和后续服务端 Release 正文统一由 GoReleaser 生成：
``changelog.use: git`` 使用 previous..current tags 的 commit 标题与 SHA，
输出到 ``dist/CHANGELOG.md`` 并用于 Release 正文。根 ``CHANGELOG.md``
已删除，不再维护或作为输入；不另写手工 notes、header/footer 或覆盖正文。
生成内容不包含 commit body/footer，breaking change 需在标题中明确标记
（如 ``feat(api)!: ...``），并在对应版本的功能/部署文档中说明迁移步骤。
历史 Git tags 与已发布 Release 保留原样，不追溯重写。

``release.yml`` 在 push tag ``v*.*.*`` 或手动选择 tag 运行时，prep 固定 checkout
事件的 ``github.sha``，再把 ``HEAD`` 解析为提交 SHA，供 checks 和发布共用；
不重新解析浮动 tag 来选择源码。先复用同 SHA 的 ``go.yml`` checks（Go test/vet、
integration 编译检查、OpenAPI、MkDocs、前端与两文档站双干净构建一致性），
再由 GoReleaser 构建和发布。
正式 UI 版本从传入的精确 ``release_tag`` 去掉前导 ``v`` 派生，仍确认
tag 所指提交 SHA = source SHA = ``HEAD``，并复用唯一验证过的 UI 包。
GoReleaser 的 ``GORELEASER_CURRENT_TAG`` 绑定真实触发 tag，不从最近旧 tag
或同 SHA 的其他 tag 猜测待发版本。

发布检查交给 GoReleaser 原生 Git/SemVer 校验与 preflight；preflight 沿用
默认只警告，不设置 ``fail_on_error: true``。不再维护自定义 release gate、
手动 draft → public → latest 状态机或独立 Docker/report job。
新 GitHub Release 内部先建 draft，全部附件上传成功后自动公开；
原生 Summary 默认已有，保留 ``prerelease: auto``。不等待三平台原生 smoke
或其他外部 smoke，GitHub Latest 采用 GoReleaser/平台默认行为。

镜像也由 ``.goreleaser.yaml`` 的 ``dockers_v2`` 管理，默认构建
``linux/amd64`` + ``linux/arm64``。``Dockerfile.goreleaser`` 复用上述
``embedui`` 预编译二进制，保留 distroless nonroot 与运行参数；原 ``Dockerfile``
仅保留为本地源码构建路径。GHCR 镜像使用 ``.Tag``、``.Version`` 两个版本标签，
每次非 prerelease 且非 snapshot 的镜像发布同时更新 ``latest``，不等待外部验证；
预发布不更新 ``latest``。它不是按版本大小排序的指针，也不与 GitHub Latest
构成同一套状态机。实际镜像构建、多架构 manifest 与运行仍需单独验证。

普通 branch/PR 的 ``go.yml`` 仍执行 UI 打包与恢复验证，但使用
``0.0.0-ci.g<完整Git提交SHA>`` 作为仅限 CI 的临时 SemVer 包标识；
``g`` 避免全数字 SHA 形成带前导零的非法数值标识。此路径不创建 tag、版本文件，
也不发布；不能把这个标识当作正式 Release 版本。

Git **只保存源码**，不提交 ``web/dist``、``web/public/doc``、
``web/public/devdoc``、根 ``dist/`` / ``build/``、缓存或 ELF 可执行文件；
Go 模块也不包含 dist。消费者执行
``go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z``
后，首次 ``serve`` 自动从自身精确版本的 Release 下载 UI ZIP 与 SHA256 清单，
校验 SHA256、包内版本与完整性后保存在
``os.UserCacheDir()/qatlas/ui/v<version>``；后续校验缓存后离线启动，不回退 latest。
只有附件已公开的新格式 Release 支持这条路径；``dev`` / Go 伪版本需完整构建
Sphinx 两站和 npm，再用 ``-tags embedui``。普通 Go build/test 不需要 UI。
GoReleaser tar.gz 内的二进制已经嵌入与独立 ZIP 相同的 UI，不需首次联网。
两条路径最终进入相同的
``fs.FS`` 静态服务与 SPA fallback，没有两套业务服务。

资源生成顺序仍是 Sphinx 两站 → npm Web build → 完整性检查 →
``go run ./internal/cmd/uibundle -version <version> -output build/ui``。
Go 工具链门槛唯一读 ``go.mod``；Node 使用 ``web/.node-version``，npm 使用
锁文件，Sphinx 依赖使用 ``docsite/requirements.txt``。完整本地命令见
:doc:`development`，不通过 Pixi 或根 Python 项目构建。
设置 ``SOURCE_DATE_EPOCH`` 为提交时间、
``TZ=UTC`` / ``PYTHONHASHSEED=0``，doctree 缓存放 ``build/`` 而非 bundle。
修改文档或前端后必须重新构建用于验证的资源，但只提交源码。
本地无正式 tag 时，用 ``UI_VERSION="0.0.0-ci.g$(git rev-parse HEAD)"``
显式传给 ``uibundle -version``，只做打包/恢复验证，不创建 tag、版本文件，也不发布。
只有 checkout 干净且已核验的正式 ``TAG`` 指向候选 SHA / ``HEAD`` 时，
才可用 ``UI_VERSION="${TAG#v}"``；
不要把最近旧 tag 当作待发版本。完整 UI/包准备后运行 ``goreleaser check`` 和
``goreleaser release --snapshot --clean --skip=docker``；不加 ``--skip=docker``
会触发本地 buildx 镜像构建。离线验证还需预先缓存工具链与依赖，跳过 Docker 的
snapshot 不构成镜像验收。快照运行版本由 GoReleaser 生成，不是正式 Release 版本，
不用于普通源码安装的资源发现。

后续采用新 workflow 的版本在 GoReleaser 发布之后，用两个官方
``actions/attest@v4.2.2`` 步骤生成签名构建证明：``subject-checksums`` 分别读取
``dist/qatlasd_<version>_checksums.txt``（三个归档与 UI ZIP）和
``dist/digests.txt``（镜像），默认登记到 GitHub，可用 ``gh attestation verify``
核验。不推送 registry bundle，只增加 ``id-token: write`` / ``attestations: write``
权限。checksum 保持默认版本化名称，不能改成 ``checksums.txt``，源码安装的 UI
下载器也依赖它。同源 SHA256 是完整性检查，不是来源签名；来源证明也不保证程序无 bug。
此修复尚未执行新发布，不为已发布的 ``v0.35.0-rc.1`` 追溯补签；该 RC 已验证的
BuildKit 双架构 SBOM/provenance 不受影响，不能视作新增 GitHub 证明。
公开的开发文档可从包直接读取，不得包含机密。

迁移时安装脚本必须取自 **同一新 tag** 的
``https://raw.githubusercontent.com/IAI-USTC-Quantum/QuantumAtlas/<tag>/cmd/qatlasd/install-qatlasd.sh``，
再传入 ``--version <tag>``。旧线上实例内嵌的脚本不识别新归档，只有
维护者验证并授权升级后该入口才切换。安装器不改配置、注册服务或重启生产。

Git tag 一旦推送便可能被 Go proxy 发现，不等待 GitHub/GHCR 发布完成；
UI 尚不可下载时首次启动会明确失败，可用后重试。正确性检查必须在打 tag 前完成。
GoReleaser 默认不覆盖同名附件，对 immutable Release 的实际发布会硬拒绝；
preflight 警告不保证失败无副作用。GitHub、GHCR 与证明登记不是原子事务：
attestation 失败时 Release 可能已公开、镜像已推送。先分别核对 Release、附件、
镜像、标签与证明状态，再决定恢复方案；不要直接重跑覆盖、移动已推 tag 或擅自升级线上。

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

qatlas-search 已按本方案接入标准化发布流程（首个 release：``v0.1.0``）。
以下 ``VERSION`` 均属于独立 app 仓库；其既有约定不随主仓的 tag-only 调整改变：

1. 该 app 仓库根目录的 ``VERSION`` 文件是版本唯一来源，发布由 push tag
   ``v*.*.*`` 触发；
2. 该 app 的 ``.github/workflows/release.yml`` 沿用早期主仓的 prep + docker 模式：
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
qatlasd 从磁盘的目录取材；目录缺失或为空时，qatlasd 回落到当前版本 UI
bundle 的文档副本（内嵌或校验后的缓存；实现见 ``internal/routes/docs.go``，
compose 模板把该目录以只读方式挂载进容器）。开发站的管理员限制只是 HTTP
门控，公开 bundle 可直接读取内容，不能存放秘密。

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

- 运维方应阅读目标版本的
  `GitHub Release <https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases>`_
  及对应版本的迁移/部署文档，确认 breaking change 与前置条件
  （如 v0.22.0 要求 PostgreSQL 先行就绪）；生成的提交列表不能替代迁移说明；
- 运维方应备份数据目录（``data/``，即 ``pb_data`` 和 ``raw``）；
- 运维方应确认当前版本与目标版本之间的兼容协议允许直接跳到目标版本
  （qatlasd 跨 minor 升级时应逐版本检查 Release 与迁移文档）。

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
  commit 标题中明确标记 breaking change，并在对应功能/部署文档中写明
  前置条件与迁移步骤；不能依赖默认生成正文不会展示的 commit footer。
  部署方按上文的前置检查执行；1.0 之后的不兼容变更按 SemVer 升 major。

服务端发版 checklist
----------------------

.. list-table::
   :header-rows: 1
   :widths: 40 60

   * - 步骤
     - 验收
   * - 人工选版本
     - 审核兼容性，选择未发布的 SemVer；不使用 PEP 440 或 ``+build``，不自动 bump
   * - 待发源码本地 CI mirror
     - gofmt、隔离 Go test/vet（含 web 与 tests）、integration 仅编译、
       OpenAPI 同步、CI Python 标准库 fixture、独立 MkDocs 与完整 UI 双构建，
       GoReleaser check/snapshot 全绿；发布也复用同 SHA 的检查
   * - 完成源码提交/review
     - breaking 标记在 commit 标题，迁移步骤在对应功能/部署文档；
       候选 SHA 已 review 且 CI 全绿，不另造版本 bump commit
   * - 创建并推送 tag
     - 在已审核候选 SHA 上创建 annotated ``vX.Y.Z[-rc.N]``，核对指向后
       只推送这一个 tag；正式版本唯一从它派生
   * - release workflow
     - 事件提交 SHA checks → GoReleaser 发布 → GitHub 签名构建证明；
       Git 生成正文，preflight 默认只警告；稳定镜像推送更新 ``latest``，RC 仅版本标签
   * - 产物验证
     - 核对归档、UI、镜像 manifest、GitHub 证明与实际运行；外部 smoke 不阻挡发布，
       证明失败可能发生在 Release 公开之后，先逐项核对远端状态
   * - 部署
     - pin 版本 → pull → up -d → ``/api/health`` 版本与探针正确 →
       冒烟通过
   * - 客户端
     - ``uv tool upgrade qatlas-cli`` 后 ``(major, minor)`` 与 qatlasd
       对齐
