管理后台
========

管理员账号需匹配服务器配置的 GitHub 或 Gitea 管理员名单，使用对应账号
登录后的 **人类浏览器会话** 访问 ``/zh/admin``。用户 PAT（包括管理员
自己的 PAT）、系统 PAT 和 worker secret 都不能代替管理员会话。

功能面板
--------

**数据库内省**
    浏览 PostgreSQL 注册表的全部表结构（表、列、索引、约束），
    只读，基于 ``pg_catalog`` 内省。

**MinerU 调度**
    查看调度器状态（是否在运行、今日已转换数、下次运行时间），
    并可手动触发一轮转换。

**下载失败列表（Acquisition Failures）**
    列出自动下载失败的论文（含 DOI/arXiv 链接、失败阶段、尝试次数、
    最近错误详情）。每行提供：

    - **DOI 链接**：点击直达出版社页面人工下载；
    - **上传 PDF**：选择本地文件后自动配对 DOI 提交，走贡献通道
      （OpenAlex 元数据校验 + resolve-or-mint + 资产登记）。

**用户管理**
    列出全部用户，管理员可切换 ``disabled``（禁用登录）和
    ``is_admin`` 标志。

**用量与套餐**
    查看每用户 agentic 搜索用量（次数 / LLM tokens / 折算 cost），
    编辑套餐限额、为单个用户指定套餐或自定义上限。

**插件管理**
    查看插件列表与实时健康状态，读写插件配置（如 qatlas-search 的
    backend key、agent LLM 端点）。

**资产浏览（Asset Browser）**
    搜索有资产的论文（标题 / DOI / arXiv ID），展开查看每篇论文
    的 PDF 与 Markdown（object key / size / sha256），支持：

    - 在线预览（PDF 内嵌 viewer / Markdown 文本）
    - 代理流式下载
    - 生成预签名 S3 URL（浏览器直连，默认 1h 有效）
    - 复制 S3 object key
    - 批量 ZIP 下载（最多 20 篇）

下载工作节点（Outbound workers）
----------------------------------

入口为 ``/zh/admin/downloader-workers``（英文为
``/en/admin/downloader-workers``）。qatlasd 本地优先下载；需要远程获取时，
由 **管理员批准的多个 worker 主动连接服务器** 领取任务，每台使用自己的
合法网络出口和浏览器。主服务器不拨入 worker，不需发布 worker / CDP
端口。不同论文并行，单篇按尝试次数与截止时间预算顺序故障转移，
不是同时广播到所有节点。完整流程见 :doc:`search`。

注册与审批
~~~~~~~~~~

#. 先确认服务器已启用 ``downloader.remote.enabled``，并配置 PostgreSQL
   与对象存储；不能同时保留非空 LEGACY ``downloader.proxy.url``。
#. 在节点页创建一次性 enrollment token（默认 15 分钟有效），安全交给
   节点运维人员。凭据只供首次注册，不是长期 worker 密钥。
#. worker 配置实际服务器 HTTPS origin 与 enrollment token，挂载自己的
   独立持久化数据卷。它在发注册请求前先保存随机 ID 和独立 secret；
   服务器只存 secret 的哈希。不要将身份 / 数据卷复制给并行节点。
#. 注册后状态为 ``pending``，只能查询自身状态，不能领取任务或上传。
   核实节点身份及授权网络后，在页面确认 **approve**。
#. 正常重连复用持久化身份，无需重新批准；注册响应丢失可用同身份重试，
   不能用已消费 enrollment token 注册另一台机器。每个新节点使用新 token。

节点页每 10 秒刷新，显示节点状态、最近心跳、运行中 / 容量、浏览器状态、
空闲磁盘、暂存用量、最近任务及错误。健康信息来自最近上报，不是主服务器
对节点的实时连通性测试。待批准节点显示不健康不意味着应重建数据卷。
快照中节点与任务各最多 500 条，不是分页审计历史。

排空、恢复与撤销
~~~~~~~~~~~~~~~~

- **drain**：approved → draining；停止新任务分配，仍允许已有任务续租、
  上传和查收据。计划停机应先排空并等待归档完成。
- **enable**：draining → approved，恢复领取任务。
- **reject**：拒绝待审批身份；**revoke**：撤销已注册节点访问权，
  阻断后续认证与执行租约，不能代替平滑排空。已在主服务器暂存的结果
  仍可完成归档恢复。
