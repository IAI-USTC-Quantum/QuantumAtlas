开发入门：Go 原生工作流
========================

本页从新 checkout 开始，覆盖 Go 检查、完整 UI 构建、隔离的本地联调与文档预览。
以下 Bash 命令均在仓库根目录执行（括号内的 ``cd web`` 除外），面向 Linux/macOS。
它们不是发布或生产操作授权；不要把生产配置、数据库、对象存储或 token 带进开发环境。
代码分层见 :doc:`layout`，版本与发布边界见 :doc:`versioning`、:doc:`release`。

选择所需工具
------------

- **只改 Go / 跑普通测试**：安装 Git 和满足根 ``go.mod`` 的 ``go`` 指令的
  Go 工具链。``go.mod`` 是 Go 工具链门槛的唯一来源，CI 也从它读取版本；
  不另维护 Pixi/Makefile/Taskfile 或另一份 Go 版本要求。依赖由 ``go.mod`` /
  ``go.sum`` 管理，OpenAPI 生成器已经登记为 Go tool。
- **改前端 / 启动本地 dev 服务 / 制作完整分发资源**：再安装
  ``web/.node-version`` 指定的 Node，使用随 Node 提供的 npm 和
  ``web/package-lock.json``。不要为一次构建顺手升级依赖。
- **构建文档**：Python 只用于独立文档工具。Sphinx 使用
  ``docsite/requirements.txt``；MkDocs 使用 ``docs/requirements.txt``。
  CI 使用 Python 3.12，可在本地创建同版本的隔离虚拟环境。
  ``.github/scripts`` 的 Python 测试只依赖标准库，不要求安装这两套文档依赖。

主仓不是 Python 应用或可安装的 Python 项目，没有根 ``pyproject.toml`` /
``uv.lock`` / ``pixi.lock`` 开发契约，不执行 ``uv sync`` 或 ``pip install .``。
``qatlas-cli`` 的开发、测试与发版都在其独立仓库；本仓保留 Sphinx、MkDocs、
``hooks/`` 与 CI 辅助脚本，不应将它们误当作旧 Python 包残留删除。

.. code-block:: bash

   git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
   cd QuantumAtlas
   go version
   go mod download
   CGO_ENABLED=0 go build -o build/qatlasd ./cmd/qatlasd
   ./build/qatlasd --version

普通 ``go build`` / ``go test`` 不需要 Node、Sphinx 或 ``web/dist``，也不需要
``embedui`` tag。源码 checkout 通常显示 ``dev``；``--version`` 不加载业务配置、
不连接数据库，也不下载 UI。**构建成功不表示这个 dev 二进制能直接 serve**。

测试前先隔离环境
----------------

已有终端可能携带真实集成测试的开关与凭据，包括
``QATLAS_TEST_PG_DSN``、``TEST_DOWNLOADFLEET_DATABASE_URL``、
``MINERU_LIVE_TEST``、``MINERU_API_TOKEN``、``QATLAS_TEST_LIVE``、
``QATLAS_S3_TEST_*``、``QATLAS_SERVER_TARGETS`` 以及其 ``token-env`` 指向的变量。
不要仅凭测试名含 fixture、文件名含 integration 或一次执行显示 skip 就认定安全。
不加载 ``deploy/.env``，不借用默认 ``~/.qatlas/config.yaml``，不复用生产数据目录。

下列隔离块使用 ``env -i`` 清除继承环境，包括上述开关、目标与凭据；只显式保留
``PATH`` 和 Go 工具链/模块/编译缓存路径。临时 ``HOME`` 和 XDG 目录隔离默认配置、
文档覆盖与运行数据；退出时仅清理本块创建的临时目录。保留缓存并不等于保留业务配置。
若依赖未缓存，Go 仍可能下载工具链/模块；这里的“离线测试”指不访问真实业务目标，
不是禁止所有工具链网络下载或本地 ``httptest`` 回环连接。

