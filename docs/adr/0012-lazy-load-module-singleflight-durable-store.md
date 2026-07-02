# 懒加载模块设计：singleflight + 持久 Store + 组合，不引框架

_把 ADR `0006` 的 OpenAlex 语料写穿缓存、以及 PDF/Markdown 资产的按需物化，统一到一个
命名、可复用的 Go 懒加载模型上。术语与词族见[术语表](../reference/glossary.md)。_

QuantumAtlas 有三条"首次访问才现产、产完落持久层、下次读命中本地"的路径：OpenAlex 语料
（`openalex_works`，miss 时调 OpenAlex API + 写回）、PDF（miss 时从 arXiv 拉 + 落对象存储）、
Markdown（miss 时跑 MinerU 转换 + 落对象存储）。三者本质是同一个**旁路缓存 + 写穿**模式
（cache-aside + write-through），中文统一叫**懒加载**（不是 PL 的惰性求值）。

为给这套模式一个**先进但克制**的设计，我们调研了五大生态的成熟库并在 `readonly-repos`
里逐一读了源码：SolidCache（Rails，SQL 原生）、FusionCache / HybridCache（.NET）、
dogpile.cache（Python）、foyer（Rust）、cache-manager（Node）、JCache/Coherence（JVM）。
结论：**Go 生态没有开箱即用的"持久后端 + 读穿 + 写穿 + 抗击穿"框架**（gocache Loadable 有机制
但内置 store 全内存/redis；gokv 有持久后端但无读穿），而其余生态的成熟库**独立收敛到同一架构**。

## 决策

- **不引框架，用 Go 标准原语做函数/文件级组合。** 迁移整框架（.NET/Ruby/JVM）不在选项内；
  Go 里"手写一个缓存类"正是要避免的。改为复用 `golang.org/x/sync/singleflight`（准标准库、
  官方维护，`internal/openalex.Resolver` 已在用）+ 一个极简持久 `Store` 接口 + 一个通用编排器。

- **三层可插拔组合**（`internal/lazyload`）——这正是 SolidCache / FusionCache / dogpile /
  foyer / JCache **都收敛到的形状**：
  1. **`Store[V]` 接口**：`Load(ctx,key)→(V,hit,err)` + `Store(ctx,key,V)`。持久层就是缓存
     （SQL 表 / 对象存储），**绝不用进程内内存**——内存缓存解决的是我们没有的问题。
  2. **`Loader[V]`**：miss 时的读穿 creator（一次 HTTP / 一次转换）。
  3. **singleflight 合并**：整个读穿（Load→Loader→Store）包在**一个按 cache-key 的
     `singleflight.Group`** 里，并发同 key 只跑一次。这与 foyer 的 `InflightManager` Lead/Wait、
     cache-manager 的 `coalesceAsync`、dogpile 的选主 creator、JCache 的 `computeIfAbsent`
     **是同一个原语**，Go 直接内建。

- **同步 vs 异步是刻意的两种模式，不是遗漏**（singleflight 的能力边界）：
  - **同步物化**（`lazyload.Materializer`）：`Get` 阻塞到值就绪，适合廉价 loader。用于
    **OpenAlex 语料**的 `/api/papers/lookup` 按需求值。
  - **异步物化 / 长时操作**（`internal/mineru.Converter` 的 job 模型）：`202 + Operation-Location
    + Retry-After` 轮询，适合分钟级、需进度/冷却/配额的 loader。用于 **PDF / Markdown**。
    singleflight **给不了**可查询进度、失败冷却、非阻塞轮询——一旦需要这些就越界了，所以
    converter 的 `c.jobs`（＝singleflight + 状态机）保持独立，不硬套 `Materializer`。

- **先进精炼分阶段落地，按实测需求解锁**（避免过度设计）：
  - **阶段 1（本 ADR 已实现）**：抽出 `lazyload.Materializer`，把 OpenAlex 语料路径接上——
    整个读穿统一进一个 singleflight（含语料 SELECT + 写回），**行为不变**，只是命名 + 复用 +
    去掉并发 miss 时的冗余 SELECT / 冗余 upsert。
  - **阶段 2 — fail-safe 瞬态错误白名单**（参考 SolidCache `failsafe.rb`）：只吞可重试的
    PG / 对象存储 / 上游错误→服务陈旧或 deferred，真 bug 照抛。`Get` 的 error 返回值已保留，
    未来加这层不改签名。
  - **阶段 3 — 跨进程 claim**（参考 SolidCache `lock_and_write` 的 `SELECT … FOR UPDATE`）：
    用 `pg_advisory_xact_lock(hashtext(key))`（零 schema）让 active-active 两台 edge 也只有
    一个去 fetch。**Postgres 原生，不引 Redis**。仅当实测到跨 edge 重复 fetch 成问题才做。
  - **阶段 4 — refresh-ahead 软 TTL**（参考 dogpile 的 `async_creator`、cache-manager 的
    `refreshThreshold`、Coherence 的 refresh-ahead）：语料/资产接近过期时，前台立即返回当前值、
    后台单独 singleflight key 触发一次刷新。仅当需要新鲜度（快照更新 / 撤稿状态）才做。

- **明确跳过**：`key_hash` 索引列（我们的 key `W…` / `kind:id` 短，直接索引即可）；Redis /
  分布式缓存（我们有 Postgres）；任何内存缓存库（持久层就是缓存）。

## 后果

- 新增 `internal/lazyload`（通用 `Store` / `Loader` / `Materializer` + `ErrNotFound` +
  写回失败 hook），有完整单测（命中 / miss / not-found / loader 错误传播 / 写回失败容忍 /
  N 并发合并成 1 / 取消不毒化 waiter）。
- `/api/papers/lookup` 的 OpenAlex 语料解析改用 `Materializer`；`corpusResolve` 成为
  `Store.Load`，`UpsertFetchedWork` 成为 `Store.Store`，旧 `fetchOnMiss` 删除。对外
  contract、响应结构、"一个 miss/错误不拖垮整批"的语义**均不变**。
- `Resolver.FetchWorkRecord` 自身的 singleflight 对该路径基本冗余（外层 `Materializer` 已按 ref
  合并，内层通常只剩 1 个 caller），但保留：它仍保护该方法未来的直接调用者，且能合并两个
  文本不同、却归一到同一 DOI/id 的 ref。无额外开销。
- PDF / Markdown **不接入** `Materializer`——它们是异步 job 模型，属于上面写明的能力边界之外。

## 备选方案与否决理由

- **迁移一个成熟框架（FusionCache / SolidCache / dogpile …）**：否决。它们绑定 .NET / Ruby /
  Python 运行时；Go 没有对等物，硬移植 = 手写封装类，正是要避免的。
- **引入内存缓存库（otter / ristretto / gocache Loadable …）**：否决。全是内存层，会在
  "已经是缓存的持久层"前面再叠一层会 stale、且 active-active 下跨 edge 不一致的副本。
- **用 Redis 做分布式锁 / 缓存**：否决（就当前规模）。我们已有 Postgres，跨进程 claim 用
  `pg_advisory` 锁即可，不额外引入一个后端。
- **保持现状（singleflight 只包 HTTP、SELECT/写回在外）**：否决。并发 miss 会冗余 SELECT +
  冗余 upsert；更重要的是这套模式散落各处、没有名字、无法复用——阶段 1 把它收敛成一个命名模块。
