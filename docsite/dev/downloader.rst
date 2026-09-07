健壮下载器内部架构（downloader / downloaderproxy / userkeys）
================================================================

本文覆盖 v0.26.0–v0.28.0 落地的三条内部能力：``internal/downloader``
健壮论文 PDF 获取模块（Robust Downloader）、其独立部署形态
``cmd/downloaderproxy``，以及 ``internal/userkeys`` 用户第三方搜索
key 的加密存储。部署视角的 runbook 见 :doc:`prod-deploy`，app 接入
模式对照见 :doc:`apps`。

策略梯（strategy ladder）
----------------------------

给定一个论文身份（DOI / arXiv id / 可解析出二者之一的 URL），
``Downloader.FetchPDF`` 依次尝试以下策略，任一策略产出**通过验证
管线的 PDF 字节**即终止；每次尝试（策略 id、URL、错误、耗时）都
记入 trace，失败时随任务快照与审计事件一起落盘：

.. list-table::
   :header-rows: 1
   :widths: 20 80

   * - 策略 id
     - 行为
   * - ``arxiv``
     - arXiv 直下：复用共享的 ``arxiv.Fetcher``\（pin 版本、全局限速、
       无版本 id 先 resolve latest）
   * - ``pmc-resolve``
     - 输入是 PMCID（``pmc:PMC…``）时先经 Europe PMC 换取真实 DOI，
       EPMC render URL 作为优先候选
   * - ``twin-resolve``
     - DOI-only 论文先问 OpenAlex 有无 arXiv twin；有则回到 arXiv 取
       （可靠、带版本），与 ingest 管线的偏好一致
   * - ``oa:europepmc`` / ``oa:unpaywall`` / ``oa:openalex`` /
       ``oa:semanticscholar``
     - OA 元数据 API 轮询候选直链。候选若其实是仓储落地页
       （green-OA：HAL、高校仓库等）则先挖出真实 PDF 链接再取
       （trace 记为 ``oa:<name>+landing``）
   * - ``pattern``
     - 按 DOI 前缀构造出版社直链（Springer、Wiley、T&F、SAGE、ACS、
       ACM、Frontiers、PLOS、eLife、bioRxiv）；构造出的 URL 仍要走
       验证管线——是猜测，不是承诺
   * - ``landing``
     - GET doi.org 落地页，挖 ``citation_pdf_url`` / PDF 锚点；
       IEEE 文档页额外解析 stamp.jsp iframe（一次有界请求）
   * - ``remote-proxy``
     - **提前触发位**：trace 中出现 bot challenge / 202 / 403 等
       授权墙信号时，先于本地 browser lane 委派给远端 downloaderproxy
       （见下文）——它在有直接订阅的网络上跑完整梯子
   * - ``browser``
     - 本地 CDP 浏览器 lane：把已试过的 PDF-ish 候选与落地页 URL 放进
       真实 Chromium 重放（bot 墙 / SPA 落地页无候选时触发）
   * - ``agent``
     - LLM 兜底链接提取（``downloader.agent.backend``：
       ``openai`` 兼容端点或本地 ``claude`` CLI），只提名 URL，
       提名仍需过验证
   * - ``remote-proxy``\ （终位）
     - 上述全部失败（不止授权墙）时的最后委派；trace 已含
       ``remote-proxy`` 时不重复

验证管线
--------

每个候选字节（包括 browser lane 捕获的与 downloaderproxy 取回的）
都经过同一条管线才会被接受（``fetcher.go``）：

- ``%PDF-`` 魔数；
- 尾部 2 KiB 内含 ``%%EOF``\（增量更新 / 尾部追踪字节的出版社会被
  ``startxref`` 存在性豁免，否则判 truncated）；
- 尺寸界：10 KiB ≤ size ≤ 100 MiB；
- 非 PDF 的 HTML body 分类为 ``bot_challenge``\（Cloudflare "Just a
  moment"、Radware…）/ ``pow_challenge``\（CloudPMC PoW…）/
  ``paywall``\（sign in、purchase…）/ ``error_page``，分类结果驱动
  重试与 UI 文案，也是 browser lane / remote-proxy 的触发信号；
  身份提供商握手（URL 含 ``/authorize`` 或 ``idp.``）按 paywall 报告。

FetchClient（``fetcher.go``）
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

除 arXiv（自带限速 fetcher）外所有策略共享的 HTTP 抓取器：

