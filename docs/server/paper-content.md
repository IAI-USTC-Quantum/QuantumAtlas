# 论文内容：固定 PDF、完整解析包与可续读视图

本文是当前内容 API 的契约。旧三桶路径、覆盖写、`GET /pdf` 恒 410、只接收
`full.md + images` 的说明只作为历史记录保留，不适用于本契约。

## 身份与存储边界

- `paper_id=qa_…` 标识作品，不表示某个 arXiv 版本或某次解析。
- `source_id` 绑定**确切 PDF 字节及完整 SHA-256**。同一 paper 下同 SHA 的新登记
  复用已有 source ID；既有评论及其 source 不换号。arXiv `vN` 是上游语义版本，
  `origin` 可以是 `arxiv:vN` / `arxiv:IDvN`，不是对象存储的 `S3VersionId`。
  两个语义版本字节相同可以共享 source；不同字节不能冒充相同冻结来源。
- `revision_id` 标识一次不可变完整解析发布。重解析或重上传产生新 revision；
  `is_current` 是可变选择指针，不能改写旧 revision 或迁移历史评论锚点。
- `S3VersionId` 是存储后端的物理对象版本，只供运维诊断/恢复，不是 source ID、
  arXiv `vN`、parse revision 或公开 API 的版本选择参数。S3 versioning 不能代替
  上述应用级不可变身份，也不允许通过恢复覆盖已冻结的 API 内容。

新字节放在独立内容桶 `qatlas-content`（本地部署使用相同逻辑命名空间），逻辑键：

```text
content/{paper_id}/{source_id}/source.pdf
content/{paper_id}/{source_id}/parses/{revision_id}/files/{producer_original_relative_path}
content/{paper_id}/{source_id}/parses/{revision_id}/manifest.json
```

文件成员保留生产者的相对路径、原文件名与原字节，包括 Middle、Markdown、
Structured Content/ContentList、图片和未知合法成员。服务端不把 `layout.json`
改名为 `middle.json`，不把任意 JSON 改名为 `content_list.json`，不重序列化原件。
生成的 `manifest.json` 独立于 `files/`，不会占用或覆盖生产者同名文件。

成员逐个 create-only 写入并从持久存储复核 SHA/大小；**所有成员核验后才写 manifest，
最后才在 PostgreSQL 发布完整 revision 并切换 current**。PG 不可用时上传失败，
不能返回“已成功，索引稍后补登记”。只完成对象写入但未发布的内容不是 ready。

## 开关、认证与懒迁移

所有 PDF、正文、raw JSON、完整包成员、block 原件/裁图、图片与 figures 都受
`paper_access.enabled` 和当前登录身份的 `papers:read` 约束；关闭返回 404，不能
以 `qa_`、别名、Range、历史 revision 或客户端缓存绕过。目录元数据及评论信息
不因此变成原件交付入口。配置字段及部署方法见[服务端配置](server-config.md)。

- 数据库迁移只增加表、指针及约束，不批量下载、转换或回填历史资产。
- 首次内容/PDF访问按需读取旧库的**PDF**，计算 SHA 并冻结到新内容命名空间；
  旧 MD、JSON、图片不复制、不作为新 ready 的证据。旧桶/旧文件保持原样不删除。
- 已有冻结来源缺失、摘要错误或存储不可达时 fail closed，不能回退旧 PDF、恢复
  另一物理版本、替换为最新 arXiv PDF或重新解析来掩盖问题。
- 已存 PDF 的读取不需要 MinerU token，也不做推理。只有显式正文/内容访问可触发按所选
  source 的懒解析；状态轮询不创建下载/解析任务。没有 nightly、boot、入库或下载归档
  自动推理，也不做 bulk 懒迁移；管理员明确调用 RunNow/batch 仍是受控的独立操作。
- 历史 `/parses/{revision}/json` 继续服务明确固定的评论原件，不参与新完整包
  readiness 判断。新读视图及完整成员 API 仅接受已发布且核验的完整包。

## 固定来源 PDF

```text
GET /api/papers/{id}/pdf?source_id=SOURCE_ID
GET /api/papers/{id}/pdf?version=v2
GET /api/papers/{id}/sources/{source_id}/pdf
```

`id` 可为 `qa_`、arXiv 或 DOI 等可解析别名。`source_id` 与 `version` 互斥；显式
pin 无匹配或存在歧义时拒绝，不换成另一个 source、最新版或出版版。无需先调用
source-list，旧 PDF 也可按需冻结后直接读取。默认成功为鉴权 `application/pdf`
字节流（200；合法 Range 为 206，使用相同认证），不是外部预签名链接，也不是 410。
`?format=link` 只返回同源、固定 source 的 `/api/papers/{qa}/pdf?source_id=S&format=bytes`
认证下载位置，不暴露桶地址或外部 presign；每次跟随链接仍检查 gate/auth。

