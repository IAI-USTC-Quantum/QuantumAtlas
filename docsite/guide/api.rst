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
