代码组织
========

QuantumAtlas 主仓库包含 Go 服务器、React 前端、部署模板与服务端测试。
Python 命令行客户端由独立仓库 ``IAI-USTC-Quantum/qatlas-cli`` 维护，
不在本仓库分发。顶层布局：

.. code-block:: text

   QuantumAtlas/
   ├── cmd/qatlasd/             Go 服务入口（PocketBase；完整构建内嵌 UI/文档）
   ├── cmd/downloaderworker/    主动接入的下载节点（独立浏览器与持久暂存）
   ├── cmd/downloaderproxy/     旧单代理服务（仅兼容路径）
   ├── internal/               Go 服务器内部包（见下表）
   ├── web/                    React SPA；.node-version + package-lock.json
   ├── deploy/                 docker-compose 部署模板
   ├── docsite/                双站源码（Sphinx + Furo，含组件提交锁）
   ├── docs/                   历史 Markdown 源；当前构建使用 docsite/manual/
   ├── tests/                  部署结构、离线 fixture 与显式启用的生产冒烟
   ├── .github/scripts/        CI 资源校验与 Python 标准库 fixture
   ├── .goreleaser.yaml        服务端归档、GitHub Release 与 GHCR 发布
   ├── Dockerfile.goreleaser   封装 GoReleaser 预编译二进制的发布镜像
   ├── Dockerfile              本地从源码构建完整服务端镜像
   ├── go.mod / go.sum         Go 工具链门槛、依赖与 Go tool 声明
   └── config.example.yaml     服务器配置完整 schema 参考

服务器（Go）
------------

