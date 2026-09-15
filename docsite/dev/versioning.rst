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
     - 唯一取自 Git release tag ``vX.Y.Z[-rc.N]`` ，去掉前导 ``v`` 派生版本；
       不维护根版本文件
     - 人工选 SemVer → 待发源码检查/提交/review → 在已审核 SHA 上创建
       annotated tag → 只推该 tag；release.yml + GoReleaser 生成正文与产物
   * - ``qatlas-cli``
     - 由 qatlas-cli 独立仓库管理
     - 按该仓库的发布流程发 PyPI（``uv tool install qatlas-cli`` ）

旧 PyPI 包 ``quantum-atlas 0.21.0`` 的最终迁移版已发布；此后不再发布
旧包版本。它仅含元数据与迁移说明，没有运行时依赖、``qatlas`` Python
命名空间或 console script，也不依赖或自动转发到 ``qatlas-cli`` 。
旧 ``qatlas.paper_assets`` 与 ``qatlas.parser.doi`` 帮助库已退役，不能把
安装 ``qatlas-cli`` 当作这些 Python API 的等价迁移。CLI 用户需要手动
卸载旧包，再安装独立的 ``qatlas-cli`` 。

固定历史 tag
`quantum-atlas-v0.21.0 <https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0>`_
保留发布时的元数据、检查器、测试及 workflow 供审计，不再是 main 的
发布入口。main 保留 ``PYPI_README.md`` 作为退役说明入口，但不再有根
``pyproject.toml`` / ``uv.lock`` / ``pixi.lock`` 或旧包构建后端。
Go 工具链门槛唯一取自 ``go.mod`` ，Node 唯一取自 ``web/.node-version`` ；
Python 只用于独立 Sphinx 入口脚本和 CI 标准库辅助脚本。
常规服务端正式版本仅从 ``v<version>`` Git tag 派生，没有 PyPI 产物；
不需要版本文件 bump commit，也不为版本专门修改 Go 源码/版本字段。
旧包退役没有发布服务端或容器，当时 GitHub Latest 保持为 ``v0.34.0`` ，
也不参与客户端/服务端版本协商；本次流程调整不构成新发行。
发布历史说明见 :doc:`release`。

运行时版本与 UI 绑定
--------------------

运行时版本依次使用 GoReleaser 默认注入的 ``main.version`` 、
``debug.ReadBuildInfo().Main.Version`` 、``dev`` 。Go 1.24+（含本仓使用的
Go 1.26.2）在源码 checkout 构建时也可能提供 tag、伪版本及 ``+dirty`` ，
不只带版本的 ``go install`` 才有 build info；缺少可用版本元数据才回退 ``dev`` 。
仅去掉开头的 ``v`` ，保留预发布与有意义的构建标记。
解析在 ``--version`` 之前完成，不加载配置、不访问数据库或网络；CLI、
health、server-info 与响应头共享同一个值。发布镜像复用 GoReleaser 的
预编译二进制；本地 ``Dockerfile`` 源码构建仍注入 ``main.version`` 。
Go、Node 或文档工具的版本都不是服务端运行版本。
GoReleaser snapshot 使用生成的快照版本，不是正式发布版本，
不能以快照运行结果代替正式 tag 与运行版本的精确核验。

新服务端 tag 推荐标准 ``vX.Y.Z[-rc.N]`` ，例如 ``v0.35.0-rc.1`` ，不用 Python
风格 ``v0.35.0a1`` 或 ``+build`` 标签。移除自定义 gate 后，发布采用 GoReleaser
原生 Git/SemVer 校验与 preflight（默认只警告）；不表示任意 tag 都能用于
Go 模块、UI 包或 Docker，这些消费者仍有各自约束。上述普通 Go 构建的版本解析不变。
不自动 bump 版本，本次流程调整不创建新发行，也不重发 ``v0.34.0`` 。
``prerelease: auto`` 保留，GitHub Latest 由 GoReleaser/平台默认处理；GHCR
``latest`` 仅在非 prerelease、非 snapshot 的镜像发布时更新，不等待外部 smoke，
也不按版本大小排序。

