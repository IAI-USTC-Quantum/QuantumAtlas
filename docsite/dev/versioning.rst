版本与兼容策略
==============

QuantumAtlas 由两个独立演进的组件构成：服务端 ``qatlasd`` （本仓库）与
命令行客户端 ``qatlas-cli`` （独立仓库 `IAI-USTC-Quantum/qatlas-cli
<https://github.com/IAI-USTC-Quantum/qatlas-cli>`_）。两者有**各自独立
的版本号**，它们靠一条明确的兼容协议协作，而不是共享一个版本号。

版本来源
--------

.. list-table::
   :header-rows: 1
   :widths: 25 40 35

   * - 组件
     - 版本唯一来源
     - 发布方式
   * - ``qatlasd`` （本仓库）
     - 仓库根目录的 ``VERSION`` 文件 + 形如 ``v<version>`` 的 git tag
       （release.yml 的 prep job 会强校验二者一致，不一致直接失败）
     - 手动编辑 ``VERSION`` → commit → ``git tag v<version>`` → push tag，
       release.yml 自动出二进制 / 镜像 / GitHub Release
   * - ``qatlas-cli``
     - 它自己仓库的 ``pyproject.toml`` （commitizen 管理）
     - ``cz bump`` 打 tag 后由该仓 CI 发 PyPI（``uv tool install qatlas-cli``）

旧 PyPI 包 ``quantum-atlas 0.21.0`` 的最终迁移版已发布；此后不再发布
旧包版本。它仅含元数据与迁移说明，没有运行时依赖、``qatlas`` Python
命名空间或 console script，也不依赖或自动转发到 ``qatlas-cli``。
旧 ``qatlas.paper_assets`` 与 ``qatlas.parser.doi`` 帮助库已退役，不能把
安装 ``qatlas-cli`` 当作这些 Python API 的等价迁移。CLI 用户需要手动
卸载旧包，再安装独立的 ``qatlas-cli``。

固定历史 tag
`quantum-atlas-v0.21.0 <https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0>`_
保留发布时的元数据、检查器、测试及 workflow 供审计，不再是 main 的
发布入口。根目录 ``pyproject.toml`` 现在仅用于不分发的 uv 开发环境、
依赖组与 Pixi 工具链，不维护旧包版本或构建后端。
常规服务端发版仍以 ``VERSION`` 与 ``v<version>`` 为准，并且没有 PyPI
产物；旧包退役没有改变 ``VERSION``（仍为 ``0.34.0``），也不参与
客户端/服务端版本协商。发布历史说明见 :doc:`release`。

运行时版本与 UI 绑定
--------------------

运行时版本依次使用 GoReleaser 默认注入的 ``main.version``、
``debug.ReadBuildInfo().Main.Version``（带版本的 ``go install``）、
``dev``。仅去掉开头的 ``v``，保留预发布与有意义的构建标记。
解析在 ``--version`` 之前完成，不加载配置、不访问数据库或网络；CLI、
health、server-info 与响应头共享同一个值。Docker 自行编译时也注入
``main.version``，不能把 ``pyproject.toml`` 的工具版本当服务端版本。

新服务端 tag 使用有效 SemVer，例如 ``v0.35.0-rc.1``，不使用 Python
风格 ``v0.35.0a1``。``VERSION`` 不因本次实现而递增，也不重发
``v0.34.0``。预发布不会覆盖稳定版 Latest。

普通源码安装首次 ``serve`` 自动获取 **自身精确版本** 的 Release UI，
校验后按版本缓存。``dev``、空版本及 Go 伪版本没有可靠的对应 UI Release，
会报错而不是下载 latest；开发者需构建 UI 后使用 ``-tags embedui``。
GoReleaser 预编译程序已经内嵌同一 UI，首次启动无需联网补资源。
Go tag 可被发现不等于 Release UI 已公开：draft 发布窗口内缺附件会明确报错。

兼容协议
--------

**qatlasd 与 qatlas-cli 的 ``(major, minor)`` 相同即兼容**，patch 位
允许随意漂移。兼容性修复只 bump patch，例如：

- qatlasd ``0.22.4`` ↔ qatlas-cli ``0.22.3``：兼容（推荐配对，各自升到
  同 ``x.y`` 线的最新 patch 即可）；
- qatlasd ``0.23.0`` ↔ qatlas-cli ``0.22.x``：不兼容（``minor`` 不同）。

运行时握手：qatlas-cli 发出的每个请求都带 ``X-Qatlas-Client-Version``
头，qatlasd 返回的每个响应都带 ``X-Qatlas-Server-Version`` 头。客户端
按上面的协议比较：

- ``(major, minor)`` 一致：客户端静默通过（patch 差异不影响兼容）；
- server 更新且当前为写操作：客户端硬失败（exit code 4），并提示用户
  执行 ``uv tool upgrade qatlas-cli``；
- server 更新且当前为读操作：客户端在 stderr 警告一次，然后继续执行；
- client 更新：客户端在 stderr 警告一次（提示运维方升级 qatlasd），
  然后继续执行；
- 响应没有版本头（0.8.0 之前的老服务端）：客户端跳过版本协商。

主仓 ``CHANGELOG.md`` 的 Unreleased 段记录 qatlasd 的变更；qatlas-cli
的变更记录在它自己仓库的 ``CHANGELOG.md`` 中。
