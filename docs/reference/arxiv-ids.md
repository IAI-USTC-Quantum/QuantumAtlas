# arXiv ID 格式

QuantumAtlas 以 arXiv ID 为论文的主键。文本侧支持两种 ID 格式 + 版本后缀；存
储侧把每条 ID 拆成「年月分片 / 子领域 / 文件 stem」三段做 sharding。本文档讲
完整规则，包括 2026-06 引入的**按子领域分目录**新布局以及背后的根因。

## 1. 两种格式

| 时代 | 格式 | 例子 |
|---|---|---|
| **老式**（≤ 2007-03） | `<category>/<7-digit>` | `quant-ph/9508027`、`hep-th/0207001`、`cs.AI/0101001`、`physics.atom-ph/0001001` |
| **新式**（2007-04 起） | `<YYMM>.<4–6-digit>` | `2501.00010`、`2403.12345`、`2401.123456` |

注意 **老式 ID 的 `category` 含斜杠**，所以会出现在 URL path 里。server 端
`splitPapersPath` 按"最后一段"匹配 action / arxiv_id 切分能正确处理。

### 1.1 老式 category 词表

支持的 category 形态（按 `internal/paperassets::oldStyleCategoryRE` 实现的语法）：

| 形态 | 例子 | 说明 |
|---|---|---|
| 无子分类 | `quant-ph`、`hep-th`、`gr-qc`、`math-ph` | 主题字段为 lower-case，可含 `-` |
| 大写 2 字母子分类 | `cs.AI`、`math.AG`、`q-bio.NC`、`nlin.CD` | 子字段 `\.[A-Z]{2}` |
| 小写连字符子分类 | `physics.atom-ph`、`cond-mat.stat-mech` | 子字段 `\.[a-z][a-z\-]*` |

> A1 之前的正则只覆盖前两种，碰到 `physics.atom-ph/0001001v1` 这类直接 reject。
> 现在统一收下。

## 2. 版本后缀 `vN`

任何 arXiv 论文都可以有多个版本（v1、v2、...）。

| 入口 | 是否要求 `vN` |
|---|---|
| `qatlas ingest <id>` | 可不带；server 取 arXiv 当前最新版 |
| `qatlas upload pdf <id>` | **必填**；对象寻址按 `<id>v<n>` 命名 |
| `qatlas upload mineru <id>` | **必填** |
| `qatlas mineru <id>` | **必填** |
| `GET /api/papers/{id}/markdown` / `/pdf` | 可不带；server 取 catalog 内最新版（多版本时在响应里显式标）|
| Wiki paper page `paper-arxiv-<id>v<n>` | **必填**（不同版本是不同页）|

## 3. 对象寻址映射（post-A1 layout）

server 把 ID 拆成 `<YYMM-shard>/<可选 category>/<stem>.<ext>`：

| arxiv_id | object key（PDF） | 说明 |
|---|---|---|
| `2501.00010v1` | `pdf/2501/2501.00010v1.pdf` | 新式扁平 |
| `2403.12345v2` | `pdf/2403/2403.12345v2.pdf` | 新式扁平 |
| `quant-ph/9508027v1` | `pdf/9508/quant-ph/9508027v1.pdf` | **老式带子目录** |
| `hep-th/0207001v3` | `pdf/0207/hep-th/0207001v3.pdf` | **老式带子目录** |
| `cs.AI/0101001v1` | `pdf/0101/cs.AI/0101001v1.pdf` | 含子分类的老式 |

`md/` / `json/` / `images/` 子目录采用**同样的三段布局**（kind + shard + 可选
category + stem）。完整规则在 `internal/paperassets/path.go::AssetKeyFor`。

