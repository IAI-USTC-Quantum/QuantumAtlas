# Robust Downloader（多范式论文抓取）

Robust Downloader（`internal/downloader`，builtin 插件 `downloader`，capability
`download`）解决一个问题：**给一个 DOI / arXiv id / 论文 URL，把正式发表的 PDF
弄回来**——不依赖单一上游，不因为一个出版社的反爬墙就放弃。

它不是替代 [懒加载摄入](../concepts/data-flow.md) 的 arXiv 抓取，而是把覆盖面
扩展到 arXiv 之外的正式版论文：OA 仓库、出版社模式 URL、落地页挖掘、真实浏览器、
甚至远端有校园订阅出口的代理机。抓回的 PDF 走与上传/arXiv 抓取完全相同的
存储 + 注册 + MinerU 管线。

入口有三个：

| 入口 | 说明 |
|---|---|
| `POST /api/downloader/fetch` + `GET /api/downloader/jobs` | HTTP API（本页 + [REST API](rest-api.md)）|
| SPA 页面 `/$lang/downloader` | 粘贴一批标识符 → 提交 → 逐篇看进度与策略轨迹 |
| `qatlasd downloader probe` | CLI 现场压测（本页 [CLI probe](#cli-probe)）|

前置条件：`paper_access.enabled: true`（复用其 fetcher/resolver/对象存储接线）+
`downloader.enabled: true`（默认开）。任一关闭时端点仍注册：`/fetch` 返回
503（detail 说明缺哪个开关），`/jobs` 返回空快照 200。

## 策略阶梯（strategy ladder）

每个标识符先进 registry 的 `ResolveOrMint`（解析或铸造 paper 记录），然后跑
策略阶梯——按序尝试，第一个通过验证的候选即胜出。每次尝试都记录在
attempt trace（策略 id、URL、错误、耗时）里，落在 job snapshot 与
`paper_acquisition_events` 审计表。

| 顺序 | 策略 id（trace 里可见）| 做什么 |
|---|---|---|
| 1 | `arxiv` | 输入带 arXiv id 时直下 arxiv.org（最可靠、带版本）|
| 2 | `pmc-resolve` | 输入是 PMCID 时先经 Europe PMC 换算成真 DOI |
| 3 | `twin-resolve` | DOI-only 论文经 OpenAlex 反查 arXiv twin，回退到 `arxiv` 直下 |
| 4 | `oa:europepmc` / `oa:unpaywall` / `oa:openalex` / `oa:semantic_scholar` | OA 元数据 API 拿候选 URL。Europe PMC `fullTextUrlList`（含免 PoW 的 `?pdf=render` 路径）；Unpaywall `best_oa_location` + 全部 `oa_locations`；OpenAlex `pdf_url`；Semantic Scholar `openAccessPdf`（可选 `downloader.s2_api_key`）|
| 4b | `oa:<name>+landing` | 候选其实是绿色 OA 仓库的落地页（HAL、高校 repo…）时，先挖出真正的 PDF 链接再抓 |
| 5 | `pattern` | 按 DOI 前缀套出版社 URL 模板（Springer、Wiley、T&F、Sage、ACS、ACM、Frontiers、PLOS、eLife、bioRxiv 等）|
| 6 | `landing` | 抓 DOI 落地页挖 `citation_pdf_url`（同 host 的裸 href 兜底；IEEE 文档页会穿过 stamp.jsp 中间页）|
| 7 | `remote-proxy:<s>` | 前面撞上 entitlement 墙（bot/PoW challenge）时，**优先于本地浏览器**把整条梯子委托给 downloaderproxy（见下）|
| 8 | `browser` | 经 CDP 把出版社 PDF/落地 URL 在**真实 Chromium** 里重放——Cloudflare/IEEE WAF 这类纯 HTTP 过不去的墙 |
| 9 | `agent:openai` / `agent:claude` | LLM 兜底：从落地页 HTML 里提名 PDF 链接（`downloader.agent.backend`，默认关）。提名的 URL 仍要走完整验证 |
| 10 | `remote-proxy` | 终极兜底：以上全败（不只是 entitlement 墙）时最后委托一次代理机 |

### 验证管线

**每个候选在入库前都要过同一套验证**——策略只负责提名 URL，接受与否由验证决定：

- `%PDF-` magic + `%%EOF` trailer + 大小上下限；
- HTML 响应会被分类：`bot_challenge` / `pow_challenge` / `paywall` / `error_page`
  （以及 IdP 登录跳转检测）——分类结果进 trace，是 probe 失败分类学的数据源；
- 抓取带 cookie jar、浏览器式 headers、per-host 速率限制；
- `downloader.respect_robots`（默认 **false**）可开启 robots.txt 门控。按需的
  entitled fetch 不是爬虫，且不少出版社用 blanket `Disallow: *` 反 AI 爬虫，
  开启会误杀合法下载；需要严格对齐时再开。

通过的 PDF 带 provenance 入库（`downloader:<strategy>` + 来源 URL + sha256），
触发既有的 PDF-ready hook 驱动 MinerU 转换。

## 配置参考（`config.yaml` 的 `downloader:` 段）

完整 YAML schema 见仓库根 [`config.example.yaml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/config.example.yaml)；此处逐字段展开：

```yaml
downloader:
  enabled: true                # 总开关；还需 paper_access.enabled: true
  concurrency: 2               # 并行下载 job 数
  unpaywall_email: ""          # Unpaywall 联系邮箱；空则回落 paper_access.openalex_mailto
  s2_api_key: ""               # 可选 Semantic Scholar key（免费档，1 req/s）——
                               # 稳定最高召回的 OA resolver，避免共享池 429
  respect_robots: false        # robots.txt 门控；默认关（见上）
  browser:                     # CDP 浏览器 lane：指向一个已在运行的 Chromium
                               # 的 DevTools 端点（如 ws://browser:9222）。
                               # 空 cdp_url = 关闭该 lane；出版社登录态就存在
                               # 那个浏览器 profile 里（人工登一次）
    cdp_url: ""
    timeout: 45s
  proxy:                       # 委托给独立 downloaderproxy（见下）。设置后
                               # 优先于本地浏览器 lane
    url: ""                    # e.g. http://ag-workstation:8602
    token: ""                  # 与代理机的 DL_PROXY_TOKEN 同值
    timeout: 5m
  agent:                       # LLM 兜底链接提取器；backend 空 = 关
    backend: ""                # "" | "openai" | "claude"
    base_url: ""               # openai：OpenAI 兼容端点，e.g. https://llm.example.com/v1
    api_key: ""
    model: ""
    max_tokens: 1024
    claude_bin: claude         # claude：本机 headless claude CLI（需已 OAuth 登录）
    claude_model: ""           # 空 = claude 默认模型
    timeout: 120s
    max_budget_usd: 0          # claude 专用；0 = 不加预算 flag
```

## downloaderproxy：校园出口代理服务

当 qatlasd 所在网络没有出版社订阅（或被强制代理）时，把抓取委托给一台**有
直连 entitlement 的机器**（如校园网出口的工作站）：

- **单容器、无 sidecar**：镜像（`Dockerfile.downloaderproxy`）自带一份当前版
  Chrome-for-Testing（chromedp/headless-shell——出版社 WAF 会拒绝 debian 仓库里
  的陈旧 chromium），entrypoint 启动它并暴露本地 CDP 端点，服务像本地浏览器
  lane 一样驱动它。
- qatlasd 侧配 `downloader.proxy.{url,token,timeout}`；梯子委托是
  submit → poll → fetch 三步，返回的字节仍过 qatlasd 侧同一套验证管线
  （策略 id 记作 `remote-proxy:<s>`）。

```bash
# 构建与部署
docker build -f Dockerfile.downloaderproxy -t qatlas-downloaderproxy .
docker run -d --name downloader-proxy -p 8602:8602 \
  -e DL_PROXY_TOKEN=<shared-secret> \
  -e DL_UNPAYWALL_EMAIL=you@example.com \
  -e DL_S2_API_KEY=s2k-... \
  qatlas-downloaderproxy
```

!!! warning "不要给容器挂代理 env"
    确保容器**没有** `http_proxy` / `https_proxy`（docker daemon 默认或 `-e`
    注入），否则出口失去校园网络身份，entitlement 白挂。

服务自身的 API（设了 `DL_PROXY_TOKEN` 时要求 Bearer）：

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/healthz` | `{"status":"ok","browser":bool}` |
| `POST` | `/v1/jobs` | body `{"identifier": "..."}` → `202 {job_id, kind}`（异步跑梯子）|
| `GET` | `/v1/jobs/{id}` | 状态 + 完整 attempt trace；终态为 `done` / `failed` / `invalid` 时附 `file_token` |
| `GET` | `/v1/files/{token}` | 取 PDF 字节。**token 单次有效**，job 完成 30 分钟后过期 |

文件落在 `/tmp/downloaderproxy`，首次取走或过期清扫即删；重启不持久化任何状态。

## 浏览器 lane

`downloader.browser.cdp_url` 指向一个**已经在运行**的 Chromium 的 DevTools
端点（`ws://…:9222`）。适用场景是纯 HTTP 撞墙（Cloudflare / IEEE WAF /
PoW challenge）或 SPA 出版社站点（PDF 链接只有 JS 渲染后才出现）。需要登录态的
出版社：在那个浏览器 profile 里**人工登录一次**，lane 复用该会话。若部署
downloaderproxy，它自带浏览器，本地 lane 通常无需再配。

## CLI probe { #cli-probe }

`qatlasd downloader probe` 是在线健壮性压测（完整 flag 表见
[CLI 参考](cli-qatlasd.md#downloader-probe)）：对真实论文跑完整策略阶梯，输出
逐篇结果表 + 失败分类学汇总，**任一失败 exit 1**（方便脚本/CI 分支）。
不写 registry、不写对象存储——probe 的验证与 server 路径完全一致但结果只进
stdout。要求 `paper_access.enabled: true`。

```bash
# 指定标识符
qatlasd downloader probe 10.1038/s41586-024-07806-9 arXiv:2401.12345

# 从 OpenAlex 随机抽 30 篇有 DOI 的论文（可加检索词过滤）
qatlasd downloader probe --random 30 --search "quantum computing"

# 机读输出
qatlasd downloader probe --random 10 --json
```

| Flag | 默认 | 含义 |
|---|---|---|
| `--random <N>` | 0 | 不给位置参数时，从 OpenAlex 抽 N 篇随机 works（`has_doi:true`）|
| `--search <q>` | — | `--random` 的 OpenAlex 检索过滤（如 `"quantum computing"`）|
| `--agent` | false | 强制启用 agent 兜底（仍需 config 里 `downloader.agent.*` 配好）|
| `--browser <url>` | — | 浏览器 lane CDP 端点 override（如 `ws://127.0.0.1:9222`；隐含 `--browser-on`）|
| `--browser-on` | false | 用 config 里 `downloader.browser.cdp_url` 启用浏览器 lane |
| `--proxy <url>` | — | downloaderproxy URL override（如 `http://ag-workstation:8602`）|
| `--proxy-token <t>` | — | downloaderproxy Bearer token |
| `--concurrency <N>` | 3 | 并行论文数 |
| `--timeout <dur>` | 4m | 单篇阶梯预算 |
| `--json` | false | 机读 JSON 输出（替代表格）|

失败分类学（summary 里按桶计数）：`bot_challenge` / `paywall` /
`robots_blocked` / `not_pdf` / `404` / `403` / `rate_limited` /
`no_candidates` / `other`。残余失败里 entitlement 级的终态（IEEE 202 网关、
Wiley/AIP 的 Cloudflare、非 OA 的 Nature/Elsevier 内容、HAL 的 JS 壳）正是
downloaderproxy / 浏览器 lane 要吃的范围。

## 管理员补救流（失败闭环） { #admin-remediation }

抓取失败不是终点。管理员 SPA 的 acquisition failures 表
（`GET /api/admin/acquisition/failures`，见 [REST API](rest-api.md)）列出
持久化的失败记录——阶段、原因、重试计数，附 DOI 直达链接；表格里可**内联上传
人工取回的 PDF**（人手动下载 → 对着 DOI 直接 `POST /api/papers/{doi}/upload-pdf`），
立即闭合该论文的获取状态并驱动 MinerU。已入库资产可用
[Admin Asset Browser](rest-api.md)（`/$lang/admin/assets`）浏览 / 预览 / 下载 /
presign。
