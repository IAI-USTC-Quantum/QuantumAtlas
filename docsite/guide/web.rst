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
     - 数据库 / MinerU / 下载失败 / 用户 / 用量 / 插件（见 :doc:`admin`）
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
- **提交后**：任务列表实时轮询（活跃任务 2 秒 / 静止 30 秒），每项显示
  状态（排队 / 下载中 / 完成 / 失败）、获胜策略、可展开的策略轨迹
  （每层尝试的 URL / 错误 / 耗时）。
- **论文链接**：已入库的论文标题点击直达论文详情页。

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
