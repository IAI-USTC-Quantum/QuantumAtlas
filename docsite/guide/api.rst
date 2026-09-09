API 参考
========

除标注 **公开** 的端点外，所有 API 都需要认证。普通论文 API 使用会话或
PAT（见本文 `认证：Personal Access Token`_）；管理员操作与 worker 协议
使用下文各自标明的认证方式，不能相互替代。

基础 URL 使用您实际连接的服务器 HTTPS origin（下文省略）。

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
      与 ``created`` （本次是否触发了新收录），以及托管摘要 ``has_md`` /
      ``has_pdf`` / ``status`` （registry 不可用时省略）；
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
    阶梯。需 ``papers:write``，成功返回 200；请求体缺失 / 超过 50 条返回
    400，下载器或注册表不可用返回 503。逐项错误仍在 200 响应的 ``items``
    中，``enqueued`` 表示接受入队，不表示 PDF 已归档。启用 remote fleet
    时使用持久化 admission，先执行本地策略，再按需委托批准的 worker。

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
    返回 200，需 ``papers:read``。本地任务状态 / 当前策略 / 尝试轨迹 /
    计数器快照；启用持久化 admission 时还合并最多 512 条待执行请求，
    包括等待本地执行槽位的请求。内存中的详细轨迹可能随重启丢失，不能用
    该列表代替持久化远程进度。前端活跃时 2 秒、静止时 30 秒轮询。
    下载器未配置时返回空快照。

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

``GET /api/downloader/remote-jobs``
    返回 200，需 ``papers:read``。响应 ``{"enabled": true, "jobs": [...]}``；
    fleet 关闭时仍返回 200，内容为 ``{"enabled": false, "jobs": []}``。
    每项含 ``id``、``worker_id``、``state``、``identifier``、``error``、
    ``updated_at``。这是 PostgreSQL 持久化远程任务快照，最多返回最近
    更新的 500 条，不是分页历史；不返回节点拓扑、健康详情或凭据。

    - ``queued``：等待可用 worker；不同论文可并行，同一论文有限次顺序换节点。
    - ``running``：下载或上传中。
    - ``staged``：PDF 已暂存，等待对象存储与 registry 归档完成。
    - ``done``：对象存储写入与资产登记均已完成；不代表 MinerU 转换完成。
    - ``failed``：任务终止，可查看 ``error``；某次 worker 尝试失败并不一定
      表示整个任务终止，剩余预算允许时任务重新排队。

本地优先、每节点网络 / 浏览器、归档收据与清理语义见 :doc:`search`；
界面操作见 :doc:`web`。``qatlasd downloader probe`` 不接入新 fleet，
请通过这里的提交 / 进度 API 与管理员节点页面验收。

Outbound worker 协议（机器认证）
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

下表端点均在 **qatlasd**，由 worker 主动请求；无需 worker 入站监听。
注册请求体为 ``id``、``name``、``enrollment_token``、``secret``，worker
在首次请求前持久化随机身份与独立 secret。注册后的请求使用
``Authorization: Bearer <worker secret>``，不是用户 PAT、管理员会话或
enrollment token。pending 节点除同身份注册重试外只能查询自身 status。

.. list-table::
   :header-rows: 1
   :widths: 10 43 10 37

   * - 方法
     - 端点
     - 成功状态
     - 认证与用途
   * - POST
     - ``/api/downloader/workers/v2/register``
     - 200
     - 首次使用请求体中的一次性 enrollment 凭据；初始返回 pending，非自动批准
   * - GET
     - ``/api/downloader/workers/v2/status``
     - 200
     - worker Bearer；查询自身节点状态，pending 可用
   * - POST
     - ``/api/downloader/workers/v2/heartbeat``
     - 200
     - approved / draining worker；上报容量、浏览器、磁盘并续租
   * - POST
     - ``/api/downloader/workers/v2/claim``
     - 200
     - approved worker 领取任务；空 ``attempts`` 正常，draining 返回空列表
   * - POST
     - ``/api/downloader/workers/v2/report``
     - 200
     - approved / draining worker；报告自身 attempt 的失败并返回收据
   * - PUT
     - ``/api/downloader/workers/v2/upload/{attempt}``
     - 200
     - approved / draining worker；上传自身 attempt 的原始 PDF，返回收据
   * - GET
     - ``/api/downloader/workers/v2/receipts/{attempt}``
     - 200
     - approved / draining worker；非破坏性读取自身 attempt 收据

