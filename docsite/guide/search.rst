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

``text`` / ``title`` / ``arxiv_id`` / ``doi`` 至少要有一个——全空时
``POST /api/search`` 与 ``/api/search/agentic`` 都直接返回 400，不再发出
空查询。仅带 ``arxiv_id`` / ``doi`` 的条目会把身份透传给 qatlas-search
微服务做精确查询（arXiv ``id_list``、OpenAlex DOI filter、Semantic
Scholar paper 端点）。

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
  发现，无需改主仓库配置）；``--direct`` 本地自带 key 直跑，``survey``
  规则化综述、``multi`` 逐平台原始结果——详细用法与标志表见
  :doc:`cli` 的「论文搜索（qatlas search）」一节；安装 qatlas-rag 包后
  ``qatlas rag "query"`` 可直接查询语义检索服务；两个插件未安装时都会
  提示安装方法。
- **API**：``POST /api/search/agentic``，body 为 Search Entry（可选
  ``sources`` 指定 backend 列表），响应在普通搜索的
  ``results``/``candidates`` 之上增加 ``conclusion`` 与 ``usage``。

**用量与限额**：agentic 搜索按用户计量（次数 + LLM tokens），每日限额默认
10000 次，超限返回 429。限额按 用户自定义上限 → 所属套餐（free/pro/max）→
服务端默认 解析。管理员可在网页管理后台查看每用户用量（含按单价换算的
cost）、编辑套餐限额、为单个用户指定套餐或自定义上限。日期按 UTC 日界。

微服务由管理员配置，终端用户通过 qatlasd 代理访问并计量；
使用入口见 :doc:`web` 与 :doc:`api`。

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
- **API**：``POST /api/search/multi`` （papers:read scope），请求体
  含 text / max_results / sources，响应按 backend 分组返回原始命中
  列表；key 的 CRUD 在 ``GET/PUT/DELETE /api/me/search-keys`` （仅
  浏览器会话）。完整请求/响应格式见 :doc:`api <api>`。

Robust Downloader（多范式下载入库）
------------------------------------

``Robust Downloader`` 页面（``/$lang/downloader``，侧边栏入口）把一批论文
标识（DOI / arXiv id / 论文链接，每行一条，单次最多 50 条）提交给服务端的
多范式下载模块（``internal/downloader``，注册为第三个 builtin 插件
``downloader``）：

.. code-block:: text

   POST /api/downloader/fetch        {"items": ["10.1038/...", "arXiv:2401.12345"]}
   GET  /api/downloader/jobs         本地进度与持久化待执行请求快照
   GET  /api/downloader/remote-jobs  持久化远程任务进度

下载 **local-first（本地优先）**：先尝试服务器自身网络可用的下载策略；
启用 outbound worker fleet 后，本地受挑战阻断或策略耗尽时才委托远程任务。
挑战触发的委托可先于本地 browser / agent 兜底，不是每篇论文都发往远程。
远程等待释放本地下载槽位；不同论文可并行，同一论文按有限尝试次数与
截止时间依次换 worker，不向所有节点无限广播。详见下文「Outbound workers」。

本地策略阶梯（逐层尝试，全部候选先过统一验证管线——``%PDF-`` 魔数、
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
6. agent（兜底，默认关）——LLM 阅读落地页 HTML 提取候选链接。配置
   项 ``downloader.agent.backend`` 设为 ``openai`` （OpenAI 兼容端点）
   或 ``claude`` （本机 headless claude CLI）。

成功的 PDF 带溯源元数据（``downloader:<策略>``、来源 URL、sha256）写入
对象存储并登记资产；MinerU 转换属于后续处理，PDF 已归档不等于转换已完成。
本地策略轨迹记录在任务快照与 ``paper_acquisition_events`` 审计表，
远程任务另有持久化进度。抓取带 cookie jar、浏览器式请求头、按主机限速；
``downloader.respect_robots`` 默认关闭，运维人员仍须确认下载权限及站点条款。

**诊断工具**：:command:`qatlasd downloader probe` 支持位置参数（DOI/arXiv）、
:code:`--random N`（OpenAlex 随机抽样）、:code:`--search`（领域过滤），
输出本地下载阶梯的逐篇结果与失败分类，任一失败退出码为 1。
它 **不测试新的 outbound fleet**；``--proxy`` 仅测试 LEGACY 代理。
新 fleet 应通过服务器 :doc:`web` / :doc:`api` 提交并观察持久化进度，
管理员同时检查节点状态（见 :doc:`admin`）。失败可能来自网络、权限、
超时或服务故障，不能一概视为权限终态；有权获取但自动下载失败的论文
可人工下载后经 ``/api/papers/{id}/upload-pdf`` 上传。配置
``downloader.s2_api_key`` 可改善 Semantic Scholar OA 候选查询的稳定性。

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


Outbound workers（主动连接的下载节点）
----------------------------------------

协调器内置于 qatlasd。管理员可批准多台 ``downloaderworker``，每台使用
**自己的网络出口与浏览器环境** 获取其有权访问的论文；worker 使用共享
下载策略，但不递归委托其他 worker / proxy，也不启用 agent 执行。
worker 主动通过 HTTPS 注册、发心跳、领取租约、上传 PDF 并查询收据；
服务器 **不拨入 worker**，无需开放 worker 或 CDP 入站端口。

