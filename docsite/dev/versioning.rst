版本与兼容策略
==============

QuantumAtlas 由两个独立演进的组件构成：服务端 ``qatlasd``（本仓库）与命令行
客户端 ``qatlas-cli``（独立仓库 `IAI-USTC-Quantum/qatlas-cli
<https://github.com/IAI-USTC-Quantum/qatlas-cli>`_）。两者有**各自独立的版本号**，
靠一条明确的兼容契约协作，而不是共享一个版本。

版本来源
--------

.. list-table::
   :header-rows: 1
   :widths: 25 40 35

   * - 组件
     - 版本唯一来源
     - 发布方式
   * - ``qatlasd``（本仓库）
     - 仓库根目录的 ``VERSION`` 文件 + 形如 ``v<version>`` 的 git tag
       （release.yml 的 prep job 会强校验二者一致，不一致直接失败）
     - 手动编辑 ``VERSION`` → commit → ``git tag v<version>`` → push tag，
       release.yml 自动出二进制 / 镜像 / GitHub Release
   * - ``qatlas-cli``
     - 它自己仓库的 ``pyproject.toml``（commitizen 管理）
     - ``cz bump`` 打 tag 后由该仓 CI 发 PyPI（``uv tool install qatlas-cli``）
   * - ``quantum-atlas`` PyPI 包（本仓库，rag embed worker 等 Python 代码）
     - 本仓库 ``pyproject.toml``（commitizen 管理）
     - 随 release.yml 的 pypi-publish job 发布，与 qatlasd 版本**不挂钩**，
       独立节奏演进

兼容契约
--------

**qatlasd 与 qatlas-cli 的 ``(major, minor)`` 相同即兼容**，patch 位随意漂移。
兼容性修复只 bump patch，例如：

- qatlasd ``0.22.4`` ↔ qatlas-cli ``0.22.3``：兼容（推荐配对，各自升到同
  ``x.y`` 线的最新 patch 即可）；
- qatlasd ``0.23.0`` ↔ qatlas-cli ``0.22.x``：不兼容（``minor`` 不同）。

运行时握手：qatlas-cli 每个请求带 ``X-Qatlas-Client-Version`` 头，qatlasd 每个
响应带 ``X-Qatlas-Server-Version`` 头。客户端按上面的契约比较：

- ``(major, minor)`` 一致：静默通过（patch 差异不算事）；
- server 更新且为写操作：硬失败（exit code 4），提示
  ``uv tool upgrade qatlas-cli``；
- server 更新且为读操作：stderr 警告一次，继续执行；
- client 更新：stderr 警告一次（提示运维方升级 qatlasd），继续执行；
- 响应没有版本头（0.8.0 之前的老服务端）：跳过协商。

主仓 ``CHANGELOG.md`` 的 Unreleased 段记录 qatlasd 侧变更；qatlas-cli 的变更
记录在它自己仓库的 ``CHANGELOG.md``。