响应通过 `X-QAtlas-Paper-Id` / `X-QAtlas-Resolved-Id`、`X-QAtlas-Source-Id`、
`X-QAtlas-Source-Origin`、`X-QAtlas-Sha256` / `X-QAtlas-PDF-SHA256` 给出身份与摘要。
完整文件可按 SHA 校验；部分 Range 不是完整 PDF 摘要的校验单位。

```bash
qatlas paper pdf qa_… --source-id src_… -o paper.pdf
qatlas paper pdf 0811.3171v2 --version v2 --no-cache -o paper-v2.pdf
```

CLI 对规范 ID 和别名均逐块计算 SHA，校验通过才输出。缓存按服务+规范 paper+SHA
寻址；每次命中缓存前仍取得当前服务端鉴权/开关许可响应，禁用或 401/403 不会
伪装为缓存成功，且不会删除此前已合法下载的文件。

## Middle 派生 JSON 阅读窗口

```text
GET /api/papers/{id}/read?source_id=S&revision=R&page=5&block=12&limit=30000
GET /api/papers/{id}/read?cursor=OPAQUE_CURSOR
```

参数均可省略：`source_id` 固定 PDF；`revision` 固定解析；`page`/`block` 为 **1-based**
页码和生产者原始 block index（非连续索引不重编号），block 必须带 page；`cursor`
是服务端返回的不可改写游标；`limit` 是 Unicode 正文字符预算，默认 30000，范围
1..100000。HTTP 亦支持共享来源解析器的 `version=vN` 语义 pin，与 `source_id` 互斥。

200 JSON 保留：

- 字符串 `paper_id`、`source_id`、`revision`、`source_sha256`、`artifact_sha256`、
  可选 `bundle_sha256`、`renderer`、`format=markdown`、`content`；
- `request_scope`（选择 page/block、limit、可选 cursor）；
- `content_ranges[]`（page/block、start_offset/end_offset、start/end locators）；
- `truncated` 布尔值、可空 `next_request`、可选 `warnings[]`。

原生 `mineru.native.middle/pdf_info-v1` 使用独立 renderer
`qatlas-mineru-native-markdown-v1`，不改变已发布 DocVortex 的 renderer/游标文本。
只构建内存消费视图：公开 page=`page_idx+1`、block=`native index+1`（原生编号0-based）；
保留编号空隙，不用数组位置补造锚点，子块不冒充新的顶层锚点。原始 index/JSON位置仍
可在固定 `layout.json` 对照。原生页单位 bbox 按有效 `page_size` 转为 `[0,1]` 供裁图，
非法/缺失 bbox 不钳制、不虚构，返回警告或不可裁图；不把转换结果写回原件。
`content_ranges` 的 start_offset/end_offset 为**渲染 Markdown 的 Unicode rune 半开区间**，
不是原 JSON 字节位移；窗口可切进 Markdown 语法，按原样拼接连续 content 才恢复选定文本。

这是由 Middle 生成的**阅读信封**，不是 Middle 原 JSON，不是 Structured Content，
更不是“截断后的原始 artifact”。若 truncated=true，将 `next_request.cursor`
原样传回下次请求。游标固定 paper/source/revision、PDF与Middle/完整包摘要、renderer
及选择范围；cursor-only 先解析这些 pins，**再**决定加载哪一 revision，不能先选
当前最新版。显式 pin/选择冲突或 renderer/摘要身份变化返回 409；格式错误返回 400。

```bash
qatlas paper read qa_… --source-id src_… --revision REVISION --page 5 --block 12 --json
qatlas paper read qa_… --limit 30000 -o first-window.json
qatlas paper read qa_… --cursor 'next_request.cursor 的原字符串' --json
```

未固定 revision/cursor、所选来源的完整包尚未就绪时，仅显式内容 GET 可启动该
exact source 的解析（source_id仅固定PDF，不固定解析修订），响应 **202** 和 `Retry-After: 5`。
明确 revision/cursor 的缺件或坏摘要不能创建替代解析；明确 source 的PDF缺失也不另抓其他版本。
已知来源时 `Operation-Location: /api/papers/{canonical}/read/status?source_id=S`；
来源尚未知的合法 fresh DOI/明确 arXiv请求保留原请求身份的 read/status 位置，按内容意图
抓取→冻结→解析，来源建立后才给source pin。PDF GET仅抓取/冻结，不启动推理。

```text
GET /api/papers/{id}/read/status?source_id=S&revision=R
```

