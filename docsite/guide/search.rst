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
Crossref / catalog / 本地语义检索），并可选用 LLM 对结果生成一段学术总结。
其中本地语义检索由另一个独立微服务 qatlas-rag 提供（GPU 上的 bge-m3
稠密 + 稀疏混合检索，RRF 融合后重排）：qatlas-search 把它作为 fan-out
的一个 backend 调用，qatlasd 在论文收录或转换完成时向 qatlas-rag 推送
索引构建任务。

- **网页**：搜索页打开「agentic 搜索」开关（微服务未上线时开关禁用）；
  结果上方显示 agent 总结，右上角显示「今日用量 x/限额」。
- **CLI**：安装插件包后 ``qatlas search "query"`` 自动可用（entry-point
  发现，无需改主仓库配置）；安装 qatlas-rag 包后 ``qatlas rag "query"``
  可直接查询语义检索服务；两个插件未安装时都会提示安装方法。
- **API**：``POST /api/search/agentic``，body 为 Search Entry，响应在普通
  搜索的 ``results``/``candidates`` 之上增加 ``conclusion`` 与 ``usage``。

**用量与限额**：agentic 搜索按用户计量（次数 + LLM tokens），每日限额默认
10000 次，超限返回 429。限额按 用户自定义上限 → 所属套餐（free/pro/max）→
服务端默认 解析。管理员可在网页管理后台查看每用户用量（含按单价换算的
cost）、编辑套餐限额、为单个用户指定套餐或自定义上限。日期按 UTC 日界。

微服务部署见 :doc:`插件化架构与路线图 </dev/plugins>`——它以 docker 微服务
形式接入（``docker compose --profile search up -d``），只在内网可达，
全部终端用户流量由 qatlasd 代理并计量。

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
