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
   * - ``qdrant``
     - 向量语义检索（需配置 RAG embedding 服务后启用）

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
`qatlas-search <https://github.com/Agony5757/qatlas-search>`_（私有）提供的
搜索微服务，在服务端做多源检索（arXiv / OpenAlex / Semantic Scholar /
Crossref / catalog），并可选用 LLM 对结果生成一段学术总结。

- **网页**：搜索页打开「agentic 搜索」开关（微服务未上线时开关禁用）；
  结果上方显示 agent 总结，右上角显示「今日用量 x/限额」。
- **CLI**：安装插件包后 ``qatlas search "query"`` 自动可用（entry-point
  发现，无需改主仓库配置）；未安装时会提示安装方法。
- **API**：``POST /api/search/agentic``，body 为 Search Entry，响应在普通
  搜索的 ``results``/``candidates`` 之上增加 ``conclusion`` 与 ``usage``。

**用量与限额**：agentic 搜索按用户计量（次数 + LLM tokens），每日限额默认
10000 次，超限返回 429。限额按 用户自定义上限 → 所属套餐（free/pro/max）→
服务端默认 解析。管理员可在网页管理后台查看每用户用量（含按单价换算的
cost）、编辑套餐限额、为单个用户指定套餐或自定义上限。日期按 UTC 日界。

微服务部署见 :doc:`插件化架构与路线图 </dev/plugins>`——它以 docker 微服务
形式接入（``docker compose --profile search up -d``），只在内网可达，
全部终端用户流量由 qatlasd 代理并计量。