.. code-block:: bash

   (
     set -euo pipefail
     TEST_HOME="$(mktemp -d)"
     trap 'rm -rf "$TEST_HOME"' EXIT
     env -i PATH="$PATH" HOME="$TEST_HOME" \
       XDG_CONFIG_HOME="$TEST_HOME/.config" \
       XDG_DATA_HOME="$TEST_HOME/.local/share" \
       XDG_STATE_HOME="$TEST_HOME/.local/state" \
       XDG_CACHE_HOME="$TEST_HOME/.cache" \
       GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" \
       GOMODCACHE="$(go env GOMODCACHE)" CGO_ENABLED=0 \
       bash -eu -c '
         go test ./internal/... ./cmd/... ./web ./tests/...
         go vet ./internal/... ./cmd/... ./web ./tests/...
         go test -tags=integration ./internal/... ./cmd/... ./web ./tests/... -run "^$"
         go test -tags=e2e ./tests/e2e -run "^TestSmokeFixture" -count=1
       '
   )

``tests/compose_test.go`` 检查部署模板；普通 ``tests/e2e`` 测试使用本地
``httptest``，不启动 Docker 或生产服务。上面的最后一行只选择本地 fixture，
不能去掉 ``-run`` 后当作普通测试运行：真实生产冒烟位于带 ``e2e`` build tag 的
``tests/e2e/production_smoke_test.go``，显式启用后缺少目标会失败。
真实 PostgreSQL/Fleet、MinerU、OpenAlex 测试由 ``testutil.IntegrationEnabled``
编译期开关门控：只有 ``-tags integration`` 才启用，且仍需原有的显式环境目标/
flag。混合测试文件不整体排除，其离线测试仍默认运行。S3 测试文件自身已有
``integration`` tag；生产冒烟的 ``e2e`` tag 与它独立。默认测试即使残留 live
开关也不应连接真实外部服务，但环境与 HOME 隔离仍不可省略。上面的 integration
命令只用 ``-run '^$'`` 验证编译，不执行集成用例。

只有获准访问可丢弃的测试库、专用 bucket 或测试账号时，才单独设置所需变量、
选择对应集成测试；它们可能写入、迁移、删除数据或消耗配额。本入门不提供生产
目标，也不默认执行真实集成测试。

格式化、OpenAPI 与提交前检查
----------------------------

无需 Pixi task，直接调用 Go 工具。下面的格式化命令只处理 Git 已跟踪的 Go
源码；新建的 Go 文件应先对其明确路径运行 ``gofmt -w path/to/new.go``。

.. code-block:: bash

   # 检查：应无输出；修复：将 -l 改为 -w，再审核 diff
   git ls-files -z -- '*.go' | xargs -0 gofmt -l

   # 从 handler 注释重新生成已跟踪的 OpenAPI 文件
   go tool swag init -g main.go -d ./cmd/qatlasd,./internal/routes \
     -o internal/apidocs --parseInternal --parseDepth 1
   git diff -- internal/apidocs
   # CI 要求生成后没有漂移；有意改 API 时一并提交生成的源码/spec
   git diff --exit-code -- internal/apidocs

   # CI 归档/发布门禁的纯标准库 fixture，不是 pytest 或 Python 应用测试
   python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v
   git diff --check

``internal/apidocs``、``web/src/routeTree.gen.ts`` 是按仓库约定跟踪的生成源码，
不同于不得提交的分发资源。前端 ``npm run build`` 会运行
``tsr generate && tsc -b && vite build``；修改路由后审核生成路由树。
前端 API 类型目前在 ``web/src/lib/api.ts``；不要假定 ``gen:api`` 已配置了自动同步链。

完整 UI：两套 Sphinx → npm → embedui
-------------------------------------

首次配置 Sphinx 环境（后续复用）：

.. code-block:: bash

   python3 -m venv build/venv-sphinx
   build/venv-sphinx/bin/python -m pip install -r docsite/requirements.txt
   # 先用自己的 Node 版本管理器选择 web/.node-version 指定的版本
   (cd web && npm ci)

每次修改文档或前端后，从源码重新构建完整资源：

.. code-block:: bash

   export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
   export TZ=UTC PYTHONHASHSEED=0
   # 只清理这些可再生输出，不触碰运行数据或个人缓存
   rm -rf web/public/doc web/public/devdoc web/dist build/doctrees web/node_modules/.tmp
   build/venv-sphinx/bin/sphinx-build \
     -b html -d build/doctrees/public docsite web/public/doc
   build/venv-sphinx/bin/sphinx-build \
     -b html -d build/doctrees/dev -t devdocs -D root_doc=dev/index \
     -D html_title="QuantumAtlas 开发文档" docsite web/public/devdoc
   # 打包前确认当前 shell 和所有会加载的 .env 文件都没有 token
   (cd web && npm run build)
   CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd
   SERVER_VERSION="$(tr -d '[:space:]' < VERSION)"
   go run ./internal/cmd/uibundle -version "$SERVER_VERSION" -output build/ui