``release.yml`` 的 prep 固定 checkout 事件的 ``github.sha`` ，再将 ``HEAD``
解析为提交 SHA，供 checks 与发布共用，不重新解析浮动 tag 来选择源码。
正式 UI 包的版本从传给 ``go.yml`` 的精确 ``release_tag`` 去掉前导 ``v`` 派生，
并检查 tag 所指提交 SHA = source SHA = ``HEAD`` 。
GoReleaser 使用绑定到触发 tag 的 ``GORELEASER_CURRENT_TAG`` ，不从最近旧 tag
或同 SHA 的其他 tag 猜测版本。普通 branch/PR CI 仍执行完整 UI 打包与恢复验证，
但只用 ``0.0.0-ci.g<完整Git提交SHA>`` 临时标识，不创建 tag、版本文件，也不发布。
本地无正式 tag 时同样使用该验证标识，见 :doc:`development`；
``uibundle -version`` 始终需要显式 SemVer，不给它伪造一个旧 Release 版本。

普通 Go 构建、测试及精确 tag 的 ``go install`` 均不要求 UI 产物，也不运行 npm。
已发布精确 tag 的源码安装首次 ``serve`` 自动获取 **自身精确版本** 的 Release
UI，校验 SHA256、包内版本与完整性后按版本缓存，后续启动同样验证缓存。
``dev`` 、空版本及 Go 伪版本没有可靠的对应 UI Release，会报错而不是下载 latest；
开发者需完整构建 Sphinx 两站及 npm UI 后使用 ``-tags embedui`` ，见
:doc:`development`。GoReleaser tar.gz 中的程序已经内嵌与独立 UI ZIP 相同的
资源，首次启动无需联网补资源。

GoReleaser OSS v2.18.1 的 ``git.ignore_tags`` 精确忽略
``quantum-atlas-v0.21.0`` （不是 glob），不会修改该历史 tag。
Go tag 可被发现不等于 Release UI 已可下载：缺附件会明确报错，Go 模块安装
不等待 GitHub/GHCR 发布完成。新 Release 内部先 draft、全部附件上传成功后自动公开，
不等待外部 smoke。默认不覆盖同名附件，对 immutable Release 的实际发布会硬拒绝。
后续采用新 workflow 的版本在 GoReleaser 发布后生成 GitHub 签名构建证明，
可用 ``gh attestation verify`` 核验；已发布的 ``v0.35.0-rc.1`` 不追溯补签。
默认版本化 ``qatlasd_<version>_checksums.txt`` 不变，源码安装的 UI 下载器也依赖它；
SHA256 完整性不是来源签名，来源证明也不保证程序无 bug。
GitHub/GHCR/证明登记非原子，证明失败时 Release 可能已公开：先分别核对 Release、
附件、镜像、标签与证明状态，不直接重跑覆盖、移动已推 tag 或擅自升级生产。

兼容协议
--------

**qatlasd 与 qatlas-cli 的 ``(major, minor)`` 相同即兼容**，patch 位
允许随意漂移。兼容性修复只 bump patch，例如：

- qatlasd ``0.22.4`` ↔ qatlas-cli ``0.22.3`` ：兼容（推荐配对，各自升到
  同 ``x.y`` 线的最新 patch 即可）；
- qatlasd ``0.23.0`` ↔ qatlas-cli ``0.22.x`` ：不兼容（``minor`` 不同）。

运行时握手：qatlas-cli 每个请求带 ``X-Qatlas-Client-Version`` ，
qatlasd 每个响应带 ``X-Qatlas-Server-Version`` 。明确写操作先探测
``GET /api/server/info`` （同一凭证、超时与 TLS，无业务载荷），通过后再
发业务请求：

- ``(major, minor)`` 一致：静默通过（patch 差异不影响兼容）；
- 服务端更新且为写：探测阶段硬失败（exit code 4），提示
  ``uv tool upgrade qatlas-cli`` ，**不发送写请求**。探测失败（非 2xx；
  404 除外）同样不发送。写请求已经发出后，版本变化只警告，不表示写入被
  拒绝或未发出；
- 服务端更新且为读：stderr 警告一次后继续；
- 客户端更新：stderr 警告一次（提示升级 qatlasd）后继续；
- 无版本头或无法解析（0.8.0 前）：跳过协商。info 接口 404 视为老服务端，
  仍允许写。

主仓不再维护根手写变更日志。现行和后续服务端 Release 正文由 GoReleaser
按 previous..current tags 的 Git commit 标题与 SHA 自动生成；
``dist/CHANGELOG.md`` 是构建输出，不是受版本控制的输入。
Conventional Commits 不自动推导版本；维护者按兼容性人工决定 SemVer。
升级时查看 `GitHub Releases <https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases>`_
和对应版本的迁移文档；默认正文不含 commit footer，breaking change 要在
标题中标记、在功能/部署文档中说明。历史 tags 与已发布 Release 保持不变，
完整发版流程见 :doc:`release`。qatlas-cli 的版本与发布记录由其独立仓库管理。