``qatlasd`` 以 PocketBase 为应用框架（认证 / 会话 / 钩子），业务逻辑全部在
``internal/`` 下按领域分包：

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - 包
     - 职责
   * - ``internal/registry``
     - PostgreSQL 论文注册表：``papers`` / ``paper_assets`` /
       ``paper_identities`` 等表，goose 迁移内嵌、启动时自动升级；
       去重入口 ``ResolveOrMint``，也提供下载接收记录及旧 pending 论文的接管
   * - ``internal/search``
     - 多范式搜索引擎：Provider 接口 + 内置 catalog / arxiv / openalex
       三个 provider（可选叠加 qatlas-search 微服务的 remote provider），
       并发 fan-out、按 DOI > arXiv > 标题归并去重
   * - ``internal/rag``
     - qatlas-rag 微服务的索引推送客户端：论文置 ready 后 qatlasd 调用
       它向 qatlas-rag 推送索引构建任务（语义检索已由 qatlas-rag 承担）
   * - ``internal/ingest``
     - 惰性收录管线：singleflight 去抖 → arXiv 抓取 → 对象存储 →
       注册表置 ready
   * - ``internal/mineru``
     - MinerU 转换器（上传通道，不依赖公网 S3）+ 每日调度器
       （0 点启动、每日上限、单进程防重叠）
   * - ``internal/downloader``
     - 共享多范式策略梯、本地优先下载、RemoteFetcher 委派、PDF 验证、
       归档回调与持久接收记录恢复；本地槽位默认 2，远端等待不占本地槽位。
       旧 RemoteProxy 仍保留，详见 :doc:`downloader`
   * - ``internal/downloadfleet``
     - qatlasd 内的 PostgreSQL 节点审批、租约调度、上传暂存、归档回执及
       后续处理 outbox；不是额外部署的调度服务
   * - ``internal/downloadworker``
     - 出站注册 / 心跳 / 领取 / 上传 runner，独立持久卷与有界执行，
       单一浏览器监督器和结果保留清理
   * - ``internal/workerprotocol``
     - ``/api/downloader/workers/v2`` 的共享 wire 类型与路径常量
   * - ``internal/userkeys``
     - 用户第三方搜索 API key 的 AES-256-GCM 加密存储
       （``search_api_keys`` 集合，密钥派生自 system PAT），供
       ``/api/search/multi`` 代理注入
   * - ``internal/routes``
     - HTTP 路由层：papers / search（含逐平台 ``/api/search/multi``
       与 backend 目录 ``/api/search/backends``）/ downloader
       （``/api/downloader/*``）/ auth / pat / me（含
       ``/api/me/search-keys``）/ admin（含 8 个资产浏览端点
       ``/api/admin/assets/*``）/ oauth-device / docs
   * - ``internal/objstore``
     - S3 兼容对象存储客户端（RustFS / MinIO）
   * - ``internal/config``
     - YAML 配置加载与校验（``~/.qatlas/config.yaml``，拒绝环境变量）

前端（web/）
------------

React + TanStack Router 的 SPA，路由带语言前缀（``/zh``、``/en``）。
Git 仅存源码，不提交 ``web/dist`` 或 Sphinx 生成的
``web/public/doc`` / ``web/public/devdoc``。固定 Node 版本见
``web/.node-version``；Sphinx 两站构建后再运行 ``npm ci`` / ``npm run build``。

``web/embed.go`` 仅在 ``embedui`` build tag 下编译：GoReleaser 嵌入完整
dist，``dockers_v2`` 通过 ``Dockerfile.goreleaser`` 复用该二进制发布镜像；
原 ``Dockerfile`` 仍支持本地从源码构建。普通 Go 模块构建使用 ``embed_none.go``，
即使没有 Node、Sphinx、dist 也能完成 ``go install``。
``web.Resolve`` 在首次 serve 时优先内置资源，否则校验版本缓存，缺缓存才从
对应 GitHub Release 下载 UI ZIP 与 SHA256 清单；不使用 latest 回退。
ZIP 不解压到缓存目录，校验路径、类型、重复项、版本、尺寸与内容后以
支持 seek 的 ``fs.FS`` 提供，静态服务、SPA fallback 与业务路由共用。
``internal/cmd/uibundle`` 从同一 dist 打包，确保 GoReleaser tar.gz 中内嵌的
UI 与独立 ZIP 的 UI 内容一致。``dev`` / Go 伪版本没有精确 Release 可下载，
需先构建 Sphinx 两站和 npm 资源，再用 ``-tags embedui`` 构建或运行。
普通 ``go build`` / ``go test`` 无此资源前置条件，完整命令见 :doc:`development`。
根 ``dist/`` / ``build/``、生成站点、缓存和 ELF 也不提交 Git。

两文档站也在完整 bundle 中：``/doc`` 公开，``/devdoc`` 仍需管理员票据。
既有 ``~/.qatlas/docs`` 显式磁盘覆盖保持不变，详见"约定"一节。
公开发布的归档/二进制可直接读取开发文档，管理员 HTTP 门控不是保密措施。

客户端（独立仓库）
--------------------

``qatlas`` CLI 的实现位于 `IAI-USTC-Quantum/qatlas-cli
<https://github.com/IAI-USTC-Quantum/qatlas-cli>`_ 的 ``src/qatlas/``，
包括命令分发、HTTP 客户端、YAML 配置、插件框架和本地 arXiv/MinerU
工作流；客户端测试也在该仓库运行。本仓库只保留相关的使用与协议文档，
不再包含 ``qatlas/`` Python 命名空间。

旧 PyPI 包 ``quantum-atlas`` 的最终迁移版 ``0.21.0`` 已发布，没有运行时
依赖或命令入口，也不会转发安装 ``qatlas-cli``；此后不再发布旧包版本。
发布时的元数据、检查器、测试与 workflow 保留在固定历史 tag
`quantum-atlas-v0.21.0 <https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0>`_
中供审计，不再属于 main 的开发与发布流程。
原来的 ``qatlas.paper_assets`` 和 ``qatlas.parser.doi`` 帮助库已退役，
不属于独立 CLI 的等价迁移承诺。服务端资产路径与 DOI 处理仍由
``internal/paperassets``、``internal/openalex`` 等 Go 包实现，保持不变。

约定
----

- 主客户端与主服务配置使用 YAML：客户端 ``~/.config/qatlas/config.yaml``，
  qatlasd ``~/.qatlas/config.yaml``；独立 ``downloaderworker`` 则使用
  ``DL_WORKER_*`` 环境变量与非凭据 flags，不能把主进程约束推广到执行节点；
- 开发命令直接使用 ``go``、``go tool swag``、``npm`` 和文档工具，
  完整步骤见 :doc:`development`。没有根 Python 项目或 ``uv sync`` 流程，
  不引入 Pixi、Makefile、Taskfile 或新构建框架。Go 工具链门槛只读 ``go.mod``；
  Sphinx 由 ``.github/scripts/build_docs.py`` 的 PEP 723 头与旁边的锁文件管理；
  组件文档提交由 ``docsite/components.lock.json`` 锁定。``.github/scripts`` 的
  部分 Python fixture 仅用标准库，不用 pytest。
- 服务端测试使用 Go：``tests/compose_test.go`` 检查部署模板，
  ``tests/e2e/`` 的普通 fixture 只使用本地 ``httptest``。
  ``CGO_ENABLED=0 go test ./internal/... ./cmd/... ./web ./tests/...``
  不需 UI 产物，也不启用真实目标测试。真实 PG/Fleet/MinerU/OpenAlex 测试需
  ``-tags integration`` 编译期开关及原有环境目标/flag 双门禁，S3 文件已有
  ``integration`` tag；混合文件保留离线测试。生产冒烟由 nightly 单独选择
  ``e2e`` tag 与 ``QATLAS_SERVER_TARGETS``，缺目标会失败。
  即使已有门禁，开发检查仍清除真实目标与凭据、使用临时 HOME/XDG 目录，
  只显式复用 PATH 和 Go 缓存，避免误读默认配置；见 :doc:`development`；
- 文档站：公开站 ``/doc`` 与开发站 ``/devdoc``（管理员票据鉴权）由
  ``.github/workflows/docs.yml`` 独立构建并发布为 ghcr 上的
  ``qatlas-docs`` 镜像；部署机运行 ``deploy/update-docs.sh`` 把新文档写入
  ``~/.qatlas/docs``，qatlasd 从该目录取材（目录缺失或为空时回落到
  当前版本 bundle 的副本，来源可为内嵌或缓存，见 ``internal/routes/docs.go``）。
  已选磁盘目录内部更新不需重启；改变来源仍需重启。本地构建公开站和开发站
  的直接命令见 :doc:`development`，使用独立 ``build/doctrees`` 缓存与可复现
  时间环境；不要把 Sphinx 缓存混入发行资源。