再在隔离环境验证内嵌资源；不能直接继承带真实目标的联调终端环境：

.. code-block:: bash

   (
     set -euo pipefail
     TEST_HOME="$(mktemp -d)"
     trap 'rm -rf "$TEST_HOME"' EXIT
     env -i PATH="$PATH" HOME="$TEST_HOME" \
       XDG_CONFIG_HOME="$TEST_HOME/.config" \
       XDG_DATA_HOME="$TEST_HOME/.local/share" \
       XDG_STATE_HOME="$TEST_HOME/.local/state" \
       XDG_CACHE_HOME="$TEST_HOME/.cache" \
       GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" \
       GOMODCACHE="$(go env GOMODCACHE)" CGO_ENABLED=0 \
       go test -tags embedui ./web ./cmd/qatlasd/...
   )

``web/embed.go`` 只在 ``embedui`` tag 下嵌入完整 ``web/dist``，普通构建走
``web/embed_none.go``。两文档站缺一不可；只运行 npm 不能凭空生成 Sphinx 页面。
Doctree/pickle 缓存放 ``build/doctrees``，不能随 UI 发行。CI 对同一提交做两次
干净的双站和前端构建，比较整个树的路径与字节（包括缺失、隐藏文件及时间信息），
再验证 UI ZIP 的恢复树与嵌入树一致；不要靠忽略 diff 或保留旧文件通过检查。

Git 只保存源码与必要锁文件。不要提交 ``web/dist``、``web/public/doc``、
``web/public/devdoc``、根 ``dist/`` / ``build/``、MkDocs ``site/``、
``node_modules``、缓存、运行配置或 ELF 可执行文件。始终显式将本地 Go 输出放到
``build/``，而不是把 ``qatlasd`` / ``downloaderproxy`` / ``downloaderworker``
留在仓库根目录。禁止通过 ``git add -f`` 绕过这些边界。

源码安装与发布资源的区别
------------------------

对已经采用此 UI 分发流程、且附件已公开的 **精确 Release tag**，用户可以
执行下列安装命令；先把示例 tag 替换成已核验的实际 tag，不使用 ``@latest``
掩盖版本关系，也不要假定旧 ``v0.34.0`` 有新格式附件。

.. code-block:: bash

   go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z

此安装只编译 Go 源码，不执行 npm。首次 ``serve`` 在没有内嵌 UI 时，从
**同一个 Release** 下载 UI ZIP 与 SHA256 清单，校验 SHA256、包内版本和资源
完整性后缓存到 ``os.UserCacheDir()/qatlas/ui/v<version>``；后续启动也校验缓存。
不存在 latest 回退。无对应 Release 的 ``dev`` / Go 伪版本必须使用上面的完整
Sphinx 两站与 npm 构建，再以 ``-tags embedui`` 构建或运行本地服务。

GoReleaser 的 ``.tar.gz`` 内程序已嵌入 UI，独立 ``_web.zip`` 来自同一份
验证过的树，二者内容一致，不是两套 UI。GoReleaser OSS v2.18.1 保留默认
names、flags、ldflags，``git.ignore_tags`` **精确匹配** 忽略
``quantum-atlas-v0.21.0``；不是 OSS glob 匹配，不移动或删除历史 tag。
Snapshot 版本可不同于源码 ``VERSION``，不代表正式版本可用。

``VERSION`` 是服务端发布版本来源；工具链清理不递增它，不重发 ``v0.34.0``，
不发布旧 PyPI 包或独立 ``qatlas-cli``。保留 ``PYPI_README.md`` 与固定历史 tag
用于退役说明及审计。推送 Git tag 后 Go 模块可能已经可见，GitHub draft 不会
阻挡模块安装；UI 附件尚未公开时首次启动会明确失败。GitHub/GHCR 也不是原子
发布事务；本地构建验收不授权创建 tag、Release、镜像或升级生产。

本地服务与 Vite 联调
--------------------

