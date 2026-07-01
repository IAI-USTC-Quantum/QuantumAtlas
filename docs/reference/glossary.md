# 术语表 / Glossary

统一项目内反复出现的模式与领域词汇——尤其是**缓存 / 懒加载**这一族，容易和编程语言
理论里的「惰性求值」混淆。**约定：中文一律用「懒加载」，不用「惰性求值」**；英文用
`lazy loading` / `cache-aside` / `write-through` 等业界标准词。写代码注释、ADR、文档时
以本表为准。

## 1. 懒加载与缓存（核心词族）

QuantumAtlas 的 OpenAlex 语料、PDF、Markdown 都不预先全量物化，而是**首次被请求时才现产
并缓存**。这一族词描述的就是这套机制。

| 中文 | English | 定义 |
|---|---|---|
| **懒加载** | lazy loading / load-on-demand | 首次访问某数据时才现产（拉取 / 计算），而非预先全量加载。 |
| **旁路缓存** | cache-aside | 读路径：先查缓存；未命中 → 从事实源现产 → 写回缓存 → 返回。懒加载的标准实现形态。 |
| **写穿（缓存）** | write-through (cache) | 现产后**在同一路径内**把结果写回缓存 / 持久层，使下次读命中本地。 |
| **按需拉取 / 未命中拉取** | fetch-on-miss | cache-aside 里「未命中 → 现产」那一步的具体说法。 |
| **记忆化** | memoization | 把一次（昂贵）计算的结果缓存，后续同输入直接返回。懒加载「缓存结果」的本质。 |
| **预热** | pre-warm | 提前批量灌入缓存以减少运行时未命中。QA 的 `openalex bootstrap-pg` 是**可选**预热，不是前置条件。 |
| **自愈** | self-heal | 未命中时懒加载自动补齐，无需人工 / 批处理干预。 |

!!! warning "别用「惰性求值」"
    编程语言语义的**惰性求值 / lazy evaluation / call-by-need**（thunk、生成器那一套「用到才算」）
    在 QA 里**不存在**。QA 是 cache-aside 式**懒加载**——写文档 / 注释统一用「懒加载」。

**代码出处**：`corpus.UpsertFetchedWork`（写穿）、`Resolver.FetchWorkRecord`（按需拉取）、
`internal/routes/papers_lookup.go::fetchOnMiss`（OpenAlex 懒加载）；设计见 ADR 0006。

## 2. 执行模式：同步物化 vs 异步物化

同一套懒加载，按「现产成本」分两种执行模式。**默认约定：OpenAlex 语料同步、PDF/MD 异步。**

| 中文 | English | 语义 | QA 用在哪 |
|---|---|---|---|
| **同步物化** | synchronous materialization | 在请求内阻塞现产 + 写回，响应直接带结果。产得快才适合。 | OpenAlex 语料 by-id 解析（`fetchOnMiss`，请求内完成） |
| **异步物化 / 长时操作** | asynchronous materialization / long-running operation (LRO) | 后台现产，请求立即返回一个「操作句柄」，客户端轮询到就绪。产得慢 / 贵才用。 | PDF / Markdown（MinerU：网络拉取 + 分钟级转换 + 每日配额） |

**LRO 轮询协议**（PDF / MD 走这个）：未命中 → `202 Accepted` + `Operation-Location: …/status`
+ `Retry-After` → 客户端轮询 `…/status` → 就绪后取内容 `200`。对齐 Azure `Operation-Location`
/ Google AIP-151 的长时操作约定。设计见 ADR 0011。

!!! note "PDF / MD 有没有同步调用？"
    - **热**（资产已在存储）：首次调用即 `200` 返回字节 / 直链 —— 等价同步。
    - **冷**（尚未物化）：**永远** `202` + 轮询，没有 `?wait=` 阻塞参数。这是刻意的：MinerU
      转换分钟级 + 限流 + 有配额，同步阻塞会吃满连接。

## 3. 相关并发 / 操作模式

| 中文 | English | 定义 | 出处 |
|---|---|---|---|
| **并发合并** | singleflight / request coalescing | 同一 key 的并发调用合并成一次现产、共享结果，避免重复拉取。 | `golang.org/x/sync/singleflight`；`internal/openalex/lookup.go` |
| **在途去重 / 搭车** | in-flight dedup / piggyback | 同一资产已有任务在跑，新请求搭车共享、不另起。MinerU converter 的 `c.jobs` 就是带状态机的 singleflight。 | `internal/mineru/converter.go` |
| **有界并发 / 信号量限流** | bounded concurrency (semaphore) | 用带缓冲 channel 限制并发任务数。 | `MINERU_MAX_CONCURRENT_JOBS`（默认 4） |
| **优雅降级** | graceful degradation | 依赖（PG / 对象存储）不可达时降级返回（`available=false` / `X-Catalog-Sync: deferred`）而非硬失败。 | ADR 0006 |

## 4. 领域术语（项目高频）

| 中文 | English | 定义 |
|---|---|---|
| **语料** | corpus | 本地留存的 OpenAlex works 元数据（`openalex_works`），是一个懒加载写穿缓存。 |
| **规范 ID** | canonical id | arXiv 论文的归一主键（裸 id + 版本后缀 `vN`）。见 [arXiv ID 格式](arxiv-ids.md)。 |
| **catalog（目录）** | catalog | 论文资产目录（`papers` + `paper_assets`），QA 托管资产的索引。见 [PG Schema](../server/pg-schema.md)。 |
| **资产 / artifact** | asset / artifact | 一篇论文的某种物化产物：PDF / Markdown / JSON。 |
| **claim（声明）** | claim | lean 侧的文献学声明（哪条 Lean 定理引用哪篇论文），**只读**引用 `papers.paper_id`。见 ADR 0007 / 0008。 |
| **lease（租约）** | lease | MinerU 转换的**独占租约**（防并发重复转换），per-asset。**与 claim 是两回事**，已改名解耦。 |
| **访问开关** | access gate | `QATLAS_PAPER_ACCESS_ENABLED`：合规开关，关时不分发衍生作品（PDF 仅回 arXiv 链接、md/json 不发）。见 [许可与署名](../about/license-and-attribution.md)。 |

## 5. 弃用 → 规范 对照

| 弃用 / 易混 | 用这个 | 原因 |
|---|---|---|
| 惰性求值、惰性（填充） | **懒加载** | 与 PL 的 lazy evaluation（thunk）混淆；QA 是 cache-aside 式懒加载 |
| 惰性（休眠义，如「插件处于惰性状态」） | **休眠 / 未激活**（dormant） | 与「懒加载」撞词；表达「未启用」用休眠更准 |
| MinerU claim（旧名） | **lease（租约）** | 「claim」现专指 lean 的文献学声明；转换独占改叫 lease |
