API 参考
========

除标注 **公开** 的端点外，所有 API 都需要认证（见本文 `认证：Personal Access Token`_）。

基础 URL 为 :code:`https://qatlas.hfnl.app.chenzhaoyun.com`（下文省略）。

搜索
----

``POST /api/search``
    经典多 provider 搜索（本地 catalog / arXiv / OpenAlex），归并去重后
    返回论文 ID 列表。需 ``papers:read``。

    .. code-block:: bash

       curl -X POST /api/search \
         -H "Authorization: Bearer <PAT>" \
         -H "Content-Type: application/json" \
         -d '{"arxiv_id": "quant-ph/0001001", "max_results": 5}'

    响应字段：

    - :code:`results`：权威身份命中，每项含 :code:`paper_id`、命中信息 :code:`hit`
      与 ``created``（本次是否触发了新收录），以及托管摘要 ``has_md`` /
      ``has_pdf`` / ``status``（registry 不可用时省略）；
    - ``candidates``：仅标题命中的候选，不入库。

    请求体 ``text`` / ``title`` / ``arxiv_id`` / ``doi`` 全空时返回 400；
    带 ``arxiv_id`` / ``doi`` 的请求会把身份透传给 qatlas-search 微服务做
    精确查询（arXiv ``id_list`` / OpenAlex DOI filter / Semantic Scholar
    paper 端点），不再发出空查询。

    请求体格式见 :doc:`search`。

``POST /api/search/multi``
    逐平台搜索：每个被选 backend 返回自己的原始命中列表（该平台自身
    排序，**不做跨源合并与融合评分**），供前端分平台 Tab 展示。需
    ``papers:read``。

    .. code-block:: bash

       curl -X POST /api/search/multi \
         -H "Authorization: Bearer <PAT>" \
         -H "Content-Type: application/json" \
         -d '{"text": "quantum error correction", "max_results": 10,
              "sources": ["arxiv", "openalex", "semantic_scholar", "wikipedia"]}'

    响应::

       {
         "results": {
           "arxiv":     [{"title": "...", "abstract": "...", "authors": [...], ...}],
           "openalex":  [...],
           "semantic_scholar": [...],
           "wikipedia": [...]
         },
         "errors": {},
         "remote": true
       }

    每个 hit 含 ``title / abstract / authors / year / doi / arxiv_id /
    url / venue / citations / source / raw_rank / raw_score``。

    失败的 backend 在 ``errors`` 里给出原因（如 ``"missing API key"``），
    不影响其他平台的结果。

``POST /api/search/agentic``
    LLM 搜索（带学术总结与每日配额）。请求体同 Search Entry，可加
    ``"agent": true``（默认 true）与 ``"sources": [...]``。空 entry
    （``text`` / ``title`` / ``arxiv_id`` / ``doi`` 全空）在计量之前就
    返回 400；身份条目（``arxiv_id`` / ``doi``）同样透传给微服务做
    精确查询。需 ``papers:read`` 且用户级凭据（系统 PAT 返回 403）。

    响应在普通搜索之上增加：

    - ``conclusion``：LLM 生成的学术总结（一段话）；
    - ``usage``：``{"today": 3, "limit": 10000, "llm_tokens": 450}``；
    - ``ranking``：``{"source": "llm"|"default", "weights": {...}}``
      （排序意图审计块，仅 ranking:"auto" 时有效）。

    429 响应体含当前用量：``{"detail": "...", "usage": {"today": 10000,
    "limit": 10000}}``。

``GET /api/search/backends``
    backend 目录（搜索页复选框数据源）：静态表 ∪ qatlas-search 实时
    可用性 ∪ 当前用户已配置的个人 key 状态。仅浏览器会话。

    响应::

       {
         "remote": true,
         "keys_enabled": true,
         "backends": [
           {"name": "arxiv",  "label": "arXiv", "category": "academic",
            "requires_key": false, "user_key": false,
            "server_ready": true, "key_configured": false, "selectable": true},
           {"name": "ieee",   "label": "IEEE Xplore", "category": "academic",
            "requires_key": true, "user_key": true,
            "server_ready": false, "key_configured": false, "selectable": false},
           ...
         ]
       }

    ``selectable = server_ready || (user_key && key_configured)`` ——
    需 key 但未配置的后端复选框禁用，前端展示「去配置」链接。

Robust Downloader
-----------------

