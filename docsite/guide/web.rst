Web 界面
========

界面路径带语言前缀（``/`` 会按浏览器语言重定向到 ``/zh`` 或 ``/en``，
下表省略前缀，以 ``/zh`` 为例）：

.. list-table::
   :header-rows: 1
   :widths: 18 30 52

   * - 页面
     - 路径
     - 说明
   * - 首页
     - ``/zh``
     - 概览与「已转换论文」
   * - 搜索
     - ``/zh/papers/search``
     - 多范式论文搜索：可切换 agentic / 逐平台模式，复选框选 backend，
       Tab 分平台展示结果（见 :doc:`search`）
   * - Robust Downloader
     - ``/zh/downloader``
     - 粘贴一批 DOI / arXiv ID / 论文链接，一键提交多范式下载入库
       （见 :doc:`search`）
   * - 论文列表
     - ``/zh/papers``
     - 注册表浏览（默认显示已转换 Markdown 的论文）
   * - 论文详情
     - ``/zh/papers/{paper_id}``
     - 元数据、PDF / Markdown / 图片访问、下载进度时间线
   * - 用户面板
     - ``/zh/dashboard``
     - 个人 profile、账号绑定（GitHub/Gitea）、今日搜索用量、
       PAT 快捷入口、搜索 API keys 管理
   * - PAT 管理
     - ``/zh/pat``
     - 创建 / 吊销个人访问令牌（scope 粒度选择）
   * - 管理后台
     - ``/zh/admin``
     - 数据库 / MinerU / 下载失败 / 下载节点 / 用户 / 用量 / 插件（见 :doc:`admin`）
   * - 下载工作节点
     - ``/zh/admin/downloader-workers``
     - 注册凭据、人工审批、排空 / 恢复 / 撤销，容量与心跳监控（仅管理员）
   * - 资产浏览
     - ``/zh/admin/assets``
     - 搜索论文 → 预览 / 下载 PDF 与 Markdown / 预签名 URL
       （仅管理员，见 :doc:`admin`）

搜索页功能
----------

- **agentic 开关**：开启时走 LLM 搜索（带学术总结与每日配额）；
  关闭时走逐平台搜索。
- **backend 复选框**：按「学术源 / 网页引擎」分组；需 key 但未配置的
  后端显示为禁用并附「去配置」链接（跳转 dashboard）。
- **Tab 分平台结果**：关闭 agentic 后每个平台一个 Tab，各平台按自身
  排序展示原始命中，失败平台在 Tab 内显示错误。
- **选择持久化**：backend 选择保存在浏览器 localStorage。

Robust Downloader 页
--------------------

- **输入**：多行文本框，每行一条 DOI / arXiv ID / 论文链接（单次最多
  50 条）；前端实时解析预览（类型徽章：doi / arXiv / url / 无效）。
- **本地优先**：先用服务器自身网络下载；启用 outbound fleet 后，受挑战
  阻断或策略耗尽才按需委托管理员批准的 worker。每个 worker 使用自己的
  网络 / 浏览器并主动连接服务器，不需要入站端口。
- **本地与待执行请求**：``GET /api/downloader/jobs`` 活跃时 2 秒 / 静止时
  30 秒轮询，显示状态、获胜策略及可展开的 URL / 错误 / 耗时轨迹。
  启用持久化 admission 时，该快照还合并最多 512 条待执行请求，包含等待
  本地槽位的请求；内存详细轨迹可能随重启清空，不能据此判断持久化任务丢失。
- **远程节点任务进度**：fleet 启用后单独显示，每 5 秒查询
  ``GET /api/downloader/remote-jobs``。持久化任务可在服务器重启后继续查看，
  展示任务 / 节点 ID、更新时间、错误及 ``queued``（排队）、``running``
  （下载或上传）、``staged``（归档中）、``done``、``failed``。
  最多显示最近更新的 500 条，不是完整分页历史；不展示管理员节点详情或凭据。
- **并行与重试**：不同论文受全局 / 节点容量限制并行；同一论文有限次顺序
  更换 worker，受任务截止时间约束，不会无限广播或重试。
- **完成含义**：远程 done 表示上传 PDF 已写入对象存储并完成 registry
  资产登记，持久化收据允许 worker 核对后删除副本。staged / HTTP 成功不能
  视为归档确认；未确认结果在保留期内继续查收据或重试。MinerU / 索引由后续
  持久化 outbox 处理，不阻塞收据，Markdown 就绪需另看转换状态。
- **论文链接**：本地任务中已入库的论文标题点击直达论文详情页。

新 fleet 的验收应在服务器页面 / :doc:`api` 观察上述流程；
``qatlasd downloader probe`` 不测试它，``--proxy`` 只属于 LEGACY 协议。
原理见 :doc:`search`，命令边界见 :doc:`cli`。

下载工作节点管理页
------------------

``/zh/admin/downloader-workers`` 需要管理员的人类浏览器会话，PAT 或
worker 密钥不能用于审批。管理员创建有时限、一次性的 enrollment token，
worker 注册后保持 pending，核实身份并 approve 后才可领取任务。
页面每 10 秒刷新心跳、容量、浏览器状态、磁盘 / 暂存、任务和错误；
健康数据仅反映最近上报，不是服务器对 worker 的实时拨入测试。

计划停机用 **drain** 停止新任务但允许已有结果上传 / 确认；**enable**
恢复领取。**reject / revoke** 为终态，重新加入需新身份与新注册凭据，
不能当作暂停开关。详细操作与临时文件保留注意事项见 :doc:`admin`。

Dashboard 面板
--------------

- **Profile**：头像、姓名、GitHub/Gitea 绑定状态、角色徽章。
- **账号绑定**：GitHub / Gitea OAuth 卡片，绑定后可双渠道登录。
- **今日用量**：agentic 搜索次数 + LLM tokens（进度条）。
- **PAT**：快捷链接到 /pat 管理页。
- **搜索 API keys**：按 backend 配置个人 key（IEEE / Scopus / Tavily
  等），保存后搜索页对应复选框自动解锁。

界面访问需要登录：GitHub（仅白名单账号）或自建 Gitea 实例账号
（实例内全部账号可登录，首次登录遇用户名 / 邮箱冲突时按页面提示
匹配或绑定已有账号，详见「账号绑定」面板）。本文档站点（``/doc``）
无需登录。