先完成完整 UI 构建，然后在**独立终端**创建临时 HOME 和显式开发配置。
即使 Vite 单独提供前端，源码 ``go run`` 的 ``serve`` 仍会校验 UI，必须带
``-tags embedui``。以下配置不启用 PostgreSQL、S3、OAuth、MinerU 或远端服务；
数据与 PocketBase 留在临时 XDG 目录。仅做本地界面检查，不触发外部检索/下载。

.. code-block:: bash

   (
     set -euo pipefail
     DEV_HOME="$(mktemp -d)"
     trap 'rm -rf "$DEV_HOME"' EXIT
     env -i PATH="$PATH" HOME="$DEV_HOME" \
       XDG_CONFIG_HOME="$DEV_HOME/.config" \
       XDG_DATA_HOME="$DEV_HOME/.local/share" \
       XDG_STATE_HOME="$DEV_HOME/.local/state" \
       XDG_CACHE_HOME="$DEV_HOME/.cache" \
       GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" \
       GOMODCACHE="$(go env GOMODCACHE)" CGO_ENABLED=0 \
       bash -eu -c '
         go run ./cmd/qatlasd config init --config "$HOME/config.yaml"
         go run -tags embedui ./cmd/qatlasd serve \
           --config "$HOME/config.yaml" --http=127.0.0.1:4200
       '
   )

正常配置入口是 ``--config`` 或默认 ``~/.qatlas/config.yaml``，不是
``QATLAS_SYSTEM_PAT`` 等服务端环境变量；配置形状的环境变量会被拒绝。
如需长期保留本地数据，请单独准备已审核的开发 YAML 与数据路径，不复制生产配置。

在另一个终端：

.. code-block:: bash

   cp web/.env.development.example web/.env.development.local
   # 保持 target=http://127.0.0.1:4200；只看界面时可设 FAKE_AUTH=1
   (cd web && npm run dev -- --host 127.0.0.1)

浏览器地址以 Vite 打印的本机 URL 为准。当前代理仅覆盖 ``/api``、``/_``、
``/share``、``/swagger``，不含 ``/doc`` / ``/devdoc``；文档路由请到后端地址验证。
``VITE_DEV_FAKE_AUTH`` 只是前端 stub，不授予后端读、写或管理员权限。
如需 API 数据，应在隔离开发后端配置最小权限的真实 Bearer；它可能授权写入，
不能宣称“fake auth 下所有写操作一定 401”。

``VITE_DEV_API_PAT`` 由代理注入 ``/api`` 请求，目标服务的 system PAT 来自
YAML ``system_pat.token``，不是旧环境变量。``VITE_`` 是 Vite 可暴露给客户端的
前缀，不能保证 token 永不进入包；仅在本地隔离联调时使用，生产打包前确保 shell
与会加载的 env 文件不含 token。不要将带真实凭据的 Vite 服务开放到公网，也不要
把生产后端作为日常前端开发目标。更完整的路由表和代理约束见仓库 ``web/README.md``。

MkDocs 与开发文档的验收
------------------------

MkDocs 是独立文档体系，不代替上面的两套 Sphinx 站点，也不进入同一 UI 构建链。
保留其 ``hooks/openapi_spec.py`` 等文档 hook；不需要旧 Python 包、pytest 或
``mkdocstrings`` Python API 自动文档插件。

.. code-block:: bash

   python3 -m venv build/venv-mkdocs
   build/venv-mkdocs/bin/python -m pip install -r docs/requirements.txt
   build/venv-mkdocs/bin/mkdocs build --strict --site-dir build/mkdocs
   build/venv-mkdocs/bin/mkdocs serve --dev-addr 127.0.0.1:8000

Sphinx 开发文档经服务端 ``/devdoc`` HTTP 管理员门控访问，但公开的二进制和
UI ZIP 中也能直接读取其内容。**HTTP 仅管理员可见不等于内容保密**，不要写入
密钥、个人 token、内网凭据或私有部署信息。

提交前应通过隔离 Go test/vet、gofmt、OpenAPI 同步、CI 标准库 fixture、
MkDocs build，以及完整 Sphinx/前端双构建与内嵌资源检查。新生成的站点仅用于
验收，不提交 Git；再次审核 ``git status --short`` 与 ``git diff --check``。