``POST /api/downloader/fetch``
    提交一批论文标识（DOI / arXiv id / 论文链接，每行一条，单次最多
    50 条），逐条解析 → resolve-or-mint 入注册表 → 入队多范式下载
    阶梯。需 ``papers:write``。

    .. code-block:: bash

       curl -X POST /api/downloader/fetch \
         -H "Authorization: Bearer <PAT>" \
         -H "Content-Type: application/json" \
         -d '{"items": ["10.1038/s41586-024-07806-9", "arXiv:2401.12345",
                        "https://doi.org/10.1103/PhysRevA.109.012601"]}'

    响应::

       {
         "items": [
           {"input": "10.1038/...", "kind": "doi",
            "paper_id": "qa_01H...", "created": true},
           {"input": "arXiv:2401.12345", "kind": "arxiv",
            "paper_id": "qa_01H...", "created": false},
           {"input": "not a paper", "kind": "invalid", "error": "..."}
         ],
         "enqueued": 2
       }

``GET /api/downloader/jobs``
    任务快照：每篇论文的状态 / 当前策略 / 完整尝试轨迹 / 计数器。
    需 ``papers:read``。前端 Robust Downloader 页面 2 秒轮询此端点。

    响应::

       {
         "jobs": [{
           "paper_id": "qa_01H...", "input": "10.1038/...", "kind": "doi",
           "state": "done", "phase": "pdf_ready", "active": false,
           "strategy": "oa:unpaywall",
           "error": null,
           "trace": [{"strategy": "oa:unpaywall", "url": "https://...",
                      "error": null, "ms": 1234}],
           "events": [{"phase": "downloading_pdf", "state": "running", "at": "..."}],
           "submitted_at": "...", "updated_at": "..."
         }],
         "counters": {"queued": 0, "in_flight": 0, "succeeded": 2, "failed": 1}
       }

完整的六层策略阶梯与配置项见 :doc:`search`。

个人搜索 API keys
-----------------

``GET /api/me/search-keys``
    列出当前用户已配置的个人搜索 API key。
    仅浏览器会话。响应 ``{"enabled": true, "keys": [{"backend": "ieee",
    "hint": "••••ab3f", "updated_at": "..."}]}``（hint 为末四位掩码）。

``PUT /api/me/search-keys/{backend}``
    保存 / 更新一个 backend 的个人 key。body ``{"key": "..."}``。
    仅浏览器会话。backend 必须在 backend 目录中有用户 key 槽位。

``DELETE /api/me/search-keys/{backend}``
    删除一个 backend 的个人 key。仅浏览器会话。

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
     - 分页列出论文（过滤参数 ``has_md`` / ``status`` / ``q`` /
       ``arxiv_id`` / ``doi`` / ``paper_id``，后三者为精确身份过滤——
       ``arxiv_id`` 自动去版本后缀，``doi`` 容忍 URL 前缀；另有
       ``page`` / ``per_page`` / ``sort``）
   * - GET
     - ``/api/papers/{id}``
     - 论文详情；``{id}`` 可以是 ``paper_id``、arXiv ID（新旧式，
       可不带 ``vN``）或 DOI。合法但未收录的标识符返回 404，提示用
       ``GET /api/papers/lookup`` 解析元数据
   * - GET
     - ``/api/papers/lookup?ids=``
     - 批量（≤200 条）解析 ``arxiv:`` / ``doi:`` / ``openalex:`` 引用；
       每项含 ``hosted`` 与 ``has_md``：前者表示是否已收录，后者表示
       默认资产是否已有 markdown——批量核对「有没有 markdown」的官方入口
   * - GET
     - ``/api/papers/{id}/pdf``
     - **已停用（410 Gone）**——PDF 分发设计性禁用，改用 markdown 端点；
       PDF 仍作为内部资产服务转换与贡献者 lease
   * - GET
     - ``/api/papers/{id}/pdf/status``
     - PDF 就绪探测（debug 端点；PDF 抓取仍是 markdown 转换管线的
       内部阶段）
   * - GET
     - ``/api/papers/{id}/markdown``
     - 获取 MinerU 转换的 Markdown（支持 ``?format=link|bytes|stream``）；
       ``{id}`` 也接受 ``qa_`` paper_id（服务端解析成最高 arXiv 版本，
       DOI-only 论文走 DOI 管线）
   * - GET
     - ``/api/papers/{id}/markdown/status``
     - Markdown 转换进度（LRO 轮询）
   * - GET
     - ``/api/papers/{id}/images``
     - 列出该论文 Markdown 引用的图片文件
   * - GET
     - ``/api/papers/{id}/images/zip``
     - 打包下载全部图片（支持 ``?format=link`` 预签名 URL）
   * - POST
     - ``/api/papers/{id}/upload-pdf``
     - 上传本地 PDF（multipart form，``?overwrite=true`` 覆盖）
   * - POST
     - ``/api/papers/{id}/upload-mineru``
     - 上传本地 MinerU 转换结果
   * - POST
     - ``/api/papers/{id}/mineru-claim``
     - 认领一篇论文的 MinerU 转换权（贡献者工作流）
   * - POST / DELETE
     - ``/api/papers/{id}/mineru-lease``
     - 获取 / 释放 MinerU 租约