除 register 外上表每一项均需 worker Bearer。无效 / 缺失凭据返回 401，
pending 越权或 rejected / revoked 身份返回 403，不存在或不属于该 worker
的 attempt 返回 404，过期 / 冲突的 attempt 可返回 409，请求超时可返回
408，fleet 关闭或暂不可用返回 503；无效请求返回 400。

上传使用 ``Content-Type: application/pdf``，必需头为 ``X-PDF-SHA256`` 与
``X-PDF-Size``，PDF 上限 100 MiB（也受 assignment 的 ``max_pdf_bytes``
限制）。可选 ``X-Source-URL``、``X-Download-Strategy`` 与
``X-Download-Result``（base64url 无填充的 JSON 溯源元数据，头上限 16 KiB）。
这不是用户的 multipart ``upload-pdf`` 接口。

收据含 ``task_id``、``attempt_id``、``state`` 及可用时的 ``sha256``、
``size``、``error``；状态为 ``running`` / ``staged`` / ``done`` /
``failed`` / ``expired``。**HTTP 200 不等于 done**：暂存完成但归档未完成
时不能删除本地 PDF。worker 核对 done 收据中的任务、attempt、摘要、大小
均匹配后才删除已确认结果；不确定响应应查收据 / 重试，保留期限到期清理
是独立的失败处理。MinerU / 索引由归档后的持久化 outbox 处理，不阻塞收据。

LEGACY ``downloader.proxy.*``、代理自身的 ``/v1/jobs`` / ``/v1/files/*``
以及 CLI ``--proxy`` 不属于此协议；不能与已启用的 remote fleet 混用。
审批与 enrollment 管理见 :doc:`admin`。

个人搜索 API keys
-----------------

``GET /api/me/search-keys``
    列出当前用户已配置的个人搜索 API key。
    仅浏览器会话。响应示例（hint 为末四位掩码）：

    .. code-block:: json

       {"enabled": true, "keys": [{"backend": "ieee", "hint": "••••ab3f", "updated_at": "..."}]}

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
        - POST /api/search、/api/search/multi；GET /api/papers/、/api/downloader/jobs、/api/downloader/remote-jobs
        - 搜索与读取
      * - ``papers:write``
        - POST /api/downloader/fetch、论文 upload 端点
        - 上传 / 下载触发（隐含 read）
      * - ``plugins:read``
        - GET /api/v1/plugins
        - 插件状态查询

命令行场景还支持 OAuth Device Flow（``/api/oauth/device/*``），适合无浏览器的
终端登录。

管理端点
--------

``/api/admin/*`` 只接受管理员的浏览器会话，PAT 一律 403；
``whoami`` 是例外，任意已登录用户会话可查询自己的管理员标志。
worker secret 不能用于管理员操作。

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

Downloader fleet 管理
~~~~~~~~~~~~~~~~~~~~~

以下均需 **管理员的人类浏览器会话**，不是 PAT / worker secret。
未登录会话返回 401，非管理员会话或 PAT 返回 403。

.. list-table::
   :header-rows: 1
   :widths: 10 45 10 35

   * - 方法
     - 端点
     - 成功状态
     - 说明
   * - GET
     - ``/api/admin/downloader/workers``
     - 200
     - ``workers`` 与 ``jobs`` 快照，各最多 500 条；不含 worker secret
   * - POST
     - ``/api/admin/downloader/enrollment``
     - 201
     - 返回 ``token`` / ``expires_at``，默认 15 分钟有效的一次性注册凭据
   * - POST
     - ``/api/admin/downloader/workers/{id}/{action}``
     - 200
     - action 为 ``approve`` / ``reject`` / ``drain`` / ``enable`` / ``revoke``；返回更新节点

fleet 未启用返回 503，节点不存在返回 404，未知 action 返回 400；
已映射的冲突 / 禁止操作分别返回 409 / 403，其他操作错误返回 500
（含当前实现的部分非法状态转换），应刷新节点状态后检查原因。
批准、排空和撤销的操作语义见 :doc:`admin`。

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

前端入口： :code:`/zh/admin/assets` （侧边栏「资产浏览」，仅管理员可见）。
