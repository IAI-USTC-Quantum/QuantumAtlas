# Claim 的 `references` 用 `kind:id`，服务端通过 `/api/papers/lookup` 精确解析

Claim 增加一个 **`references`** 字段：它引用的文献 works，存成**带命名空间、
无版本的 `kind:id` 字符串**——`arxiv:2208.06941`, `openalex:W4406693713`,
`doi:10.22331/q-2023-03-20-955`。这些 IDs 同时出现在 Claim 的 `claims.json` 和
qatlas-lean 在 Claim 抽取期间消费/产出的 gitea issue 正文中，这会**锁定格式**
（一旦 issues 携带它们，难以回退的成本就是重写 `agony/qatlas-lean` 的 issue 历史）。

解析（`kind:id` → title / authors / year）是**服务端且精确**的：新增
`GET /api/papers/lookup?ids=arxiv:…,openalex:…,doi:…` 批量端点，从**本地
OpenAlex corpus**（ADR `0006`）回答，并为每个 ref 返回 `{ref, title, authors, year, hosted, resolved}`。
自由文本 **fuzzy** 文献检索是刻意**独立、延后**的能力。

## 为什么是带命名空间的 `kind:id`，且无版本

- **自描述前缀** → 解析器直接分发到正确后端（`openalex:`/`doi:`/`arxiv:`），
  而不是从形状推断；它在人读 issue 正文时也可读。它匹配 qatlas-lean 的文献工作流
  和 QA 的只读 papers/corpus 接口面所共享的 lookup 模式。
- **无版本**：一个引用（Reference）引用的是一个 *work*，不是一个快照，所以是
  `arxiv:2208.06941`（没有 `vN`）——这刻意不同于归档论文（paper-of-record）；QA 用代理键
  `papers.paper_id`（ADR `0009`）识别归档论文，而 PDF 版本按资产存在
  `paper_assets.arxiv_version`，不放在 paper id 自身里。
- 一个**引用（Reference）**（这里的文献指针）不同于 lean **reference lemma**
  （语料内 Lean 复用候选 entry——`reference_lemmas`），也不同于 **proof citation**
  （证明说明文字里的 citation，带 `citation_string`）。术语表记录了这个区别。

## 为什么是服务端 lookup，而不是客户端 public API 解析

ADR `0006` 把**完整** OpenAlex corpus 保存在本地，目的正是让“每个 `referenced_works` id
都能本地解析——引用遍历永不 fallback 到 public API”。让客户端对 public API 解析
references，正是 `0006` 要消除的冗余。因此：

- Lookup 是一种 **host-core Paper/corpus 能力**（它补上了现有 `papers/getCitedRefs` stub,
  `hostapi/core.go:89` 的精神），**不是** claim-domain 端点——所以不违反 ADR `0003`。
  它按 ID 读取 OpenAlex corpus；它对 Claims 一无所知。
- 读取它与 QA/lean 边界一致：qatlas-lean 可以运行 Claim 抽取和文献/引用增补，
  但这些工作流只是从 QA **读取** paper bytes 和 corpus metadata；它们不会让
  QA 起草或持久化 Claims、proofs 或 Theorems。QA 提供只读 paper/corpus 基底；
  lean own claim/proof/theorem 工作流。
- **优雅降级**（镜像 ADR `0006`）：当 corpus 不可达时，qatlas client 可以在该会话中 fallback
  到 public OpenAlex API。

## 证明器如何访问 QA：优先 CLI/HTTP（+ MCP），保留只读 DB

证明器（qatlas-lean）通过**两种被认可的只读模式**读取 QA 的 Paper catalog + OpenAlex corpus，
按优先级排序。lean 永不写 QA 存储——QA own paper md / catalog / corpus，lean own
claims/proofs/theorems；QA 永不起草或维护 lean 的内容（反之亦然）。

1. **CLI / HTTP — 首选，默认。** qatlas client（`qatlas paper …`）以及 REST 接口面
   （`GET /api/papers/lookup`, `/api/papers/{id}/markdown`, `/stats`, `/needs-mineru`），使用
   `papers:read` PAT。lean **不持有 QA DB credentials**，因此 QA 可以在稳定 HTTP 合约
   之后自由演进物理 schema。一个**新的 QA MCP server** 把同一个读取接口面包装成
   自描述工具（MCP tool schema *就是* 文档），让 qatlas-lean 的 Claim 抽取与
   文献/引用工作流不用直接 DB access 也能消费 QA 的 paper/corpus data。
2. **Read-only SQL — 保留，次级。** QA PostgreSQL 上的直接只读 role
   （`papers` + `paper_assets` + `openalex_works`——ADR `0009` 重构后的 catalog，以及 ADR
   `0010` 的 corpus，后者删除了 `work_referenced` edge table），**只**在某个 query 需要
   HTTP 无法高效表达的 join/scan 时使用。这**不再是 “deferred”**：它是保留并文档化的能力——
   但它是 fallback，不是默认，因为它把 lean 耦合到 QA 的物理 schema。