Dashboard / Me
--------------

.. list-table::
   :header-rows: 1
   :widths: 12 45 43

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/me``
     - 当前用户 profile（仅浏览器会话）
   * - GET
     - ``/api/me/usage``
     - 今日 agentic 搜索用量（仅浏览器会话）

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
     - 服务器版本与能力信息（``capabilities``：匿名只见布尔位——
       ``paper_access`` / ``markdown_delivery`` / ``pdf_delivery``
       （恒 false）/ ``agentic_search`` / ``mineru.*``；认证调用者
       额外见 ``mineru.daily_cap`` / ``mineru.converted_today``）
   * - GET
     - ``/api/pat/scopes`` **公开**
     - PAT 权限范围词汇表
   * - POST / GET / DELETE
     - ``/api/pat``
     - PAT 创建 / 列表 / 吊销（需浏览器会话）
   * - GET
     - ``/api/v1/plugins``
     - 插件注册表摘要（需 ``plugins:read``）

认证：Personal Access Token
---------------------------

程序化访问使用 PAT：

#. 在 PAT 管理页面（``/zh/pat``）用 GitHub 登录后创建 PAT，**明文只显示一次**；
#. 请求头携带 ``Authorization: Bearer <PAT>``；
#. 权限范围（scope）：

   .. list-table::
      :header-rows: 1
      :widths: 25 20 55

      * - Scope
        - 端点
        - 说明
      * - ``papers:read``
        - GET /api/search、/api/papers/、/api/downloader/jobs
        - 搜索与读取
      * - ``papers:write``
        - POST /api/search/multi、/api/downloader/fetch、upload
        - 上传 / 下载触发（隐含 read）
      * - ``plugins:read``
        - GET /api/v1/plugins
        - 插件状态查询

命令行场景还支持 OAuth Device Flow（``/api/oauth/device/*``），适合无浏览器的
终端登录。

管理端点
--------

``/api/admin/*`` 只接受管理员的浏览器会话，PAT 一律 403。

.. list-table::
   :header-rows: 1
   :widths: 12 48 40

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/admin/whoami``
     - 当前登录身份与角色
   * - GET
     - ``/api/admin/db/schema``
     - 数据库表结构内省（只读）
   * - GET
     - ``/api/admin/db/tables/{name}/rows``
     - 原始行浏览（分页）
   * - POST
     - ``/api/admin/mineru/run``
     - 手动触发一轮转换
   * - GET
     - ``/api/admin/mineru/status``
     - MinerU 调度器快照
   * - GET
     - ``/api/admin/acquisition/failures``
     - 下载失败论文列表（含 DOI 链接与原因，admin 端）
   * - GET / PATCH
     - ``/api/admin/users``
     - 用户列表 / 角色管理
   * - GET
     - ``/api/admin/usage``
     - 每用户 agentic 搜索用量
   * - GET / PUT
     - ``/api/admin/plans`` / ``/api/admin/quotas/{user}``
     - 套餐与配额管理
   * - GET
     - ``/api/admin/plugins``
     - 插件列表（含 admin 面板代理）
   * - GET / PUT
     - ``/api/admin/plugins/{id}/manifest`` / ``.../config``
     - 插件配置读写

Admin Asset Browser（资产浏览）
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

管理员可直接浏览、预览、下载论文的 PDF 和 Markdown，并生成预签名
S3 URL 供浏览器直连：

.. list-table::
   :header-rows: 1
   :widths: 12 55 33

   * - 方法
     - 端点
     - 说明
   * - GET
     - ``/api/admin/assets/{paper_id}``
     - 资产清单（object key / size / sha256 / content_type）
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}``
     - 单资产详情（``?presign=true&ttl=1h`` 附带预签名 URL）
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/download``
     - 代理流式下载（Content-Disposition: attachment）
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/inline``
     - 代理流式预览（Content-Disposition: inline）
   * - GET
     - ``/api/admin/assets/{paper_id}/{kind}/url``
     - 预签名 URL JSON（``?ttl=1h``，限 1m–24h）
   * - GET
     - ``/api/admin/assets/batch?paper_ids=a,b,c``
     - 批量资产清单（最多 50 篇）
   * - GET
     - ``/api/admin/assets/batch/download``
     - 批量 ZIP 下载（最多 20 篇，流式归档）
   * - GET
     - ``/api/admin/assets/search?q=...``
     - 按标题 / DOI / arXiv ID 搜索有资产的论文

``{kind}`` 取 ``pdf`` 或 ``markdown``。

前端入口：:code:`/zh/admin/assets`（侧边栏「资产浏览」，仅管理员可见）。
