搜索
====

搜索是 **多范式、可插拔** 的：输入一个标准 Search Entry，引擎并发地询问多个
搜索提供方（provider），归并去重后返回论文 ID 列表。

内置 Provider
-------------

.. list-table::
   :header-rows: 1
   :widths: 20 80

   * - Provider
     - 数据来源
   * - ``catalog``
     - QuantumAtlas 自有数据库（标题 / 作者 / 标识匹配）
   * - ``arxiv``
     - arXiv 官方 API
   * - ``openalex``
     - OpenAlex 学术图谱

语义向量检索不在内置 provider 之列：它由独立的 qatlas-rag 微服务提供，
经 qatlas-search 接入（见下文「Agentic 搜索」一节）。

渠道原理与用量限制
------------------

各渠道的接入方式与上游限制如下。引擎层面另有统一约束：每个 provider
调用超时 15 秒，``max_results`` 默认 10、上限 50；单个渠道失败只记入
响应的 ``errors``，不影响其他渠道的结果。

- **catalog**：本地 PostgreSQL 论文注册表（需 ``QATLAS_POSTGRES_DSN``）。
  DOI / arXiv ID 精确命中（评分 1.0），否则按标题分词 ``ILIKE`` 匹配
  （评分 0.5，按时间倒序）。纯内部 SQL 查询，无外部配额。
- **arxiv**：arXiv 官方 Atom API（``export.arxiv.org/api/query``），无需
  API key。查询优先级：``id_list``（arXiv ID 直达）> ``doi:"…"`` >
  ``ti:"…"`` > 全文 ``all:``。arXiv 对匿名客户端限流激进，qatlasd 以
  固定 UA 标识自己；搜索调用单次超时 10 秒、不自动重试（被限流时记为
  该渠道失败）。论文抓取（非搜索路径）另有独立的令牌桶限流：
  ``arxiv_fetch_rps`` 默认 0.33（约每 3 秒一次，遵循 arXiv 官方建议）、
  burst 2、最多重试 3 次并遵守上游 ``Retry-After``。
- **openalex**：OpenAlex works API（``api.openalex.org/works``），无需
  API key。DOI 查询走 ``filter=doi:``，其余走 ``search=``，单页至多 25
  条。配置 ``paper_access.openalex_mailto``（``QATLAS_OPENALEX_MAILTO``）
  后进入 polite pool——每 IP 约 10 req/s，远稳于匿名池，生产环境建议
  必配。仅含 arXiv ID 的条目在 OpenAlex 无对应查询方式，直接返回空。

agentic 搜索另有一套服务端计量：按用户统计调用次数与 LLM tokens，
每日限额默认 10000 次，超限返回 429，详见下文「Agentic 搜索」一节。

Search Entry 格式
-----------------

.. code-block:: json

   {
     "text":             "自由文本查询",
     "title":            "论文标题（精确或片段）",
     "doi":              "10.xxxx/xxxxx",
     "arxiv_id":         "quant-ph/0001001 或 2401.12345",
     "max_results":      10,
     "required_phrases": ["必须出现的短语"]
   }

结果归并
--------

归并优先级：**DOI > arXiv ID > 标题**。

- 带权威身份（DOI / arXiv ID）的命中进入 ``results``，并触发惰性收录
  （见 :doc:`concepts` 与响应中的 ``created`` 字段）；
- 仅标题匹配的命中进入 ``candidates``，仅供参考，不入库。

调用示例见 :doc:`api`。

Agentic 搜索（独立微服务）
--------------------------

除内置 provider 外，QuantumAtlas 还支持 **agentic 搜索**：由独立仓库
`qatlas-search <https://github.com/Agony5757/qatlas-search>`_ （私有）提供的
搜索微服务，在服务端做多源检索（arXiv / OpenAlex / Semantic Scholar /
Crossref / PubMed / Europe PMC / DBLP / DOAJ / OpenAIRE / catalog，以及需
个人或服务端 API key 的 CORE / NASA ADS / Springer / IEEE Xplore / Scopus
和网页引擎 Wikipedia / SearXNG / Tavily / Exa / Serper / Brave / Kagi），
并可选用 LLM 对结果生成一段学术总结。
其中本地语义检索由另一个独立微服务 qatlas-rag 提供（GPU 上的 bge-m3
稠密 + 稀疏混合检索，RRF 融合后重排）：qatlas-search 把它作为 fan-out
的一个 backend 调用，qatlasd 在论文收录或转换完成时向 qatlas-rag 推送
索引构建任务。

