管理后台
========

管理员账号为 GitHub 白名单中的管理员名单。管理员用 GitHub 登录后可
访问 ``/zh/admin``。

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

API 端点
--------

对应的 API 端点只接受管理员浏览器会话，PAT 一律 403：

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
