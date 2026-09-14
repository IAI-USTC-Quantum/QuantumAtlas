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
   * - ``quantum-atlas`` 旧 PyPI 包（退役迁移说明）
     - 本仓库 ``pyproject.toml`` 固定为最后一个版本 ``0.21.0``
     - 仅精确 tag ``quantum-atlas-v0.21.0`` 触发最后一次迁移发布；
       不随 qatlasd 的 ``v*`` tag 继续发布

``quantum-atlas 0.21.0`` 是仅含元数据与迁移说明的最终版本：没有运行时
依赖、``qatlas`` Python 命名空间或 console script，也不依赖或自动转发到
``qatlas-cli``。旧 ``qatlas.paper_assets`` 与 ``qatlas.parser.doi`` 帮助库
已退役，不能把安装 ``qatlas-cli`` 当作这些 Python API 的等价迁移。
CLI 用户需要手动卸载旧包，再安装独立的 ``qatlas-cli``。

本仓库已移除 Commitizen 配置，不再用根目录的 ``cz bump`` 管理发布。
常规服务端发版仍以 ``VERSION`` 与 ``v<version>`` 为准；旧包迁移不修改
``VERSION``（迁移时仍为 ``0.34.0``），也不参与客户端/服务端版本协商。
最后一次 PyPI 发布的隔离与验收要求见 :doc:`release`。

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
