命令行（qatlas CLI）
======================

安装
----

.. code-block:: bash

   # 从 PyPI 安装为全局工具（推荐；CLI 独立发布为 qatlas-cli 包）
   uv tool install qatlas-cli
   # 或以可编辑模式安装 qatlas-cli 仓库的本地检出（贡献者）
   git clone https://github.com/IAI-USTC-Quantum/qatlas-cli.git
   cd qatlas-cli && uv tool install . --editable --force

   qatlas --help

默认线上实例为 https://qatlas.hfnl.app.chenzhaoyun.com/ （本文档即由它提供）。
首次使用：

.. code-block:: bash

   qatlas config set server_url https://qatlas.hfnl.app.chenzhaoyun.com
   qatlas auth login            # 浏览器批准后 PAT 自动落盘

命令总览
--------

.. list-table::
   :header-rows: 1
   :widths: 18 82

   * - 命令
     - 说明
   * - ``qatlas config``
     - 管理客户端配置文件（``~/.config/qatlas/config.yaml``，YAML 唯一配置源）
   * - ``qatlas auth``
     - 管理各服务器的 PAT / 会话令牌（``login`` 走 OAuth Device Flow）
   * - ``qatlas paper``
     - 从服务器取论文资产：``get markdown`` / ``get images`` /
       ``get metadata`` / ``status`` / ``mineru-lease``；目录检索
       ``list`` / ``lookup``；批量下载 ``fetch`` 与进度 ``jobs``；
       缓存未命中时自动触发服务端抓取与转换（LRO 轮询）。PDF 分发已停用
       （``/pdf`` 恒 410），没有 ``get pdf`` 子命令
   * - ``qatlas contrib``
     - 贡献者工作流：``contrib pdf`` 上传 PDF；``contrib mineru`` 用自己的
       MinerU token 本地转换并回传（队列 / 单篇 / watch 守护模式）
   * - ``qatlas parser``
     - 本地工作区命令：抓取并解析 arXiv 论文（不经过服务器）

别名：``papers`` → ``paper``，``parse`` → ``parser``。

``qatlas --help`` 的输出里带有本文档链接；各子命令 ``--help`` 有完整标志表。

能力总览
--------

qatlas-cli 覆盖用户侧「搜论文 → 拿内容 → 做贡献」的完整工作流：

- **论文搜索**：插件 ``qatlas search``。一条查询 fan-out 到 arXiv /
  OpenAlex / Semantic Scholar / Crossref / PubMed / Europe PMC / DBLP /
  DOAJ / OpenAIRE / 本站 catalog 等学术源，融合成单一排序结果；四种形态
  ——默认 agentic（经 qatlasd，含可选 LLM 结论与用量计量）、``--direct``
  本地自带 key 直跑、``survey`` 规则化综述检索、``multi`` 逐平台原始结果。
  详细用法见下文 `论文搜索（qatlas search）`_。
- **论文获取**：``paper get markdown``（缓存未命中自动触发服务端抓取 +
  MinerU 转换，LRO 轮询到完成）、``paper get images``（插图 zip）、
  ``paper get metadata``（registry 元数据 JSON）；``paper status`` 看转换进度。
- **目录检索与批量下载**：``paper list``（registry 分页检索）、
  ``paper lookup``（≤200 条引用批量解析）、``paper fetch``（向 Robust
  Downloader 提交 ≤50 条 DOI / arXiv ID / 论文链接批量下载）与
  ``paper jobs``（本地 / 远程进度，``--watch`` 轮询）。
- **语义检索**：插件 ``qatlas rag``，直接查询 qatlas-rag 向量检索服务
  （bge-m3 混合检索 + 重排）。
- **论文身份匹配**：插件 ``qatlas match``，按 qatlas-id / DOI / arXiv /
  OpenAlex / URL / 标题判定是否已入库并返回统一 ``qa_…`` id（只在内部
  registry 精确匹配，与 search 的外部检索互补）。详见下文
  `论文匹配（qatlas match）`_。
