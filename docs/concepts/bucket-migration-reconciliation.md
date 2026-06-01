# 桶迁移对账（Bucket Migration Reconciliation）

> **范围**：v0.7.0 拆桶（`qatlas-raw` → `qatlas-{pdf,md,images}`）后历史资产迁移完毕的**完整性核查**思想，以及增量迁移、`papers sync` 后 Neo4j catalog 跟桶状态对账的通用原则。具体核对的脚本 / 命令 / mc / boto3 写法**不在本文档**——由运维各自实现，本文档只给 invariant 和判定规则。

## 为什么要做对账

S3 兼容存储的批量迁移（`mc mirror`、`mc cp --recursive` 等）在弱后端（HDD IOPS、低性能 CPU、并发抢占）上不保证零失败：

- 单个 prefix 内部分对象因 server 端 IO timeout 被 client 标 fail，但其它 obj 已成功 transfer
- listing API 在大桶上可能超时翻页中断，client 报 success 但**实际只 cover 部分 prefix**
- 多次 retry / restart 后已 done 的对象会被 mc skip，但**漏 cover 的对象不会自动补**
- `papers sync` 报告 `image objects: 0` 不一定是真的 0，可能是 list API 在某次返回 timeout 被 silently swallowed

**结论**：迁移命令 "DONE" 字样不等于完整。要靠**独立对账**确认。

## 三个 invariant（pdf / md / images）

迁移完成后，对每一类资产（pdf / markdown / images）都应满足：

```
count(target bucket recursive) >= count(source bucket recursive prefix)
```

不等号是 `>=` 不是 `==`，因为 v0.7.0+ 生产环境持续在写新桶（mirror 期间和之后），target 含 source + v0.7.0+ 自然增量。

判定：

- `target >= source` → mirror 至少覆盖了 source（OK）
- `target < source` → **mirror 漏对象**，需要排查 fail 集合
- `target == source` → 可能 OK 也可能 mirror 漏 + 生产没新写（取决于桶活跃度）

## 一个语义 invariant（md vs images）

布局约定：

- `qatlas-md/<yymm>/<stem>.md` — 每篇 paper 一个 markdown 文件（flat）
- `qatlas-images/<yymm>/<stem>/<sha>.jpg` — 每篇 paper 一个目录，目录里 N 张图（N ≥ 0）

每个 image 目录 `<yymm>/<stem>/` 都对应一篇 paper（按 arxiv stem 索引），但**反过来不成立**：

- 有些 paper 没有 image（MinerU 没提取出图 / 这类论文本来就没图） → md 文件存在但 images 下无目录
- 有些 paper 有 image → md 文件 + images 下有同 stem 目录

所以语义 invariant：

```
{ <yymm>/<stem> | qatlas-images/<yymm>/<stem>/ 存在 }
  ⊆
{ <yymm>/<stem> | qatlas-md/<yymm>/<stem>.md 存在 }
```

数量上 `count(image paper-level dirs) <= count(md files)`。

判定：

- 包含关系成立（image dirs ⊆ md stems）→ OK
- 反例（image 有但 md 无）→ **数据完整性破坏**：上传 image 时漏写 md，或 mirror 漏 md，或 image 来自别处（不是同 paper 体系）
- image / md 比率异常低（如 < 5%）→ MinerU 没跑或解析失败率高（需要核 MinerU pipeline），不一定是 mirror 问题

## 推荐的数对象方式

**优先用 server 端聚合**：S3 server（RustFS / MinIO 等）一般有内置 bucket scanner 周期性扫描每桶，统计 size + object count 持久化到 metadata。手动触发或读最近一次扫描结果，得到**权威数字**——比 client listing 快几个数量级、不受网络中断影响。

**不要用 `mc ls --recursive | wc -l`** 数大桶：

- 200k+ 对象桶上 client listing 极慢甚至 timeout
- 翻页中断 client 不一定察觉，统计偏少不报错（silent partial result）
- 后端 CPU/IO 在 listing 期间被占用，影响别的 op

如果 server scanner 暂不可用（或扫描数据陈旧），fallback **按顶层 prefix 分批 list 累加**——单 prefix 通常几百到几千对象，listing 不易 timeout。

## 失败模式 → 怀疑路径

| 对账失败 | 怀疑顺序 |
|---|---|
| `target pdf/md < source` 同前缀 | 1. mirror 漏（看 mirror log fail list） 2. source 还在被写（应该 v0.7.0+ 不允许）3. mc skip 误判某些 obj 已存在 |
| `target images < source` | 同上 + listing timeout（images 桶常因小文件多 prefix 深 + HDD 慢） |
| image dirs > md files | 1. 上传 pipeline 漏写 md 2. md mirror 段漏 3. 历史脏数据（v0.7.0 之前的 stale image） |
| image/md 比率突然降到 < 5% | 多半 MinerU pipeline 问题（image 提取失败），不是 mirror 锅 |
| Neo4j has_pdf/has_md flag 数 << 桶对象数 | `papers sync` 没跑过 / 跑过但 list 失败被 silently swallowed（用 fail-loud 版本 rerun）/ Neo4j 节点本身缺失 |

## 跟硬约束 3 的关系

[storage-architecture.md](storage-architecture.md) 硬约束 3 = **source of truth 是 RustFS + Wiki repo，Neo4j 是可重建的派生 index**。对账正是这个约束的运行时验证：

- 桶里实际有什么 = source of truth
- Neo4j 节点 + flag = 派生 index
- 对账失败 → Neo4j 偏离 source of truth → 跑 `qatlas-server papers sync --full --from-rustfs` 重建 / 修复

定期对账是 catalog 健康度的早期信号——比"用户报 404"提前发现迁移漏洞、上传 pipeline bug、sync 静默 swallow error 等问题。

## 何时跑对账

- 任何一次 batch migration / mirror 完成后（首次必跑）
- 上线新版 server 改了 upload pipeline / sync 逻辑后
- 怀疑某次 `papers sync` 报告异常（如 `image objects: 0` 但桶里明明有图）
- 定期（季度）作为 catalog 健康度巡检
- `papers sync` 出 fail-loud error 后修完代码 → rerun + 对账
