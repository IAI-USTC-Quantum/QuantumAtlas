代码组织
========

QuantumAtlas 主仓库是一个多语言单体仓库（monorepo），包含 Go 服务器、
Python 客户端、React 前端与部署模板。顶层布局：

.. code-block:: text

   QuantumAtlas/
   ├── cmd/qatlasd/            Go 服务器入口（内嵌 PocketBase + SPA + 本文档站）
   ├── cmd/downloaderworker/   主动接入的下载节点（独立浏览器与持久暂存）
   ├── cmd/downloaderproxy/    旧单代理服务（仅兼容路径）
   ├── internal/               Go 服务器内部包（见下表）
   ├── qatlas/             Python 客户端（qatlas CLI）
   ├── web/                React SPA 前端
   ├── deploy/             docker-compose 部署模板
   ├── docsite/            本文档站源码（Sphinx + Furo）
   ├── tests/              Python 测试套件
   └── config.example.yaml 服务器配置完整 schema 参考

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

React + TanStack Router 的 SPA，路由带语言前缀（``/zh``、``/en``），
``npm run build`` 产物（``web/dist``）通过 ``web/embed.go`` 内嵌进 Go
二进制；``web/public/doc`` 是本文档站的构建产物，随 dist 一起内嵌，
作为文档的基线副本（免鉴权）；运行时 qatlasd 优先从
``~/.qatlas/docs`` 目录取材，详见"约定"一节。

客户端（Python，qatlas/）
-------------------------

.. code-block:: text

   qatlas/
   ├── cli.py            顶层命令分发（config / auth / paper / contrib / parser）
   ├── config.py         客户端 YAML 配置（~/.config/qatlas/config.yaml）
   ├── client/
   │   ├── paper.py      qatlas paper：取 PDF / Markdown（LRO 轮询）
   │   ├── contrib.py    qatlas contrib：上传与本地 MinerU 调度
   │   ├── upload.py     PDF / MinerU zip 上传
   │   ├── mineru.py     本地 MinerU runner（claim → 转换 → 回传）
   │   ├── auth.py       PAT 管理与 OAuth Device Flow 登录
   │   ├── config.py     qatlas config 子命令
   │   └── plugins/      客户端插件框架（entry-point 发现，见下页）
   └── parser/           本地 arXiv 抓取 + MinerU 解析（qatlas parser）

约定
----

- 主客户端与主服务配置使用 YAML：客户端 ``~/.config/qatlas/config.yaml``，
  qatlasd ``~/.qatlas/config.yaml``；独立 ``downloaderworker`` 则使用
  ``DL_WORKER_*`` 环境变量与非凭据 flags，不能把主进程约束推广到执行节点；
- Python 测试：``uv run --extra dev pytest tests/``；
  Go 测试：``go test ./internal/... ./cmd/...``（或 ``pixi run test-go``）；
- 文档站：公开站 ``/doc`` 与开发站 ``/devdoc``（管理员票据鉴权）由
  ``.github/workflows/docs.yml`` 独立构建并发布为 ghcr 上的
  ``qatlas-docs`` 镜像；部署机运行 ``deploy/update-docs.sh`` 把新文档写入
  ``~/.qatlas/docs``，qatlasd 从该目录取材（目录缺失或为空时回落到
  二进制内嵌的副本，见 ``internal/routes/docs.go``），更新文档不需要
  重启服务。本地预览时开发者仍可直接运行两套 sphinx 构建：
  公开站 ``sphinx-build -b html docsite web/public/doc``，开发站
  ``sphinx-build -b html -t devdocs -D root_doc=dev/index docsite
  web/public/devdoc``；