- rejected / revoked 为终态，不能重新 enable；重新接入需要新 enrollment
  与新身份。处理旧卷前保留未确认 PDF 以供调查，不要因撤销就删除它们。

管理员验收
~~~~~~~~~~

通过 :doc:`web` 的 Robust Downloader 页面或 :doc:`api` 的
``POST /api/downloader/fetch`` 提交获授权的样本，结合节点页确认心跳、
并发容量和有界故障转移。查看 ``GET /api/downloader/remote-jobs`` 的
持久化 ``queued → running → staged → done`` 进度；某些阶段可能在两次
轮询间完成。本地优先命中的样本不一定产生远程任务。
``GET /api/downloader/jobs`` 还合并最多 512 条持久化待执行请求，包含
等待本地槽位的请求；重启可能清除本地详细轨迹，但不会以此清除已持久化队列。

**done 的边界是 PDF 归档**：上传经校验后写入对象存储并完成 registry
资产登记，才创建持久化收据。worker 核对 task / attempt / SHA-256 / size
后删除已确认副本；HTTP 成功或 staged 不能作为删除依据。未确认结果会重试，
但受保留期限限制（worker 默认 ready 后 24 小时）。MinerU / 索引由后续
持久化 outbox 处理，不阻塞 done；Markdown 就绪需另行检查。
``qatlasd downloader probe``（包括 LEGACY ``--proxy``）**不测试新 fleet**。

API 端点
--------

对应的 API 端点只接受管理员浏览器会话，PAT 一律 403；``whoami`` 允许
任意已登录用户会话查询自身管理员标志。worker 机器认证不能用于这些操作。
下载节点管理端点如下（详细协议与错误状态见 :doc:`api`）：

.. list-table::
   :header-rows: 1
   :widths: 10 45 10 35

   * - 方法
     - 端点
     - 成功状态
     - 说明（均需管理员会话）
   * - GET
     - ``/api/admin/downloader/workers``
     - 200
     - 节点与远程任务快照，不返回 worker 密钥
   * - POST
     - ``/api/admin/downloader/enrollment``
     - 201
     - 创建一次性 ``token`` 与 ``expires_at``
   * - POST
     - ``/api/admin/downloader/workers/{id}/{action}``
     - 200
     - ``approve`` / ``reject`` / ``drain`` / ``enable`` / ``revoke``

未登录返回 401，非管理员 / PAT 返回 403，fleet 关闭返回 503；不存在节点
返回 404，未知 action 返回 400，状态变化 / 操作错误需刷新后检查（部分
非法状态转换当前返回 500，不能假定均为 409）。

其他管理端点：

.. list-table::
   :header-rows: 1
   :widths: 15 45 40

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/admin/whoami``
     - 当前登录身份与是否为管理员
   * - GET
     - ``/api/admin/db/schema``
     - 数据库表结构内省（只读）
   * - GET
     - ``/api/admin/db/tables/{name}/rows``
     - 原始行浏览（分页）
   * - GET
     - ``/api/admin/mineru/status``
     - MinerU 调度器快照
   * - POST
     - ``/api/admin/mineru/run``
     - 手动触发一轮转换（已在运行时返回冲突）
   * - GET
     - ``/api/admin/acquisition/failures``
     - 下载失败论文列表
   * - GET / PATCH
     - ``/api/admin/users``
     - 用户列表 / 角色管理
   * - GET
     - ``/api/admin/usage``
     - 每用户 agentic 用量
   * - GET / PUT
     - ``/api/admin/plans`` / ``/api/admin/quotas/{user}``
     - 套餐与配额管理
   * - GET / PUT
     - ``/api/admin/plugins/{id}/manifest`` / ``.../config``
     - 插件配置读写
   * - GET
     - ``/api/admin/assets/{paper_id}``
     - 资产清单（object key / size / sha256）
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/download``
     - 代理流式下载
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/inline``
     - 代理流式预览
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/url``
     - 预签名 URL（``?ttl=1h``）
   * - GET
     - ``/api/admin/assets/search?q=...``
     - 按标题 / DOI / arXiv 搜索有资产的论文
   * - GET
     - ``/api/admin/assets/batch/download``
     - 批量 ZIP 下载（最多 20 篇）

完整 API 参考见 :doc:`api`。
