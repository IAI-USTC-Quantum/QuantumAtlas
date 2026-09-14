# 按 id 取资产：PDF 默认直链、md/JSON 默认字节流，均可复写

_实现 ADR `0007` 决定的读取界面，基于 ADR `0009` 的 `papers`/`paper_assets` schema。_

> **状态注记**：本文关于 PDF 交付（默认直链 / `?format=bytes` 复写）的描述是
> **停用前的历史行为**——PDF 分发后来被设计性停用，`GET /pdf` 现在恒返
> **410 Gone**（PDF 仍作为内部资产服务 MinerU 转换与贡献者 lease）。
> markdown 侧的决策（默认字节流、`?format=link` 复写、LRO suspend-and-wait）
> 仍然有效。

`GET /api/papers/{id}/markdown`（以及 `/pdf`）已经遵循 async LRO contract——`202 Accepted`
+ `Operation-Location` + `Retry-After`，轮询 `…/status`，完成后重新 GET。ADR `0009` 为每个
work 提供一行 catalog row 和一个子 `paper_assets` 表后，读取路径会**经由 catalog 解析**
（`papers` → `paper_default_asset_id` → asset 存储的 `*_path` key）。catalog 只存 RustFS
**对象键**；调用者如何拿到内容，由两个正交问题决定：**(1) QA 是否允许提供它**（server config
中的合规门控）以及 **(2) 以字节流还是以直链提供**（技术/UX 选择，与合规无关）。

## 决策

- **按 id 经由 catalog 解析。** 通过三类 external ids 中任意一种读取（`arxiv` / `doi` /
  `openalex`，在 `papers` 上各自 `UNIQUE`）时，解析链路是 `papers` →
  `paper_default_asset_id` → asset 存储的 key（`pdf_path` / `mineru_md_path` /
  `mineru_json_path`）。default-asset pointer（ADR `0009` Q21：published-first，否则 latest arXiv）
  决定裸 by-paper read 服务的是*哪一个* asset。批量 id → metadata 解析仍留在
  `GET /api/papers/lookup`（ADR `0007`）。

- **合规由访问门控 `QATLAS_PAPER_ACCESS_ENABLED`（server config）决定。** QA 是否对外提供
  paper content 完全在这里决定，再分发义务也在这里处理：
  - **OFF**（默认；公开 quantum-atlas.ai 姿态）：QA 不做再分发。arXiv 论文的 **PDF**
    读取返回规范 **`arxiv.org/pdf/<id>vN` link**（由来源方分发，不是 QA）；没有 arXiv
    source 的 PDF（published/DOI-only）**不提供**。**markdown / JSON** **不提供**。
  - **ON**（operator 选择开启，并接受 derivative-work distribution obligation）：PDF、markdown
    和 JSON 都提供。

- **字节流 vs. 直链是正交的逐请求选择，不是合规决策。** 一旦门控允许提供内容，字节如何到达调用者就是纯技术问题；
  每类资产有默认值，也有 REST 复写：
  - **PDF → 默认：RustFS 直链。** 该 link 是 server-configured RustFS public URL prefix
    （`QATLAS_S3_PUBLIC_ENDPOINT`，所有 clients 都可达）拼上存储的 `pdf_path`。默认给 link，
    是因为 PDF 是大二进制，更适合由对象存储直接服务，而不是经 qatlasd proxy。
  - **markdown / JSON → 默认：字节流。** handler 读取 `mineru_md_path` /
    `mineru_json_path` 并流式传输 bytes（`text/markdown`、`application/json`）——这些是小文本，
    API 适合 inline 提供。
  - **通过 REST query parameter 复写**（`?format=link|bytes`）：调用者可以请求 PDF **bytes**
    （经 qatlasd proxy）或 markdown/JSON **link**。在无法 presign / 没有 public prefix 的 backend
    （LocalStore dev）上，link request 会降级为 bytes。