- **网页**：搜索页打开「agentic 搜索」开关（微服务未上线时开关禁用）；
  结果上方显示 agent 总结，右上角显示「今日用量 x/限额」。
- **CLI**：安装插件包后 ``qatlas search "query"`` 自动可用（entry-point
  发现，无需改主仓库配置）；安装 qatlas-rag 包后 ``qatlas rag "query"``
  可直接查询语义检索服务；两个插件未安装时都会提示安装方法。
- **API**：``POST /api/search/agentic``，body 为 Search Entry（可选
  ``sources`` 指定 backend 列表），响应在普通搜索的
  ``results``/``candidates`` 之上增加 ``conclusion`` 与 ``usage``。

**用量与限额**：agentic 搜索按用户计量（次数 + LLM tokens），每日限额默认
10000 次，超限返回 429。限额按 用户自定义上限 → 所属套餐（free/pro/max）→
服务端默认 解析。管理员可在网页管理后台查看每用户用量（含按单价换算的
cost）、编辑套餐限额、为单个用户指定套餐或自定义上限。日期按 UTC 日界。

微服务部署见 :doc:`插件化架构与路线图 </dev/plugins>`——它以 docker 微服务
形式接入（``docker compose --profile search up -d``），只在内网可达，
全部终端用户流量由 qatlasd 代理并计量。

逐平台搜索与个人 API keys
--------------------------

关闭「agentic 搜索」时，搜索走 **multi 模式**：qatlasd 把查询代理给
qatlas-search（``POST /api/search/multi``），每个被选中的 backend 返回
自己的**原始**命中列表（保持该平台自身排序，不做跨源合并与融合评分），
前端用 Tab 按平台分开展示，失败的平台在该 Tab 内显示错误。

- **后端选择**：搜索页提供按「学术源 / 网页引擎」分组的复选框
  （``GET /api/search/backends`` 提供 catalog：静态表 + 微服务实时可用性
  + 当前用户已配置的 key）。需要 key 但未配置的后端复选框禁用，并给出
  指向 dashboard 的「去配置」链接；选择持久化在浏览器 localStorage。
- **个人 API keys**：dashboard 的「搜索 API keys」面板可为支持个人 key
  的 backend（Semantic Scholar / OpenAlex / PubMed 加速 key，以及 CORE /
  NASA ADS / Springer / IEEE / Scopus / Tavily / Exa / Serper / Brave /
  Kagi 等必填 key）保存自己的密钥。key 以 AES-256-GCM 加密存储在
  PocketBase（加密密钥由服务端 system PAT 派生，无额外配置项），列表只
  显示末四位掩码；搜索时代理解密注入，qatlas-search 不持久化任何请求级
  key。服务端 YAML 里的同名 key 仍作为兜底（优先级：用户 key > 服务端）。
- **API**：``POST /api/search/multi``（papers:read scope），body
  ``{"text", "max_results", "sources": [backend]}``，响应
  ``{"results": {backend: [hit]}, "errors": {backend: msg}, "remote": true}``；
  key 的 CRUD 在 ``GET/PUT/DELETE /api/me/search-keys``（仅浏览器会话）。

Robust Downloader（多范式下载入库）
------------------------------------

``Robust Downloader`` 页面（``/$lang/downloader``，侧边栏入口）把一批论文
标识（DOI / arXiv id / 论文链接，每行一条，单次最多 50 条）提交给服务端的
多范式下载模块（``internal/downloader``，注册为第三个 builtin 插件
``downloader``）：

.. code-block:: text

   POST /api/downloader/fetch   {"items": ["10.1038/...", "arXiv:2401.12345"]}
   GET  /api/downloader/jobs    任务快照（状态/策略/完整尝试轨迹）

策略阶梯（逐层尝试，全部候选先过统一验证管线——``%PDF-`` 魔数、
``%%EOF`` 尾部、大小上下限、bot 墙/付费墙/错误页分类）：