- **贡献上传**：``contrib pdf`` 上传本地 PDF；``contrib mineru`` 用自己的
  MinerU 配额本地转换并回传（队列 / 单篇 / ``--watch`` 守护）；DOI-only
  论文可 ``--zip`` 上传现成 MinerU 产物。
- **本地解析**：``parser`` 不经服务器、在本地工作区抓取并解析 arXiv 论文。

**PDF 与管理员资产获取**：面向普通用户的 PDF 分发端点已停用
（``GET /api/papers/{id}/pdf`` 恒 410），因此没有 ``paper get pdf`` 子命令。
论文 PDF 由服务端 Robust Downloader 归档进对象存储，获取入口是**管理后台
资产浏览页**：`/<lang>/admin/assets
<https://qatlas.hfnl.app.chenzhaoyun.com/zh/admin/assets>`_。管理员可按
标题 / DOI / arXiv ID 搜索已归档资产，在线预览与下载 PDF / Markdown、生成
预签名 S3 URL，或单篇 / 批量（≤20 篇 ZIP）下载。这些 ``/api/admin/assets/*``
端点只接受**浏览器会话**鉴权（PAT 会被拒绝），所以 CLI 不提供对应子命令。
Markdown 对普通用户始终可用 ``paper get markdown`` 获取。

子命令参考
----------

``qatlas config`` — 配置文件管理
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. list-table::
   :header-rows: 1
   :widths: 22 78

   * - 子命令
     - 说明
   * - ``config path``
     - 打印配置文件路径
   * - ``config set <key> <value>``
     - 写入一个键（敏感值走 stdin 隐藏输入）
   * - ``config unset <key>``
     - 删除一个键
   * - ``config get <key>``
     - 打印单个已解析值（未设置时退出码 1）
   * - ``config show``
     - 打印全部已解析配置

``qatlas auth`` — 凭据管理
~~~~~~~~~~~~~~~~~~~~~~~~~~

.. list-table::
   :header-rows: 1
   :widths: 22 78

   * - 子命令
     - 说明
   * - ``auth login``
     - OAuth Device Flow 登录（RFC 8628）：打印并尝试打开批准页，
       浏览器批准后 PAT 自动落盘 ``~/.config/qatlas/hosts.yml``
   * - ``auth logout``
     - 删除某 host 已存的 PAT
   * - ``auth status``
     - 列出已配置的 host 与 token 形态
   * - ``auth token``
     - 打印已存 token，便于管道给其他工具

``auth login`` 常用标志：

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 标志
     - 说明
   * - ``--server-url, -s URL``
     - 目标服务器（默认取配置 ``server_url``，缺省时交互式询问）
   * - ``--with-token``
     - 从 stdin 读 PAT 明文直接落盘（脚本 / CI 友好，跳过浏览器流，
       明文不进 argv / shell 历史）
   * - ``--no-browser``
     - 不打开本地浏览器，只打印 URL（headless / SSH 场景把 URL 带到
       任意其他设备打开）
   * - ``--scopes`` / ``--expires-days`` / ``--token-name``
     - 浏览器批准表单的预填默认值（批准页内仍可修改）
   * - ``--timeout N``
     - 等待浏览器批准的秒数（默认 600）
   * - ``--insecure``
     - 跳过 TLS 校验（仅自签名开发证书）

``qatlas paper`` — 论文资产获取与下载
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas paper get markdown ID_OR_DOI [--output FILE] [--no-wait]
   qatlas paper get images   ID_OR_DOI [--output FILE]
   qatlas paper get metadata ID_OR_DOI
   qatlas paper status       ID_OR_DOI [--kind markdown]
   qatlas paper list         [--has-md true] [--status …] [-q …] [--json]
   qatlas paper lookup       REF... [--json]
   qatlas paper fetch        ID|DOI|URL... [--file FILE] [--json]
   qatlas paper jobs         [--remote] [--watch] [--json]
   qatlas paper mineru-lease ID_OR_DOI [--ttl-seconds N]
   qatlas paper mineru-lease release ID_OR_DOI CLAIM_ID

- ``--output / -o``：写入文件（默认 stdout）；
- ``--no-wait``：缓存未命中时只触发服务端抓取、不阻塞等待转换完成；
- ``--ttl-seconds``：MinerU 租约时长（服务端有默认值与上限）；
- 服务端 ``paper_access`` 的默认值提示输出在 stderr，用 ``--quiet-notes``
  关闭。

``qatlas paper list`` — 目录检索（``GET /api/papers``，需 ``papers:read``）：
``--has-md true|false``、``--status pending|ready|failed``、``-q``（标题子串）、
``--arxiv-id / --doi / --paper-id``（精确身份过滤）、
``--page / --per-page / --sort created_at|updated_at`` 分页排序；
默认表格输出 paper_id / has_md / status / 标题，``--json`` 输出原始响应。

``qatlas paper lookup`` — 批量引用解析（``GET /api/papers/lookup``，
``papers:read``）：接受 ``arxiv:`` / ``doi:`` / ``openalex:`` 引用，
单次至多 200 条；每项报告 resolved / hosted / has_md 与元数据，
是批量核对「有没有 markdown」的官方入口。

``qatlas paper fetch`` — 批量提交下载（``POST /api/downloader/fetch``，
需 ``papers:write``）：混合 DOI / arXiv ID / 论文链接，单次至多 50 条，
可 ``--file FILE`` 从文件读（``#`` 注释行忽略）；输出逐项入队结果与
``enqueued`` 汇总。入队不等于 PDF 已归档。

``qatlas paper jobs`` — 下载进度（``papers:read``）：默认打印本地任务
快照与 counters；``--remote`` 查询持久化 outbound fleet 快照；
``--watch`` 轮询至空闲（Ctrl-C 提前退出码 130）；``--json`` 机器可读
（watch 模式为 JSON lines 流）。

``qatlas contrib`` — 贡献者工作流
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas contrib pdf <ARXIV_ID|DOI> --pdf <path> [--overwrite] [--verify warn|strict]
   qatlas contrib mineru                                # 队列模式：认领并处理
   qatlas contrib mineru <ARXIV_ID>                     # 单篇模式：认领、转换、上传
   qatlas contrib mineru --watch [--watch-interval N]   # 守护循环
   qatlas contrib mineru <DOI> --zip <path> [--verify warn|strict]
                                                       # 上传现成 MinerU zip（DOI-only）

``--verify strict`` 在校验失败时硬失败，``warn`` 仅告警；``--overwrite``
覆盖服务端已有 PDF。

``qatlas parser`` — 本地解析
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas parser <arxiv_id> [-o OUTPUT_DIR] [--no-pdf] [-m] [-j]

``-m`` / ``-j`` 额外保存 Markdown / JSON 解析产物，``--no-pdf`` 跳过
PDF 下载；产物默认落在 ``./papers``。

论文搜索（qatlas search）
-------------------------

``qatlas search`` 由独立插件包 ``qatlas-search`` 提供（entry-point 组
``qatlas.plugins`` 自动挂载）。未安装时命令会给出安装提示，不影响其他命令；
从私有仓库安装：

.. code-block:: bash

   uv tool install --from git+ssh://git@github.com/IAI-USTC-Quantum/qatlas-search.git qatlas-search

四种形态：

.. list-table::
   :header-rows: 1
   :widths: 34 66

   * - 形态
     - 说明
   * - ``qatlas search QUERY``
     - **agentic 融合搜索**：默认。经 qatlasd 代理 fan-out 多源、融合排序，
       可选 LLM 学术结论；按用户每日计量。``--direct`` 变体在本地自带 key
       直跑（不走服务端、不计量的 Bring-Your-Own-Key 模式）
   * - ``qatlas search survey GOAL``
     - **规则化综述检索**：``/api/search/survey``，按作者 / 期刊 / 年份 /
       引用量过滤；覆盖为 post-filter bounded（过滤已检索元数据，非穷尽）
   * - ``qatlas search multi QUERY``
     - **逐平台原始结果**：``/api/search/multi``，每个 backend 返回该平台
       自身排序的命中，不做跨源融合
   * - ``qatlas rag QUERY``
     - qatlas-rag 插件提供的语义向量检索直连（独立命令，见 :doc:`search`）

认证与服务器解析（服务器模式）：server URL 与 PAT 复用 qatlas 客户端配置
（``config.yaml`` + ``hosts.yml``，即 ``qatlas auth login`` 存的那份），
``--server`` / ``--token`` 可按次覆盖；需要 ``papers:read`` scope。
``--direct`` 模式不访问 qatlasd，backend key 来自本机
``~/.qatlas/config.yaml`` 的 ``search:`` 段（或 ``/etc/qatlas-search/``）。

默认 agentic 搜索
~~~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas search "surface code threshold"
   qatlas search "quantum error correction" --json --top 20
   qatlas search --list-tools                 # backend 目录（服务器模式含实时可用性）
   qatlas search "graph neural networks" --direct --tools arxiv,openalex,crossref

.. list-table::
   :header-rows: 1
   :widths: 26 74

   * - 标志
     - 说明
   * - ``--agent / --no-agent``
     - 是否请求 LLM 结论（默认开；服务器模式）。结论消耗 LLM tokens，
       与调用次数一起计入每日配额
   * - ``--tools a,b,c``
     - backend 允许清单（默认取配置）。学术源之外还有网页引擎与需个人 key
       的源（CORE / NASA ADS / IEEE / Scopus 等）；``--list-tools`` 查看全部
   * - ``--top N`` / ``--max-results N``
     - 展示条数（默认 15）/ 检索上限
   * - ``--direct``
     - 本地直跑：自带 key、无计量、无 registry 身份锚定；
       配 ``--ranking scorer --scorer-file PATH`` 可用自定义 JSON 评分程序
       （``--list-scoring-capabilities`` 打印能力清单，``--explain`` 输出
       评分执行树）
   * - ``--json`` / ``-v``
     - JSON 输出 / 逐 backend 计数与错误

超限（429）时 stderr 给出当日用量与限额；配额规则见 :doc:`search`。

综述检索 survey
~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas search survey "topological qubits" \
     --author "Felix von Oppenheim" --year-from 2020 --min-citations 50
   qatlas search survey "surface codes" --query "surface code threshold" \
     --query "mbqec" --sort citations --json

.. list-table::
   :header-rows: 1
   :widths: 26 74

   * - 标志
     - 说明
   * - ``--query Q``
     - 显式检索式（可重复，至多 6 条；缺省由 goal 与作者规则推导）
   * - ``--author A`` / ``--venue V``
     - 作者 / 期刊过滤（可重复）
   * - ``--year-from / --year-to``
     - 年份区间（1600–2100）
   * - ``--min-citations N``
     - 最低引用数
   * - ``--sort relevance|newest|citations``
     - 有界结果集内排序（默认 relevance）
   * - ``--sources a,b``
     - backend 允许清单（默认 semantic_scholar,arxiv,openalex）
   * - ``--agent / --no-agent``
     - 是否让服务端 LLM 规划查询（默认开，消耗 agentic 配额）

命中按 DOI / arXiv ID 锚定进论文 registry；响应的 ``coverage`` 明示
``post_filter_bounded``、``exhaustive=false``——规则只过滤已检索到的
元数据，不是对全文献空间的穷尽综述。

逐平台搜索 multi
~~~~~~~~~~~~~~~~

.. code-block:: bash

   qatlas search multi "quantum error correction" --sources arxiv,openalex
   qatlas search multi "fault tolerance" --sources semantic_scholar,wikipedia --json

每个被选中的 backend 返回自己的原始命中（平台自身排序、含 ``paper_id``
时说明已在本站 registry）；个人 backend key 由服务端注入（在网页 dashboard
「搜索 API keys」里保存，AES-GCM 加密存储），失败的 backend 单独报错不影响
其他平台。需要 key 但未配置的源会被跳过或禁用——用 ``--list-tools`` 查看
哪些已就绪。

论文匹配（qatlas match）
------------------------

``qatlas match`` 由独立插件包 ``qatlas-match`` 提供（entry-point 组
``qatlas.plugins`` 自动挂载）。与 search 的本质区别：match **只在内部
registry 里查找**——判定一篇论文（qatlas-id / DOI / arXiv id / OpenAlex
id / 论文 URL / 标题）是否已在 qatlas 入库，命中则返回统一的
``qa_…`` 论文 id。精度优先，宁缺毋滥：

- **标识符精确匹配**：只做极简归一化——strip、转小写、剥离已知 URL
  前缀（``https://doi.org/`` 等）、arXiv id 剥 ``arXiv:`` 前缀与尾部
  ``vN`` 版本号。归一化后与库内存储值完全相等才算命中，没有任何模糊
  匹配。
- **URL 白名单**：只识别 arxiv.org（``/abs/``、``/pdf/``）、doi.org、
  openalex.org 三类域名，化归为内嵌标识符后按上述规则匹配；其他域名
  一律不命中。
- **标题必须每个单词都匹配**：小写、按字母数字切词后，查询与库内
  标题的词集完全一致（顺序无关）；多一个词或少一个词都不命中。标题
  撞车多篇时返回全部候选并标记 ``ambiguous``，用 ``--author`` /
  ``--year`` 消歧。
- **合并论文透明解析**：已并入其他论文（``merged_into:``）的条目一路
  追到幸存者，返回的永远是当前规范 id。

.. code-block:: bash

   uv tool install --from git+ssh://git@github.com/IAI-USTC-Quantum/qatlas-match.git qatlas-match

.. code-block:: bash

   qatlas match 2401.12345 10.1103/physrevlett.123.070501   # 自动识别 kind
   qatlas match https://arxiv.org/abs/quant-ph/9508027       # URL 化归
   qatlas match --title "Quantum Error Correction for Beginners" --author Nielsen
   qatlas match --doi "https://doi.org/10.1103/PhysRevA.12.010101" --json

.. list-table::
   :header-rows: 1
   :widths: 26 74

   * - 标志
     - 说明
   * - ``INPUT...``
     - 自由格式标识符（位置参数，可多个）：``qa_`` id、DOI、arXiv id、
       OpenAlex id、论文 URL 或多词标题，逐条自动识别；含空格的自由文本
       才会按标题处理，单词不会被误当标题
   * - ``--doi / --arxiv / --openalex / --qatlas-id / --url / --title``
     - 强制指定 kind（避免自动识别歧义）
   * - ``--author`` / ``--year``
     - 标题消歧提示（第一作者姓氏 / 年份）；给出后视为断言，排除全部
       候选即不命中
   * - ``--json``
     - 输出服务端原始响应（``results`` 数组含 ``matched`` / ``qatlas_id``
       / ``method`` / ``paper`` / ``candidates`` / ``reason``）
   * - ``--direct``
     - 部署机上就地连库直跑（读本机 ``~/.qatlas/match.yaml``），不经
       qatlasd

认证（服务器模式）：复用 qatlas 客户端配置的 PAT，需要 ``papers:read``
scope；请求经 qatlasd 的 ``POST /api/papers/match`` 代理（用户鉴权在
qatlasd 完成，qatlas-match 微服务只走内网）。退出码为 grep 风格便于脚本
判断：``0`` 至少一条命中、``1`` 全部未命中、``2`` 用法 / 配置错误。

其他插件命令
------------

.. list-table::
   :header-rows: 1
   :widths: 18 30 52

   * - 命令
     - 插件包
     - 说明
   * - ``qatlas rag``
     - ``qatlas-rag``
     - 语义检索（qatlas-rag 微服务的 CLI 前端；``--server`` / ``--token``
       / ``--max-results`` / ``--json``）
   * - ``qatlas match``
     - ``qatlas-match``
     - 论文身份匹配（高精度判定 DOI / arXiv / OpenAlex / URL / 标题是否
       已入库并返回统一 ``qa_…`` id；详见 `论文匹配（qatlas match）`_ 一节）

未安装时 CLI 会提示安装方法，不影响其他命令。

插件协议（CLI plugin API v2）
-----------------------------

第三方包通过 entry-point 组 ``qatlas.plugins`` 向 CLI 贡献命令（协议定义在
``qatlas/client/plugins/base.py``）：

.. code-block:: toml

   # 插件包自己的 pyproject.toml
   [project.entry-points."qatlas.plugins"]
   myplugin = "my_package.qatlas_plugin:plugin"

插件类继承 ``QatlasPlugin``，从两个挂载点贡献命令：顶层
``top_level_commands()``（``qatlas <name>``）与 ``contrib_subcommands()``
（``qatlas contrib <name>``）。``available()`` 控制命令是否出现；
插件导入失败只影响自己，不拖垮 CLI。

**协议 v2 要点**：

- ``CommandSpec.handler`` 支持两种签名：v1 的 ``handler(argv) -> int``
  与 v2 的 ``handler(ctx, argv) -> int``。CLI 按签名探测分发，旧插件
  无需改动；
- ``ctx`` 是 ``CliContext``（已解析的 ``server_base_url`` / ``token`` /
  ``request_timeout`` / ``insecure`` / ``client_version``），与内置命令
  读同一份 ``~/.config/qatlas/config.yaml`` 与 hosts.yml；
- ``qatlas.client.pluginsupport`` 是插件的公共 HTTP 层：
  ``server_request(ctx, method, path, ...)`` 自动带 PAT、
  ``X-Qatlas-Client-Version`` 协商头、超时与 TLS 选项；
  ``format_api_error(resp)`` 统一错误渲染；``poll_lro(ctx, path, ...)``
  提供 LRO 轮询。插件不应再自行实现这些；
- 退出码约定与核心一致：0 成功、1 传输/服务端错误、2 输入非法、
  4 版本协商硬失败（写操作对更新服务端）；
- 插件可声明 ``cli_api_version``；声明版本高于当前 CLI 提供的
  ``PLUGIN_API_VERSION`` 时跳过其命令并在 stderr 给一行警告
  （``QATLAS_QUIET=1`` 静默）；
- ``CommandSpec.usage`` 可选字段会在 ``qatlas --help`` 里额外展示一行
  用法。

内置命令优先于插件命令；``search`` / ``rag`` 未安装时 CLI 给出安装提示
（提示表在 ``qatlas/client/plugins/registry.py`` 的
``KNOWN_STANDALONE_PLUGINS``）。

示例
----

**论文获取**

.. code-block:: bash

   # 拉取论文 Markdown（未收录时服务端惰性抓取）。PDF 分发已停用
   # （/pdf 恒 410），没有 get pdf 子命令——要元数据用 get metadata。
   qatlas paper get markdown quant-ph/9508027 -o paper.md
   qatlas paper get metadata 10.1103/PhysRevLett.103.150502

   # 查看转换进度
   qatlas paper status 2401.12345

**贡献者工作流**

.. code-block:: bash

   # 上传本地 PDF（需 papers:write 权限的 PAT）
   qatlas contrib pdf quant-ph/9508027v1 --pdf paper.pdf

   # 本地 MinerU 转换并回传
   qatlas contrib mineru 2501.00010v1
   qatlas contrib mineru --watch

搜索的用法示例见上文 `论文搜索（qatlas search）`_ 一节。

论文 ID 支持多种形式（服务端自动补全）：带版本 arXiv ID（``0811.3171v3``）、
裸 arXiv ID（补最新版本）、裸旧式编号（补 ``quant-ph/`` 分类）、DOI。

配置与认证
----------

- 客户端配置只有 YAML 一个来源：``~/.config/qatlas/config.yaml``
  （``qatlas config set <key> <value>``，敏感值走 stdin 隐藏输入）；
- 认证用 PAT：``qatlas auth login`` 发起设备流，浏览器里批准后令牌自动落盘；
- 论文读取需要 ``papers:read``，上传 / 删除需要 ``papers:write``，见 :doc:`api`。

.. note::

   ``qatlas search`` 与 ``qatlas rag`` 都由独立插件提供（entry-point
   发现，安装对应的插件包后命令自动出现在 CLI 中，未安装时会提示
   安装方法）：前者由 qatlas-search 仓库提供（详细用法见本文
   `论文搜索（qatlas search）`_ 一节），后者由 qatlas-rag 仓库提供，
   平台级搜索架构见 :doc:`search`。

服务器端运维命令（``qatlasd``）
-------------------------------

``qatlasd downloader probe`` 是本地下载阶梯的诊断 / 抽样工具，
**不接入新的 outbound multi-worker fleet**，不测试持久化入队、审批、
远程租约、上传归档、done 收据或重启恢复。即使服务器配置启用了 remote，
也不能用 probe 成功作为新 fleet 的验收结果：

.. code-block:: bash

   # 本地运行下载阶梯，打印策略轨迹（不经过服务器 fleet）
   qatlasd downloader probe 10.1038/s41586-024-07806-9 arXiv:2401.12345

   # 从 OpenAlex 随机抽 N 篇论文压测（可选 --search 过滤领域）
   qatlasd downloader probe --random 30 --search "quantum computing"

   # LEGACY：测试旧 downloaderproxy，不是 outbound worker 注册 / 调度
   qatlasd downloader probe --random 10 --proxy https://legacy-proxy.example.org

   # 机器可读 JSON 输出；任一失败退出码为 1（适合自动化）
   qatlasd downloader probe --random 5 --json

常用标志：

.. list-table::
   :header-rows: 1
   :widths: 25 75

   * - 标志
     - 说明
   * - ``--random N``
     - 从 OpenAlex 随机抽 N 篇（自动过滤 ``has_doi:true``）
   * - ``--search "..."``
     - OpenAlex 搜索过滤（与 ``--random`` 组合；不带 ``--random`` 时无效）
   * - ``--proxy URL``
     - **LEGACY** downloaderproxy 端点（旧 ``/v1/jobs`` / ``/v1/files/*`` 协议）
   * - ``--proxy-token T``
     - **LEGACY** 代理 Bearer token，不是 enrollment token 或 worker secret
   * - ``--browser URL``
     - 本地 browser lane CDP 端点（如 ``http://127.0.0.1:9222``）
   * - ``--agent``
     - 强制启用 LLM agent 兜底（需 ``downloader.agent.*`` 配置）
   * - ``--concurrency N``
     - 并行论文数（默认 3）
   * - ``--timeout D``
     - 单篇预算（默认 4m）
   * - ``--json``
     - 机器可读 JSON 输出

需要 ``paper_access.enabled: true``（读取 ``~/.qatlas/config.yaml``）。
旧 ``downloader.proxy.url / token / timeout`` 配置为 **LEGACY**，不能与
``downloader.remote.enabled: true`` 混用；新 worker 主动连接 qatlasd，
不是把 worker URL 传给 ``--proxy``。

新 fleet 的验证入口
~~~~~~~~~~~~~~~~~~~

#. 在 :doc:`admin` 节点页生成一次性 enrollment token，使用独立持久化卷
   注册 worker，管理员以人类浏览器会话核实并 approve；PAT 不能执行审批。
#. 在服务器 :doc:`web` 的 Robust Downloader 页或 :doc:`api` 的
   ``POST /api/downloader/fetch`` 提交获授权样本。服务器先本地尝试，
   按需委托使用各自网络 / 浏览器的 approved worker；不同论文并行，
   单篇按次数与截止时间预算顺序故障转移。
#. 用 ``GET /api/downloader/jobs`` 查看本地进度与最多 512 条持久化待执行
   请求（含等待本地槽位者），用 ``GET /api/downloader/remote-jobs`` 查看
   重启后仍可查询的远程快照（最多 500 条），管理员同步检查节点心跳与容量。
#. 确认上传已完成 **对象存储 + registry 资产登记 → 持久化 done 收据**。
   worker 核对收据后删除副本；staged 或 HTTP 成功不能代替此确认。
   MinerU / 索引是独立后续处理，不阻塞收据，Markdown 转换需单独检查。

协议、临时结果保留期限和策略详见 :doc:`search` 与 :doc:`api`。