- 进程级 cookie jar + 浏览器式请求头（Chrome UA 追加
  ``QAtlasDownloader`` 后缀，流量对运维方可辨识）；
- per-host 限速（默认 0.5 rps、burst 1），出版社对重试风暴远比
  arXiv 敏感；429/5xx 重试一次（2s 退避，Retry-After 封顶 8s）；
- 可选 robots.txt 门（``downloader.respect_robots``，**默认关**：
  本模块是用户触发的按需授权获取而非爬虫，且部分出版社以
  robots 全量禁 ``*`` 反 AI 爬虫会误伤合法下载；robots 解析
  fail-open，10 分钟缓存）。

包结构与队列
------------

.. list-table::
   :header-rows: 1
   :widths: 28 72

   * - 文件
     - 职责
   * - ``downloader.go``
     - Downloader 核心：策略梯编排、队列 worker、成功结果的存储与
       注册（``IfNoneMatch:"*"`` 条件写 + ``downloader:<strategy>``
       来源元数据）、PDF-ready hook（MinerU 转换 + best-effort
       rag 索引推送）
   * - ``fetcher.go``
     - FetchClient 与验证管线、``ClassifyBody`` HTML 分类
   * - ``identifiers.go``
     - ``ParseIdentifier``：输入行 → doi / arxiv / url / invalid，
       不做网络请求
   * - ``resolvers.go``
     - ``OAResolver`` 接口与 Europe PMC / Unpaywall / Semantic
       Scholar 客户端（OpenAlex 经共享 resolver 适配）
   * - ``patterns.go``
     - DOI 前缀 → 出版社 URL 构造
   * - ``landing.go``
     - 落地页 miner（citation_pdf_url、IEEE stamp 解析）
   * - ``browser.go``
     - BrowserLane：chromedp 远端 CDP、Fetch-domain 响应拦截捕获
       PDF、渲染后 DOM 挖掘（SPA 站）、人类节奏停顿、stealth 脚本
   * - ``agent.go`` / ``claude.go``
     - LLM 兜底：OpenAI 兼容端点 / 本地 claude CLI（Read/Glob/Grep
       沙箱内自读 landing.html）
   * - ``proxy.go``
     - RemoteProxy 客户端：submit → poll → 一次性 file token 取回，
       取回字节复验管线
   * - ``robots.go``
     - robots.txt 缓存与判定
   * - ``progress.go``
     - 任务快照（供 ``GET /api/downloader/jobs``），终态任务最多
       保留 200 条

队列是 ingest 式的：worker 池（``downloader.concurrency``，默认 2）、
singleflight 按 paper_id 去抖、healthz 风格计数器
（queued / in_flight / succeeded / failed / skipped）。每次相位迁移
经 ``RecordAcquisitionEvent`` 写入 ``paper_acquisition_events`` 审计表。

HTTP 面（qatlasd）
------------------

- ``POST /api/downloader/fetch``\（papers:write）：body
  ``{"items": [...]}``\（≤50 行）；每行解析 → ``ResolveOrMint``
  注册 → 入队，逐行返回 kind / paper_id / 错误
  （``internal/routes/downloader.go``）；
- ``GET /api/downloader/jobs``\（papers:read）：任务快照 + 计数器；
- 模块以第三个 builtin 插件 manifest（id ``downloader``，capability
  ``download``）登记进插件注册表，SPA 的 Robust Downloader 页面按
  其 enabled 状态显隐；未配置时路由仍挂载但答 503。

downloaderproxy（``cmd/downloaderproxy``）
-----------------------------------------------

把整条梯子（含自己的 browser lane）打包成**单容器独立服务**，部署
在出口带机构订阅的机器上（如校园网 Ag-Workstation），qatlasd 自身
处于代理网络时经 ``downloader.proxy`` 委派给它。镜像
``Dockerfile.downloaderproxy`` 自带 chromedp/headless-shell（Chrome
for Testing，过 WAF 的指纹；**在现场构建，不进 ghcr**，见
:doc:`release` 的例外说明），entrypoint 先起浏览器再 exec 服务。
**容器不得带 http_proxy/https_proxy 环境变量**，否则丢失校园出口
身份。

API（``DL_PROXY_TOKEN`` 设置时全部 Bearer 鉴权）：

