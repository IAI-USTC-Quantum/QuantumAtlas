# 桶布局（Bucket Layout）

> **范围**：QuantumAtlas v0.7.0+ 的对象存储桶（bucket）拆分设计。涵盖每个桶的职责、命名规则、key 模板、读写权限、和访问模式。具体部署 / 供给脚本见 [`../deployment/rustfs.md`](../deployment/rustfs.md)；历史 `qatlas-raw` 单桶设计的 spike 笔记见 [`storage-architecture.md`](storage-architecture.md)（标注为历史教学材料）。

## 5 + 1 桶速查

| 桶 | 内容 | key 模板 | 大小级 | 访问模式 | 公开/私有 |
|---|---|---|---|---|---|
| `qatlas-pdf` | 论文 PDF blob | `<yymm>/<stem>.pdf` | MB / 个 | 偶尔整文件下载 | 私有，**字节不外传**（share 307 跳 arxiv.org）|
| `qatlas-md` | MinerU 转换的 markdown | `<yymm>/<stem>.md` | KB / 个 | 详情页全文展示 / 搜索 | 私有，presign URL 短时外发 |
| `qatlas-images` | markdown 内联图（一个 paper 一个子目录）| `<yymm>/<stem>/<file>` | KB ~ 100KB / 张 | markdown 渲染时按引用拉 | 私有，presign URL |
| `qatlas-openalex` | OpenAlex 字节级 snapshot mirror | `data/<entity>/<part>.gz` | GB / part，TB / 桶 | 离线 bootstrap / 定期增量 | 私有，从不外发 |
| `qatlas-raw` | **历史单桶**（v0.7.0 前），仍保留作为迁移源 | `pdf/<yymm>/<...>` 等 | TB | 迁移完成后只读 | 私有，迁移期可读 |
| `qatlas-s3-events` | RustFS notify webhook 落盘的 PUT/DELETE 事件流（NDJSON.snappy） | Fluent Bit 自管 | KB ~ MB / 文件 | 审计 / 离线分析 | 私有，**没有 Delete 权限**（write-once 审计）|

## 命名约定

### `<yymm>` shard

所有论文资产按 arxiv ID 的前 4 位 `<yymm>`（YearMonth）分片：

- `2401.00001` → `<yymm>` = `2401`
- 老格式 `quant-ph/9508027` → `<yymm>` = `9508`（含 80s/90s 历史论文）

shard 是 list 友好的天然边界：每个 `<yymm>` prefix 通常几百到几千对象（quantum-ph 月均 ~500 篇）。listing / 迁移 / 对账都按 yymm 切分，单次 list 在 RustFS HDD 上可控。

### `<stem>` arxiv ID

`<stem>` 是 arxiv ID 的 path-safe 字符串：

- 新格式 → `2401.00001` 直接当 stem
- 旧格式 → `quant-ph_9508027`（`/` 换 `_`，跨文件系统安全）

一篇 paper 在三桶里的 key 是**对齐的**：

```
qatlas-pdf/2401/2401.00001.pdf
qatlas-md/2401/2401.00001.md
qatlas-images/2401/2401.00001/fig1.png
qatlas-images/2401/2401.00001/fig2.jpg
```

按 stem 索引就能一次找到一篇 paper 的所有资产。

## Router 抽象

应用层不直接知道桶——`internal/objstore.Router` 按 key 的首段 "kind" 分桶：

| 应用层 key | Router 内部 | 实际桶 |
|---|---|---|
| `pdf/2401/2401.00001.pdf` | strip `pdf/`，写 backend `pdf` | `qatlas-pdf/2401/2401.00001.pdf` |
| `markdown/2401/2401.00001.md` | strip `markdown/`，写 backend `markdown` | `qatlas-md/2401/2401.00001.md` |
| `images/2401/2401.00001/fig1.png` | strip `images/`，写 backend `images` | `qatlas-images/2401/2401.00001/fig1.png` |

> ⚠️ **应用层 kind 名是 `markdown`，但桶名是 `qatlas-md`**——不要在 mc / mirror 脚本里用 `markdown` 当桶名找不到。Router 这层翻译只在 Go 进程内做。

Router 配置走 env：

- `QATLAS_S3_BUCKET_PDF` → 通常 `qatlas-pdf`
- `QATLAS_S3_BUCKET_MD` → 通常 `qatlas-md`
- `QATLAS_S3_BUCKET_IMAGES` → 通常 `qatlas-images`
- `QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT` → `qatlas-openalex`

启动时如果检测到老的单桶 var `QATLAS_S3_BUCKET` 还在 `.env` 里，**fail loud**——`config.go` 拒绝启动并要求改成三桶。

## 为什么从 `qatlas-raw` 拆出来

v0.6.x 时单桶 `qatlas-raw` 用顶层 prefix 分 kind（`pdf/` / `markdown/` / `images/`），管用但有几个痛点 v0.7.0 不能再忍：

