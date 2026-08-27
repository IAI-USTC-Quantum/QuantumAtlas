代码组织
========

QuantumAtlas 主仓库是一个多语言单体仓库（monorepo），包含 Go 服务器、
Python 客户端、React 前端与部署模板。顶层布局：

.. code-block:: text

   QuantumAtlas/
   ├── cmd/qatlasd/        Go 服务器入口（内嵌 PocketBase + SPA + 本文档站）
   ├── internal/           Go 服务器内部包（见下表）
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
       ``paper_identities`` 三表，goose 迁移内嵌、启动时自动升级；
       去重唯一入口 ``ResolveOrMint``
   * - ``internal/search``
     - 多范式搜索引擎：Provider 接口 + 内置 catalog / arxiv / openalex /
       qdrant 四个 provider，并发 fan-out、按 DOI > arXiv > 标题归并去重
   * - ``internal/ingest``
     - 惰性收录管线：singleflight 去抖 → arXiv 抓取 → 对象存储 →
       注册表置 ready
   * - ``internal/mineru``
     - MinerU 转换器（上传通道，不依赖公网 S3）+ 每日调度器
       （0 点启动、每日上限、单进程防重叠）
   * - ``internal/routes``
     - HTTP 路由层：papers / search / auth / pat / admin / oauth-device
   * - ``internal/objstore``
     - S3 兼容对象存储客户端（RustFS / MinIO）
   * - ``internal/config``
     - YAML 配置加载与校验（``~/.qatlas/config.yaml``，拒绝环境变量）

前端（web/）
------------

React + TanStack Router 的 SPA，路由带语言前缀（``/zh``、``/en``），
``npm run build`` 产物（``web/dist``）通过 ``web/embed.go`` 内嵌进 Go
二进制；``web/public/doc`` 是本文档站的构建产物，作为静态文件随 SPA 一起
服务，免鉴权。

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

- 配置一律 YAML：客户端 ``~/.config/qatlas/config.yaml``，服务端
  ``~/.qatlas/config.yaml``；两侧都拒绝环境变量配置；
- Python 测试：``uv run --extra dev pytest tests/ search/tests rag/tests``；
  Go 测试：``go test ./internal/... ./cmd/...``（或 ``pixi run test-go``）；
- 文档站改动：编辑 ``docsite/`` 后运行两套构建——公开站
  ``sphinx-build -b html docsite web/public/doc``（``/doc``），开发站
  ``sphinx-build -b html -t devdocs -D root_doc=dev/index docsite web/public/devdoc``
  （``/devdoc``，管理员票据鉴权），再重新构建前端与镜像；