.. code-block:: text

   GET  /healthz             → {"status":"ok"}
   POST /v1/jobs             {"identifier": "10.1109/... | arXiv:... | url"}
                             → 202 {"job_id","kind"}（异步跑梯子，单 job 预算 6m）
   GET  /v1/jobs/{id}        → {"status":"queued|running|done|failed|invalid",
                                "strategy","url","error","attempts":[...],
                                "size","sha256","file_token","expires_at"}
   GET  /v1/files/{token}    → PDF 字节（X-Qatlas-Sha256 / X-Proxy-Source-Url 头）

file token 单次有效、job 完成后 30 分钟过期；文件落
``/tmp/downloaderproxy``，取回即删，过期由每分钟的 sweeper 清理，
终态 job 可列举约 1 小时。服务内置**浏览器监督器**：每 5s 探
``/json/version``，连续 3 次失败（WAF 重伤后 CDP 死锁但进程未退的
形态）即杀掉并重启浏览器。环境变量：
``DL_PROXY_TOKEN``、``DL_UNPAYWALL_EMAIL``、``DL_S2_API_KEY``、
``DL_BROWSER_CDP_URL``\（默认 ``http://127.0.0.1:9222``）、
``DL_BROWSER_DEBUG``。

qatlasd 侧的委派客户端是 ``internal/downloader/proxy.go`` 的
``RemoteProxy``：默认 5 分钟总预算、1.5s 轮询；成功策略记作
``remote-proxy:<s>``。

配置项（``~/.qatlas/config.yaml`` 的 ``downloader:`` 段）
----------------------------------------------------------

.. list-table::
   :header-rows: 1
   :widths: 32 68

   * - 键
     - 含义
   * - ``enabled`` / ``concurrency``
     - 总开关（另受 ``paper_access.enabled`` 约束）与 worker 数（默认 2）
   * - ``unpaywall_email`` / ``s2_api_key``
     - OA resolver 凭据（Unpaywall 回落 ``paper_access.openalex_mailto``；
       S2 免费 key 1 rps，抗共享池 429）
   * - ``respect_robots``
     - 默认 false，见上文
   * - ``browser.cdp_url`` / ``browser.timeout``
     - 本地浏览器 lane 的 CDP 端点（空 = 关闭）与导航预算（默认 45s）
   * - ``proxy.url`` / ``proxy.token`` / ``proxy.timeout``
     - downloaderproxy 委派（优先于本地 browser lane；timeout 默认 5m）
   * - ``agent.backend`` 等
     - ``""`` 关闭 / ``openai``\（base_url、api_key、model…）/
       ``claude``\（claude_bin、claude_model、max_budget_usd…）

现场压测 harness：``qatlasd downloader probe [ids | --random N
--search ...] [--json] [--agent] [--browser-on] [--proxy URL
--proxy-token T]``\（只验证不落库，任一失败 exit 1）。

用户第三方搜索 key（``internal/userkeys``）
-------------------------------------------

multi / agentic 搜索按用户注入第三方 key（IEEE、Scopus、Tavily…），
key 的存放与解密全在 qatlasd：

- 存储：PocketBase 集合 ``search_api_keys``\（迁移
  ``1791000000``；``user`` 关联级联删除、``backend`` 名、
  ``key_encrypted`` 隐藏字段；``UNIQUE(user, backend)``）——
  owner-only 访问规则、**只允许服务端 handler 写入**\（与
  ``pat_tokens`` 同构）；
- 加密：AES-256-GCM，密钥为 system PAT 的域分离 SHA-256
  （``qatlas-userkeys-v1:`` 前缀）；无新增配置键，但**轮换 system
  PAT 会使存量 key 失效**\（解密失败跳过并告警，用户重新录入）；
  单独泄露 pb_data 不泄露 key 明文；
- HTTP 面（``internal/routes/me_search_keys.go``）：
  ``GET/PUT/DELETE /api/me/search-keys``，**sessionGuard** 而非
  scopeGuard——泄露的 PAT 不得读取或替换所有者的第三方 key；
  列表只回掩码提示（末 4 位）；
- 代理面（``internal/routes/search_multi.go``）：
  ``POST /api/search/multi`` 把调用者的 key 解密后以 ``api_keys``
  转发给 qatlas-search 微服务（微服务本身从不持久化）；
  ``GET /api/search/backends`` 合并静态目录、微服务实时可用性与
  用户已存 key，驱动 SPA 的 backend 勾选框。