- **Lazy suspend-and-wait，而不是 404-on-miss。** 当解析出的 asset 缺少请求的 artifact
  （还没 fetched PDF，或还没有 `mineru_md_path`）时，保留现有 LRO：`202` + `Operation-Location`，
  后台 silent fetch-PDF + MinerU convert，调用者轮询 `…/status` 直到 ready。这样，一个 QA 尚未托管的
  corpus hit 可以*变成* hosted（ADR `0007`），而不是 hard-missing。

- **文档自动生成。** 路由的 OpenAPI 桩位于 `internal/routes/openapi.go`，由
  `pixi run swagger` 重新生成（CI 漂移检查）；面向人的参考文档是 `docs/server/rest-api.md`。

## 为什么这两个问题要分开

- **合规完全落在 access gate 里。** `QATLAS_PAPER_ACCESS_ENABLED`（以及 OFF 时的
  arxiv-link / not-served 行为）负责避免 QA 再分发不该再分发的内容。这个决策在 server config
  中一次性做出，不依赖 transport。
- **字节流 vs. 直链永远不改变披露内容**——两种方式交付的是同一份已被允许的内容；差别只是由谁搬运字节
  （qatlasd 还是对象存储），以及 client contract 有多方便。因此它是技术/UX knob，不是合规 lever，
  可以安全地暴露为逐请求选项。

## 为什么选择这些默认值（PDF 直链，markdown/JSON 字节流）

- **PDF 直链让 qatlasd 离开大二进制路径。** PDF 很大；返回 RustFS URL 可以让对象存储
  直接服务它们。所有 clients 都能访问配置好的 RustFS endpoint，因此没有可达性限制。
- **markdown / JSON 字节流让 agents 更简单。** 消费方（qatlas-lean 的 scout/enrich）想要的是文本，
  不是再跳一次；inline 流式传输这些小型 derived artifacts 是有用的默认值。
- **二者都可复写**，因为默认值都不是普适的：想要单个 authenticated hop 的调用者可以强制 PDF bytes；
  想把 URL 继续传下去的调用者可以强制 markdown/JSON link。

## 为什么保留 LRO，而不是 artifact 缺失时返回 404

References 和 reads 的目标是 *works*；QA 在 OpenAlex corpus 中持有某个 work、但尚未 fetch/convert，
是正常且可恢复的状态——suspend-and-wait contract 让第一个 reader 触发 materialisation 并拿到结果，
这正是 ADR `0007` 依赖的 agent-friendly 行为（"a corpus hit … can later … become a hosted Paper"）。

## 备选方案

- **把字节流 vs. 直链归因于合规（本 ADR 的早期草案）。** 否决：合规已由访问门控
  执行；transport 选择不披露任何额外内容，因此把它绑到合规是错误的，还会不必要地禁止例如给
  已获许可的调用者提供 PDF byte-stream。
- **所有内容都作为字节流提供（现状）。** PDF 场景否决：这会让 qatlasd proxy 每个本可由
  对象存储直接服务的大 PDF 二进制。
- **所有内容都作为 links 提供。** markdown/JSON 场景否决：对 API 可直接 stream 的小文本增加了不必要的 hop
  + presign dependency，并且在 LocalStore 上会破（no presign）。
- **artifact 缺失时返回 404。** 否决：破坏 lazy-materialise contract。

## 影响

- **`/pdf` success shape changes（Phase C）。** 默认情况下，ready PDF 返回 direct link（gate OFF
  时为 arXiv redirect；ON 时为 RustFS URL），而不是 `application/pdf` bytes——这会 breaking
  当前 byte consumers（pre-launch，可接受），可用 `?format=bytes` 切回。`/markdown` 继续
  streaming bytes，并获得一个 sibling `/json` byte endpoint（现在已有
  `paper_assets.mineru_json_path`），二者都支持 `?format=link`。`docs/server/rest-api.md` +
  OpenAPI 规范会重新生成。
- **依赖 ADR `0009`。** handlers 读取 `papers`/`paper_assets`；它们随 Phase A 一起实施，不会早于
  Phase A。
- **没有新的 write path。** 这些是 `papers:read` 下的读取能力，符合 QA/lean 边界（ADR `0007`/`0008`）：
  QA 暴露只读 paper substrate；它永远不 author lean content。