1. **arxiv** — arXiv 直下（版本固定、全局限速）；
2. **twin-resolve** — OpenAlex 把 DOI 解析到 arXiv 孪生预印本后走 1；
3. **oa-apis** — Europe PMC（含绕过 PMC PoW 的 ``?pdf=render``）、
   Unpaywall、OpenAlex、Semantic Scholar 的 OA PDF 直链；仓库**落地页**
   （HAL、高校机构库等）会被继续挖掘出真实 PDF 链接（绿色 OA）；
4. **pattern** — 出版社 URL 构造表（Springer/Wiley/T&F/Sage/ACS/ACM/
   Frontiers/PLOS/eLife/bioRxiv，按 DOI 前缀）；
5. **landing** — doi.org 落地页 ``citation_pdf_url`` 挖掘（IEEE 文档页
   额外解析 stamp.jsp 中间页）；
6. **agent**（兜底，默认关）— LLM 阅读落地页 HTML 提取候选链接，
   ``downloader.agent.backend: openai``（OpenAI 兼容端点）或
   ``claude``（本机 headless claude CLI，Read/Glob/Grep 沙箱）。

成功的 PDF 带溯源元数据（``downloader:<策略>``、来源 URL、sha256）写入
对象存储并触发 MinerU 转换；每次尝试的策略轨迹记录在任务快照与
``paper_acquisition_events`` 审计表。抓取带 cookie jar、浏览器式请求头、
按主机限速；``downloader.respect_robots`` 默认关闭（按需授权获取不属于
爬虫，且多家出版社用 ``Disallow: *`` 反 AI 爬虫会误伤合法下载）。

**验收工具**：``qatlasd downloader probe [ids | --random N --search "..."]``
从 OpenAlex 随机抽样跑全链路并输出逐篇结果与失败分类（机器人墙 /
付费墙 / 404 / 无候选…），任一失败退出码为 1，便于自动化压测。残余
失败均为环境权限类终态：IEEE 网关 202、Wiley/AIP 的 Cloudflare 挑战、
无 OA 副本且无订阅的 Nature/Elsevier 内容——这类论文请用浏览器下载后
经 ``/api/papers/{id}/upload-pdf`` 手动上传。配置
``downloader.s2_api_key``（免费 Semantic Scholar key）可显著稳定第 3 层
的召回（CVPR/ICCV 等 IEEE 会议论文多靠 S2 找到 arXiv 副本）。

本地 agentic 后端（claude CLI）
-------------------------------

除远程微服务外，agentic 搜索还可以切换为 **本地后端**：qatlasd 进程内调用
本机已登录的 `claude <https://claude.com/claude-code>`_ CLI（headless
``claude -p``）对引擎 fan-out 的原始命中做精炼/去重/排序，并产出学术总结。
两种后端走同一端点、同一响应形状（``results``/``candidates``/``conclusion``/
``usage``/``errors``），由配置切换：

.. code-block:: yaml

   search:
     agentic:
       backend: local        # 默认 remote（qatlas-search 微服务）
       local:
         claude_bin: claude  # 需在本机完成 OAuth 登录
         model: ""           # 空 = claude 默认模型
         sandbox_dir: ""     # 空 = <paths.data_dir>/agentic
         timeout: 5m         # 单次 claude 调用超时
         retention: 24h      # 沙箱审计保留时长
         max_budget_usd: 0   # 0 = 不传 --max-budget-usd
         prompt_template: "" # 空 = 内嵌模板

**沙箱机制**：每个请求在 ``sandbox_dir`` 下建一个独立目录
（``<时间戳>-<随机后缀>/``），写入 ``query.json``（规范化后的查询）、
``results.json``（fan-out 原始命中）、``prompt.txt``（渲染后的 prompt）、
``run/``（claude 的工作目录，唯一可写区）、``response.json``（标准化输出）
与 ``meta.json``（耗时/exit code/token 用量/错误）。后台 janitor 每分钟
清扫超过 ``retention`` 的沙箱，启动时先兜底清扫一次崩溃残留。沙箱是
文件系统级隔离（cwd + 工具白名单 Read/Write/Glob/Grep），不是容器级隔离。

``agent: false`` 的请求同样可用：本地后端只做引擎 fan-out，不调 claude，
``conclusion`` 为 null、``errors`` 携带各 provider 失败表，响应形状与
``agent: true`` 完全一致。