`<YYMM>` 跟 [arxiv.org bulk-data 目录约定](https://info.arxiv.org/help/bulk_data.html) 一致。
带 category 子目录是 QuantumAtlas 在 A1（2026-06）后引入的额外维度，**bulk-data 上游
没有这一层** —— 我们引入它是为了避免下一节描述的跨子领域冲突。

### 3.1 为什么老式 ID 要保留 category 子目录

**老式 7-digit number 不是全局唯一的，只在 category 内唯一**。同一个 `YYMMnnn`
在不同 category 下对应**完全不同**的论文：

| arxiv URL | 标题 |
|---|---|
| `arxiv.org/abs/quant-ph/0207065` | Relation between classical communication capacity and entanglement capability for two-qubit unitary operations |
| `arxiv.org/abs/hep-th/0207065` | Does curvature-dilaton coupling with Kalb Ramond field lead to an accelerating Universe? |
| `arxiv.org/abs/math/0207065` | A duality proof of Tchakaloff's theorem |
| `arxiv.org/abs/gr-qc/0207065` | Gravitomagnetic effects |

剥掉 `category` 后写到同一个 bucket key（`pdf/0207/0207065v1.pdf`）= **silent
overwrite**。A1 之前确实是这么存的；当前生产之所以没踩，纯粹是因为 OpenAlex
bootstrap 加了 quant-only filter。一旦扩到 hep-th / cond-mat 等任意第二个子领域，
立刻冲突。

A1 的 `pdf/<yymm>/<category>/<stem>.<ext>` 子目录布局从结构上彻底排除这种冲突。

### 3.2 新旧布局对照

| arxiv_id | A1 之前（legacy） | A1 之后（new） |
|---|---|---|
| `2501.00010v1` | `pdf/2501/2501.00010v1.pdf` | `pdf/2501/2501.00010v1.pdf` *(unchanged)* |
| `quant-ph/9508027v1` | `pdf/9508/9508027v1.pdf` | `pdf/9508/quant-ph/9508027v1.pdf` |
| `hep-th/0207065v1` | `pdf/0207/0207065v1.pdf`（冲突！）| `pdf/0207/hep-th/0207065v1.pdf` |

新式 ID 的 layout 不变（数字本来就全局唯一）。仅老式有变更。

### 3.3 Dual-read 过渡期

A1 后立刻部署 server 不会 break 已有数据：

- **读路径** (`paperassets.LocateAsset` / `LocateAssetByID`) 先查新 layout，未命中
  时对老式 canonical ID **回退**查 legacy bare key
- **写路径** (`AssetKey` / `AssetKeyFor`) 总是写新 layout，不写 legacy

也就是说：

- 老对象（pre-A1，bare layout）→ 通过 fallback 仍可读
- 新写入（post-A1）→ 都进 per-category 子目录
- migration 把 legacy 对象 `mc cp` 到新位置（manifest-based，三阶段；plan §4
  Phase E）→ 同步更新 catalog 主键
- 一个 release 后移除 dual-read fallback 路径

dual-read 命中由 `paperassets.LegacyLayoutReads()` 计数，运维可观测 migration 进度。

## 4. kind 后缀

每篇论文除了 PDF 还可能有：

| kind | key 模板（新式） | key 模板（老式 canonical） | 内容 |
|---|---|---|---|
| `pdf` | `pdf/<yymm>/<stem>.pdf` | `pdf/<yymm>/<category>/<stem>.pdf` | 原始 PDF |
| `markdown` | `markdown/<yymm>/<stem>.md` | `markdown/<yymm>/<category>/<stem>.md` | MinerU 解析的 markdown |
| `json` | `json/<yymm>/<stem>.json` | `json/<yymm>/<category>/<stem>.json` | arXiv 元数据（题目 / 作者 / abstract） |
| `images` | `images/<yymm>/<stem>.zip` | `images/<yymm>/<category>/<stem>.zip` | MinerU 解析出的图片 zip |

`qatlas upload pdf --pdf` 把 PDF 字节落到 `pdf/`。论文元数据（题目 / 作者 /
摘要 / 引用）走 OpenAlex 上游同步进 Neo4j catalog，不再通过 upload 端点写
`json/`（v0.7.0 起；该前缀仅保留兼容历史对象的读路径）。

## 5. 规范化与结构化解析

`internal/paperassets` 提供两层 API：

- **轻量 helpers**（保留兼容历史调用方）：
  - `NormalizeIdentifier(s)` — 去空白 + 剥 `arXiv:` 前缀
  - `StripVersion(s)` — 去掉尾部 `vN`
  - `SafeKey(s)` — 把 `/` 换成 `__` 给老式磁盘 fallback 用
- **结构化解析**（新代码优先用）：
  - `Parse(s) (ParsedArxivID, error)` — 拆成 Category / Stem / StemBase / Version / YYMM 等字段
  - `MustParse(s)` — 测试常量用，invalid 输入直接 panic
  - `AssetKeyFor(kind, p)` — 给 `ParsedArxivID` 渲染对象 key
  - `LegacyAssetKeyFor(kind, p)` — 给 dual-read fallback 用的 pre-A1 key
  - `LocateAsset(ctx, store, kind, p)` — 一站式 read 接口：自动新 layout 优先 + legacy fallback

老式 bare ID（`9508027v1`，无 category）由 `Parse` 标记 `IsBare=true`，调用方决
定怎么处理：

- **upload 入口** (`ValidateUploadID`)：当前**仍兼容**接受 bare（issue #4 缓解）
- **storage 入口** (`AssetKeyFor`)：bare 输入回退到 legacy layout `pdf/<yymm>/<stem>.<ext>`
- **未来**（plan §H3 / [issue #12](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/12)）：catalog migration 完成后 `ValidateUploadID` 拒 bare，由上层 resolver 反查 Neo4j 拿到 category 再调用

## 6. 常见坑

!!! failure "upload 时漏掉 `v1`"
    ```
    qatlas upload pdf 2501.00010 --pdf paper.pdf
    ```
    server 返回 400 `arxiv_id must include version suffix`。改成 `2501.00010v1`。

!!! failure "用了 `arxiv.org/abs/...` 完整 URL"
    server 只接受 ID 部分。从 URL `https://arxiv.org/abs/2501.00010` 抽出
    `2501.00010` 就行。

!!! success "老式带版本是合法的"
    `quant-ph/9508027v1`（含斜杠 + 版本）—— 是合法的，正常工作。

!!! warning "老式 bare 形态（`9508027v1`，无 category）当前仍接受但**有歧义**"
    由 [issue #4](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/4)
    历史原因，server `ValidateUploadID` 仍兼容裸形态。但裸 7-digit 号在不同子
    领域下指向**不同论文**（见 §3.1）；当前仅靠"catalog 只有 quant-ph"这个事
    实保证不冲突。新代码 / 客户端**建议永远带 category 前缀**调用。
    [issue #12](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/12) 跟踪
    最终拒绝裸形态的工作。

!!! info "DOI 入口同一个端点"
    `GET /api/papers/{id_or_doi}/markdown` 自动 detect DOI（`^10\.\d{4,9}/`），
    经 OpenAlex 反查成 arxiv_id 后走同一套 handler。详见 [REST API · DOI 寻址](rest-api.md#doi-addressing)。

## 7. 跟 Wiki page id 的关系

Wiki paper 页面 id 规则：`paper-arxiv-<规范化 id 含 v>`，斜杠转 `-`。

| arxiv_id | Wiki page id |
|---|---|
| `2501.00010v1` | `paper-arxiv-2501.00010v1` |
| `quant-ph/9508027v1` | `paper-arxiv-quant-ph-9508027v1` |
| `cs.AI/0101001v1` | `paper-arxiv-cs.AI-0101001v1` |
| `physics.atom-ph/0001001v2` | `paper-arxiv-physics.atom-ph-0001001v2` |

文件名同上，`.md` 结尾。Wiki page id 没有 dual-read 过渡期问题——它从来都按
canonical 形态拼接（包含 category）。
