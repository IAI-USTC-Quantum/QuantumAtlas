管理后台
========

管理员账号为 GitHub 白名单中的管理员名单（当前为 ``Agony5757``）。
管理员用 GitHub 登录后可访问 ``/zh/admin``：

- **数据库内省**：浏览 PostgreSQL 注册表的全部表结构
  （表、列、索引、约束），只读，基于 ``pg_catalog`` 内省；
- **MinerU 调度**：查看调度器状态（是否在运行、今日已转换数、
  下次运行时间），并可手动触发一轮转换。

对应的 API 端点（只接受管理员浏览器会话，PAT 一律 403）：

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
     - ``/api/admin/mineru/status``
     - MinerU 调度器快照
   * - POST
     - ``/api/admin/mineru/run``
     - 手动触发一轮转换（已在运行时返回冲突）