1. **三种 kind 的 IO 模式完全不同**：
   - `pdf/` MB 级 blob 多，CDN 友好（实际并不 CDN，但 PUT/GET 走大块）；
   - `markdown/` KB 级文本，详情页每次访问都读，理想 caching layer 高；
   - `images/` 海量小文件（一个 paper 5~50 张图），listing 翻页深、HDD IOPS 敏感。
   单桶意味着这三类 workload 互相干扰，notify webhook 一次 burst 全打到同一个事件流，难做差异化 quota / metrics / rate limit。

2. **bucket-scope policy 表达力**：S3 IAM policy 的 ARN 是按 bucket 钉的。要给 MinerU worker 只发"markdown 桶写 + pdf 桶读"的权限，单桶下只能靠 prefix-condition（容易漏）；拆桶后写两条独立 policy 即可，最小权限边界天然清晰。

3. **bucket-scope event subscription**：RustFS notify webhook 是 per-bucket 订阅（`mc event add` 绑 bucket）。单桶下 pdf/markdown/images 的事件全混在一起，下游 sink 要在事件流里再过滤；拆桶后想关 images 通知只取消那一个 bucket 的订阅，零附加 filter。

4. **统计 / 对账独立**：每个桶有独立的 object count / size 聚合（RustFS 内置 scanner）。混在一起的话 `qatlas-raw` 总 object 数包含三种 kind 一锅烩，没法直观对答"这个 yymm 下 pdf 和 md 的比例对吗"。

5. **share 链接清晰**：私密 share URL 形如 `https://<edge>/<bucket>/<key>`——拆桶后 URL 自带 kind 信息（一眼能看出"这是 pdf"还是"这是 md"），运维抓 log 查私享流量更直观。

### 没拆的另一面（content-addressed 设想未采纳）

老 storage-architecture spike 里设想过 `raw/<sha[:2]>/<sha>.pdf` 的 content-addressed 命名（按 SHA256 寻址，天然 dedup）。**v0.7.0 没采纳这个**，理由：

- arxiv 是天然唯一 ID（不可改、不会撞），按 arxiv ID 寻址比 SHA 寻址更直观（运维肉眼看 key 能猜出是哪篇 paper）；
- 字节级 dedup 在 paper 场景效益低——同一篇 arxiv ID 多版本会改 PDF（v1/v2/v3 不同字节），dedup 主要意义不大；
- content-addressed 要求**任意操作前先算 SHA**，多一次 IO，pipeline 复杂度上升；
- 真要去重只在 upload 路径加 `x-amz-meta-sha256` metadata + 重传同字节短路即可（v0.7.0 已实现），桶布局保持 path-addressed。

历史 spike 笔记保留在 [`storage-architecture.md`](storage-architecture.md) 作为教学材料。

## ACL 与外发策略

| 桶 | 公网直读 | presign 外发 | 字节是否离开后端 |
|---|---|---|---|
| `qatlas-pdf` | ❌ | ❌（share 用 `307` 跳 arxiv.org） | ❌ |
| `qatlas-md` | ❌ | ✅（详情页 / share 链接，TTL 短） | ✅ |
| `qatlas-images` | ❌ | ✅（markdown 内联图 presign） | ✅ |
| `qatlas-openalex` | ❌ | ❌（仅内部 bootstrap / 增量 sync 用） | ❌ |
| `qatlas-raw` | ❌（历史，迁移完后期望停写） | — | — |

> **`qatlas-pdf` 永不外发字节**——这是版权红线：arxiv PDF 字节版权归原作者，QuantumAtlas 只做 metadata + 引用图谱。share 接口对 pdf kind 返回 `307 Location: https://arxiv.org/pdf/<id>.pdf`，让用户直接从 arxiv 拿字节。详见 [`license-and-attribution.md`](../about/license-and-attribution.md)。

## 桶生命周期对账

资产桶之间有应用层约束（v0.7.0 后必须满足，对账思想见 [`bucket-migration-reconciliation.md`](bucket-migration-reconciliation.md)）：

- `qatlas-pdf` 是 truth-of-record——`papers sync` 据其 list 重建 Neo4j `:PaperWork.has_pdf` flag；
- `qatlas-md` 一定子集对应 `qatlas-pdf`（先有 PDF 才会跑 MinerU 出 md，反之不成立——有些 PDF 还没 MinerU）；
- `qatlas-images` 一定子集对应 `qatlas-md`（一篇 paper 没 md 就不会有 images 目录，反之有 md 不一定有 images——MinerU 没提图）；
- `qatlas-openalex` 跟其它桶**无引用关系**，独立增量同步。

任一约束 break → 数据完整性问题，按对账文档的怀疑路径排查。

## 历史迁移：`qatlas-raw` 退役

v0.7.0 上线时，已有的 `qatlas-raw` 桶数据按 kind 分桶迁移到三个新桶（pdf / markdown → md / images）。迁移完成后 `qatlas-raw` 桶**冻结只读**——保留作为 cold backup（不删，万一新桶哪天发现迁移漏对象可以回查），生产 server 不再写。

迁移完成的判定走 [`bucket-migration-reconciliation.md`](bucket-migration-reconciliation.md) 的三个 invariant + 一个语义约束；具体迁移命令 / 脚本 / 工具选择由运维各自实现，本文档不规定。