## 为什么是 `/api/papers/lookup`（文献总入口），不是 `/api/works` 或 `/api/openalex`

- 这个端点是现有 `/api/papers/{path...}` catch-all 下的**仅 path 特例**，
  与 `/api/papers/stats` 和 `/api/papers/needs-mineru` 并列；`/api/papers/{id}/markdown`
  以及其余按 id 取资产的接口面不受影响。
- lookup 覆盖**更广义的文献**（OpenAlex corpus 里大多是 QA **不** host 的 works），并用
  `hosted` 标记每个命中。一个 QA 尚未 host 的 corpus 命中之后可以触发后台 PDF 下载 +
  MinerU 转换，并**成为** hosted Paper——因此用单个 `/api/papers/` 总入口同时覆盖
  “查找文献” + “获取 hosted Paper 的资产” 是一致的。
- 术语表仍然把 **Paper catalog** 和 **OpenAlex corpus** 作为不同的 *数据存储*
  （ADR `0006`）；`/api/papers/` URL 只是文献 *总入口*。`works` / `openalex` 作为
  资源名词被否决：`works` 读起来像实体而不是操作，`openalex` 会把 vendor name 焊进 URL。

## 为什么本迭代只做 exact；fuzzy search 延后

引用（References）从**源论文**中抽取，而源论文通常会打印 arXiv ids / DOIs，所以
**按 id 精确 batch lookup** 高效（corpus by-id SQL）且当前足够。把一个**无 id 的自由文本 citation**
解析到正确 work（fuzzy matching）是更难的独立问题；它被拆成未来的 `/api/papers/<search>` 能力，
**本迭代不构建**。

## unresolved Reference 为什么带 warning 提交，而不是阻塞

引用（References）是为 Claim **增补信息**；它们不是正确性门控。OpenAlex 覆盖缺口（全新 preprint、
无 DOI 的书、打错的 id）不应阻止提交一个其它方面良好的 Claim。contrib WebUI 对 unresolved
Reference 显示 warning；人来删除 / 编辑 / 保留它。保留下来的 unresolved Reference 以原始
`kind:id` 和 `"resolved": false` 存储。

## 备选方案

- **Bare ID（推断 kind）** / **full URI** — 否决（见“为什么是带命名空间的 `kind:id`”）。
- **客户端 public-API 解析** — 否决：与 ADR `0006` 的本地 corpus 冗余；还会把我们已经持有的数据的负载分摊到每个用户的 OpenAlex key 上。
- **新增 `/api/works` 或 `/api/openalex` resource** — 否决，改用 `/api/papers/` 文献总入口
  + `hosted` flag。
- **直到每个 Reference 都解析成功才允许 Confirm** — 否决：面对 corpus 覆盖缺口太脆弱。

## 影响

- `GET /api/papers/lookup` 作为 `/api/papers/{path...}` 下的仅 path 特例实施。
  `papers/getCitedRefs` stub 的角色被澄清为以 paper 为中心的 “paper X cite 了哪些 works” query，
  其结果 items 自身也可通过 `lookup` 解析。
- `claims.json` 和 gitea issue body 增加一个 `references` list，元素为 `kind:id`（加一个
  `resolved` flag）；格式由 issue history 锁定（上线前不欠 backwards-compat，但 issue
  history 是实际锁）。
- **证明器访问模型已决策（取代早期 “read view deferred”）。** 证明器通过
  **优先 CLI/HTTP（+ 一个新的 self-documenting MCP），保留 read-only SQL role 作为次级路径**
  读取 QA（见“证明器如何访问 QA”）。早期 deferral 已解决：DB path 是 *retained, not deferred*，
  但降级到 CLI/HTTP 之下。因此原交接里的 “design the prover's PG read schema this pass”
  被重新收束为次级路径的**只读 schema contract**，而不是 bespoke prover DB view。
- **两条访问路径都要文档化。** CLI/HTTP：`qatlas paper` CLI docs（`docs/client/`）+
  REST API docs（`docs/server/rest-api.md` + `/swagger/` 上生成的 OpenAPI/Swagger，
  由 `pixi run swagger` 重新生成）+ 新 MCP server（self-describing tool schemas）。
  Read-only SQL：发布只读 schema contract（`papers` / `paper_assets` / `openalex_works`
  的稳定可读子集——ADR `0009`/`0010`）+ read-only role grant。
- **新增构建项——覆盖 papers read surface 的 QA MCP server**（lookup + markdown + stats），
  纯新增（QA 目前没有 MCP）。它把同一个 `papers:read` HTTP capability 重新暴露为 MCP tools；
  它**不开放任何写入路径**。
- **Fuzzy free-text literature search 是已知未来能力**，本迭代不构建。
- qatlas-lean 仍然是运行 Claim 抽取和文献/引用工作流的地方；QA 只通过
  CLI/HTTP、MCP，以及上述次级 read-only SQL path 暴露只读 paper/corpus 基底。
