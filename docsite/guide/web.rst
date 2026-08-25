Web 界面
========

界面路径带语言前缀（``/`` 会按浏览器语言重定向到 ``/zh`` 或 ``/en``，
下表省略前缀，以 ``/zh`` 为例）：

.. list-table::
   :header-rows: 1
   :widths: 18 25 57

   * - 页面
     - 路径
     - 说明
   * - 首页
     - ``/zh``
     - 概览与「已转换论文」
   * - 搜索
     - ``/zh/papers/search``
     - 多范式论文搜索
   * - 论文列表
     - ``/zh/papers``
     - 注册表浏览（默认显示已转换 Markdown 的论文）
   * - 论文详情
     - ``/zh/papers/{paper_id}``
     - 元数据、PDF / Markdown / 图片访问
   * - PAT 管理
     - ``/zh/pat``
     - 创建 / 吊销个人访问令牌
   * - 管理后台
     - ``/zh/admin``
     - 数据库表结构浏览、MinerU 调度状态（仅管理员，见 :doc:`admin`）

界面访问需要 GitHub 登录（仅白名单账号）。本文档站点（``/doc``）无需登录。
