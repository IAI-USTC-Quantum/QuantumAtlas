API 参考
========

除标注 **公开** 的端点外，所有 API 都需要认证（见本文 `认证：Personal Access Token`_）。

搜索
----

``POST /api/search``

.. code-block:: bash

   curl -X POST https://qatlas.hfnl.app.chenzhaoyun.com/api/search \
     -H "Authorization: Bearer <PAT>" \
     -H "Content-Type: application/json" \
     -d '{"arxiv_id": "quant-ph/0001001", "max_results": 5}'

响应字段：

- ``results``：权威身份命中，每项含 ``paper_id``、命中信息 ``hit``
  （arXiv ID / DOI / 标题 / 作者 / 年份 / 来源 / 分数）与 ``created``
  （本次是否触发了新收录）；
- ``candidates``：仅标题命中的候选，不入库。

请求体格式见 :doc:`search`。

``POST /api/search/multi``
    逐平台搜索：每个被选 backend 返回自己的原始命中列表（该平台自身
    排序，**不做跨源合并与融合评分**），供前端分平台 Tab 展示。请求体
    ``{"text": "...", "max_results": 10, "sources": ["arxiv", "ieee"]}``，
    响应 ``{"results": {backend: [hit, ...]}, "errors": {backend: msg}}``。
    需 ``papers:read``。

``POST /api/search/agentic``
    LLM 搜索（带总结与配额），请求体同上（可加 ``"agent": true``），
    响应增加 ``conclusion`` 与 ``usage``。需 ``papers:read`` 且用户级
    凭据（计量）。

``GET /api/search/backends``
    backend 目录（搜索页复选框数据源）：静态表 ∪ qatlas-search 实时
    可用性 ∪ 当前用户已配置的个人 key 状态。``selectable`` 为
    ``server_ready || (user_key && key_configured)``——需 key 但未
    配置的后端复选框禁用。仅浏览器会话。

``GET/PUT/DELETE /api/me/search-keys``
    个人搜索 API key 管理（dashboard 面板数据源）。PUT 校验 backend
    是否有用户 key 槽位；列表只回末四位掩码。仅浏览器会话。

Robust Downloader
~~~~~~~~~~~~~~~~~

``POST /api/downloader/fetch``
    提交一批论文标识（DOI / arXiv id / 论文链接，每行一条，单次最多
    50 条），逐条解析 → resolve-or-mint 入注册表 → 入队多范式下载
    阶梯。需 ``papers:write``。

``GET /api/downloader/jobs``
    任务快照：每篇论文的状态 / 当前策略 / 完整尝试轨迹 / 计数器。
    需 ``papers:read``。

完整策略阶梯与配置见 :doc:`search` 的 :doc:`search <search>` 一节。

论文与资产
----------

.. list-table::
   :header-rows: 1
   :widths: 12 40 48

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/papers``
     - 分页列出论文（支持 ``has_md`` 等过滤）
   * - GET
     - ``/api/papers/{id}``
     - 论文详情；``{id}`` 可以是 ``paper_id``、arXiv ID 或 DOI
   * - GET
     - ``/api/papers/{id}/pdf``
     - 获取 PDF（预签名下载地址）
   * - GET
     - ``/api/papers/{id}/markdown``
     - 获取 MinerU 转换的 Markdown
   * - GET
     - ``/api/papers/{id}/images``
     - 列出该论文 Markdown 引用的图片文件

其他
----

.. list-table::
   :header-rows: 1
   :widths: 18 32 50

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/health`` **公开**
     - 健康检查（匿名返回精简状态）
   * - GET
     - ``/api/server/info`` **公开**
     - 服务器版本与能力信息
   * - GET
     - ``/api/pat/scopes`` **公开**
     - PAT 权限范围词汇表
   * - POST / GET / DELETE
     - ``/api/pat``
     - PAT 创建 / 列表 / 吊销（需浏览器会话）

认证：Personal Access Token
---------------------------

程序化访问使用 PAT：

#. 在 PAT 管理页面（``/zh/pat``）用 GitHub 登录后创建 PAT，**明文只显示一次**；
#. 请求头携带 ``Authorization: Bearer <PAT>``；
#. 权限范围（scope）：``papers:read``\（搜索与读取）、
   ``papers:write``\（上传 / 删除资产）。

命令行场景还支持 OAuth Device Flow（``/api/oauth/device/*``），适合无浏览器的
终端登录。

管理端点
--------

``/api/admin/*``\（数据库内省、MinerU 调度）只接受管理员的浏览器会话，
PAT 一律拒绝，详见 :doc:`admin`。