管理员启用 ``downloader.remote.enabled``，并配置 PostgreSQL 与对象存储后，
已接收请求和远程任务进入持久化队列。全局容量、单节点容量、尝试次数与
任务 / worker 截止时间共同限制并发和故障转移。单篇失败可换尚未尝试的
worker，直到成功或预算耗尽；无效标识、取消等终态不会无限重试。
每台 worker 需要独立持久化卷，保存身份、临时 PDF 与恢复状态；不要复制
身份给并行节点。注册与人工审批步骤见 :doc:`admin`。

归档与删除顺序：

#. worker 下载、校验并持久化本地 PDF，再主动上传；
#. qatlasd 将上传流写入有配额的暂存区，独立校验大小、SHA-256、PDF 头尾；
#. **对象存储写入与 registry 资产登记都成功** 后，生成持久化 ``done`` 收据；
#. worker 核对收据的 task ID、attempt ID、SHA-256 与大小后删除已确认副本。

HTTP 成功或 ``staged`` （归档中）本身都不是归档确认。上传响应丢失时，
worker 可重复查询非破坏性收据或重试传输，不因不确定响应立即删除文件。
临时结果仍有保留期限（worker 默认从 ready 起 24 小时），到期可清理并报告
失败；不能保证服务器长期不可用时仍完成归档。磁盘 / 配额不足会暂停新任务，
不以驱逐未过期结果腾空间。

MinerU 与索引通过归档后的持久化 outbox 重试，**不阻塞 done 收据**；
PDF 归档完成不表示 Markdown 已完成，转换进度需另外查看。
``GET /api/downloader/remote-jobs`` 提供重启后仍可查询的远程任务，
``GET /api/downloader/jobs`` 在本地进度之外合并最多 512 条持久化待执行请求，
包含等待本地槽位的请求。远程快照最多 500 条；两者都不是完整分页历史。
具体权限和状态见 :doc:`api`。

浏览器不是完整的网络安全沙箱。每台 worker / browser 应隔离运行并限制
出站访问，阻止访问宿主机服务、内部敏感网络和云元数据；勿用 host networking、
挂载宿主服务 socket 或公开 CDP。节点审批并不赋予额外的出版社访问权。

LEGACY Downloader Proxy
-----------------------

旧 ``downloaderproxy`` 为 **LEGACY**：qatlasd 主动访问代理的
``POST /v1/jobs``、``GET /v1/jobs/{id}``、``GET /v1/files/{token}``，
以共享 Bearer token 认证。旧配置 ``downloader.proxy.url / token / timeout``
与 :doc:`cli` 中 ``--proxy`` / ``--proxy-token`` 只属于该协议，
不是 outbound worker 的注册方式，也没有新协议的持久化 done 收据保证。
``downloader.remote.enabled: true`` 与非空旧 ``downloader.proxy.url``
不能同时配置；实现拒绝混用，不会自动迁移现有部署。

浏览器 lane（真实 Chromium 兜底）
----------------------------------

当出版社的 bot 墙（Cloudflare / IEEE AWS WAF）挡住所有 plain-HTTP
策略时，downloader 会通过 CDP 驱动一个真实 Chromium 浏览器——事件驱
动挖掘落地页链接（citation_pdf_url / 内联 JSON pdfUrl / IEEE stamp
中间页），被动捕获 PDF 响应，并以人类节奏（1.5–3s 随机间隔）运行。

**本地启用** （qatlasd 所在机器跑一个 Chromium）：

.. code-block:: yaml

   downloader:
     browser:
       cdp_url: http://127.0.0.1:9222   # Chromium --remote-debugging-port
       timeout: 45s

outbound ``downloaderworker`` 容器由自身 runner 管理 Chromium，使用该
worker 的网络环境；外部 CDP 仅限受信任且隔离的私有端点。
LEGACY ``downloaderproxy`` 容器也自带 Chromium，但使用旧代理协议。

人工补救通道
------------

所有自动下载失败的论文在管理后台的「下载失败列表」中列出，
提供两个操作：

1. **DOI 链接**：点击直达出版社页面人工下载 PDF；
2. **上传 PDF**：选本地文件 → 自动配对 DOI → 走贡献通道入库
   （OpenAlex 元数据校验 + resolve-or-mint + 触发 MinerU 转换）。

对应的 CLI 命令为 ``qatlas contrib pdf <id> --pdf file.pdf``。

Admin 资产浏览
--------------

管理员可在 ``/zh/admin/assets`` 搜索、预览、下载论文的 PDF 与
Markdown，并生成预签名 S3 URL。详见 :doc:`admin`。


Rule-based survey search
------------------------

``POST /api/search/survey`` forwards a bounded query plan to qatlas-search.
The payload contains ``goal``, optional ``queries`` (at most 6), ``rules``
(authors, venues, year_from/year_to, min_citations, sort), ``sources``,
``max_results`` and ``agentic``. User backend keys are injected on the server;
callers cannot supply ``api_keys``. Filtered identity hits are anchored in the
paper registry. With ``agentic=true``, a user-bound credential and the existing
agentic daily quota are required; upstream transport failure refunds the slot.

The returned coverage explicitly says ``post_filter_bounded`` and
``exhaustive=false``. This endpoint filters retrieved metadata; it does not
claim exhaustive author pagination or global citation ranking. Upgrade
qatlas-search before qatlasd to enable the endpoint.