status 是纯轮询：不调用 EnsureSource、不创建任务。响应带顶层
`paper_id`、`source_id`、`source_sha256`、`revision`、`state`、`ready`。
完整来源/manifest/成员校验成功才 state=cached、ready=true（200），兼容 md_ready/pdf_ready
及 source/revision固定的 read_url/markdown_url；pending/queued/running 可返回 202
和同一 Operation-Location/Retry-After；failed/none 或 HTTP 错误不等于就绪。
bundle_sha256 是完整 manifest 的摘要（逻辑包指纹），不是原 ZIP 压缩字节摘要。
客户端也兼容 ready/cached/done 状态：就绪后携固定 source/revision 重取原 read 请求。
**进程 Done 或历史 Middle 存在不足以判定新内容 ready**。

`--no-wait` 返回初始 JSON（包含轮询位置/重试间隔）；默认 CLI 有界轮询，超时或
failed/cooldown/unavailable 报错，stdout 不混入进度文字。同源校验防止把 PAT 发到
Operation-Location 指向的外站。

## 原始产物与清单

```text
GET /api/papers/{id}/parses/{revision}/manifest
GET /api/papers/{id}/parses/{revision}/files/{original_relative_member_path}
GET /api/papers/{id}/parses/{revision}/json
```

manifest 是单独生成的清单，字段为 `version=1`、`paper_id`、`source_id`、
`revision_id`、`source_pdf_sha256`、`middle_path`、`markdown_path`、
`files[{path,size_bytes,sha256}]`。按清单中原始 path 请求成员，嵌套目录不扁平化；
所有原始 JSON 与图片等均可获取。未知/不可信类型按 attachment + nosniff 交付，
允许的栅格图片可 inline。路径穿越、未知成员拒绝，成功成员仍逐个复核完整 SHA。

`/json` 仍为该 revision 的**字节完全一致的 Middle artifact**；固定历史评论
使用它，不取“最新解析”替换。CLI `paper parse-json ID REVISION` 继续输出校验后的
原字节，和 `paper read` 的派生视图不是同一产物。

## 上传与解析协议

- fresh PDF/解析上传写新内容桶。成功绑定的导入别名（如明确 `arxiv:IDvN`）永久
  指向首次成功的确切 PDF；同别名不同字节返回 409，**即使 overwrite=true**。
  新语义 arXiv vN 或独立新 source 是新身份，不通过覆盖旧 source 实现。
- `upload-mineru` 要求完整受支持 Middle **及** `markdown.md`/`full.md`：
  保留 `docvortex.middle`/schema_version 2.0，也支持实际 hosted V1 standard/hybrid 的
  原生 `layout.json`（`pdf_info` 数组），记录为 `mineru.native.middle` / `pdf_info-v1`。
  原生文件没有 DocVortex schema 标识，不向原件添加或伪造这些字段；原名/字节不变。
  MD-only、ContentList-only、缺件及无效 schema 拒收；图片可为空。
- ZIP 所有合法原名/字节保留，未知成员也不丢。路径穿越、绝对/反斜杠路径、
  重名、symlink、CRC错误等导致整包失败。当前限制 ZIP 128MiB、单成员128MiB、
  解压合计256MiB、文件数10000。原 ZIP 可额外留存，但不替代完整成员/manifest。
- 重上传是新 revision；overwrite 参数不会修改既有 revision。只有完整、校验
  成功且 PG 已发布的包才可 current。详见[Upload API](upload-api.md)。
- 服务端 MinerU hosted V1 在 `/api/v1` 命名空间走 uploads/create → PUT →
  complete/file.id → parse/jobs（要求 output_formats:[zip]），
  tier 默认 `standard`（flash/basic/standard/advanced），ocr_mode 为 auto/ocr。
  legacy V4 的 model_version、language、formula/table 字段是另一协议，不能当成
  V1 tier/OCR 参数；metadata 中 tier 标签也不代表服务器已执行相应质量档位。
- gate开启时 claim的PDF URL也是同源认证locator（pdf_requires_auth=true），不是S3
  presign；不可向第三方解析器转发PAT。须先鉴权获取exactPDF再按provider协议上传。
  gate关闭时仅给外部arXiv URL及目录SHA。PDF/search/list不触发推理或RAG正文读取，
  RAG索引发布只跟随显式解析完成。

## 错误与升级注意事项

关闭开关/缺少指定原件为404，认证失败401，scope不足403，输入错误400，身份或游标
冲突409，完整包/冻结字节完整性失败422，后端不可用503。显式 pin 失败不自动修复、
替换或回退。客户端须与服务端 major.minor 对齐；本契约涉及兼容性变化，按协调的
下一 minor RC 发布，未发布前不要把旧客户端/旧服务行为当成已升级。

旧桶与物理版本继续保留供运维，禁止为本功能做无授权批量迁移、删除或覆盖。
历史布局/ADR只记录当时设计，当前操作遵循本页与实际版本 OpenAPI。
