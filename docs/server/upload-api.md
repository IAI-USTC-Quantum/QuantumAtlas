# Upload API：冻结 PDF 与完整解析发布

当前上传写独立的 `qatlas-content` 内容桶，不再向旧 markdown/images 桶覆盖写。
成功必须包括 PostgreSQL 的来源/修订发布；PG 不可用时失败，**没有 deferred-success
或“稍后补索引”的成功承诺**。历史三桶覆盖与 S3 物理版本恢复不适用于新不可变原件。
存储、读取、游标和身份边界见[论文内容契约](paper-content.md)。

## Endpoints

| Method | Path | Scope |
|---|---|---|
| POST | `/api/papers/{id}/upload-pdf` | `papers:write` |
| POST | `/api/papers/{id}/upload-mineru` | `papers:write` |
| POST | `/api/v1/papers/{id}/mineru-lease` | `papers:write` |
| DELETE | `/api/v1/papers/{id}/mineru-lease/{claim_id}` | `papers:write` |

上传 `id` 接受明确带 `vN` 的 arXiv（`2501.00010v1`、`quant-ph/9508027v2`）或 DOI。
别名 `/api/papers/{id}/mineru-claim` 保留既有 lease 操作；`claim_id` 是租约身份，
不是 source/revision。需已有用户/系统允许的认证凭据与 scope，不能把 worker secret
或 S3 凭据当成普通上传 token；凭据管理见[客户端认证](../client/manage-credentials.md)。

## PDF intake 与不可覆盖别名

multipart 字段 `pdf`，内容为 `%PDF-` 开头的真实 PDF，上限 100MiB。
可传 `expected_sha256`，服务端在持久化前计算并交叉验证完整文件摘要。

```bash
qatlas contrib pdf 2501.00010v1 --pdf ./paper.pdf
# 同一个导入别名 + 同一确切字节：200 unchanged
qatlas contrib pdf 2501.00010v1 --pdf ./paper.pdf
# 同一个已冻结别名 + 不同字节：409，即使传 --overwrite 也不能替换
qatlas contrib pdf 2501.00010v1 --pdf ./different.pdf --overwrite
```

- 首次成功把规范导入别名永久绑定确切 PDF/SHA/source ID（201）。
- 重传相同已绑定别名/SHA 返回 200，`unchanged=true`；不能通过 repeated PUT 创建
  第二套字节身份。同一 paper 下相同 SHA 可复用已有 source ID。
- 同别名不同字节返回 409，携已有/新 SHA 供核对；`overwrite=true` 不改变结果，
  不覆盖 frozen source、不替换历史评论原件。上游新的 arXiv `vN` 或合法独立新来源
  是另一个语义身份，不是对旧来源做物理版本覆盖。
- arXiv `vN` 是语义版本；`source_id` 固定字节；`S3VersionId` 是物理存储版本，
  不能用同一字段替换或从物理恢复后悄悄改冻结 PDF。

响应包含 `paper_id`、`source_id`、`pdf_path`（新的 `content/…/source.pdf` 逻辑键）、
`pdf_bytes`、`pdf_sha256`、`pdf_unchanged`、`unchanged`、`overwritten=false` 与鉴权
`pdf_url`，并返回规范 arXiv/DOI 身份及可选 verification/uploaded_by。
来源与 SHA 也由 `X-QAtlas-Paper-Id`、`X-QAtlas-Source-Id`、`X-QAtlas-Sha256` /
`X-QAtlas-PDF-SHA256` headers 提供。客户端应保留身份与摘要，而非猜测旧桶路径。

## 完整 MinerU bundle intake

multipart 字段 `mineru_zip`；必须提交最终完整输出，不接受任意 `.md` 或任意 JSON
冒充 Middle。要求：

1. 支持的 `docvortex.middle` / `schema_version=2.0`，成员为 `middle_json.json`
   或支持的 `layout.json`（实际原名保留）；
2. `markdown.md` 或 `full.md`；
3. 所选已冻结来源 PDF 存在，`pdf_sha256`（若传）等于实际解析的 exact source SHA。

MD-only、ContentList/StructuredContent-only、缺 Markdown、坏 schema 等返回 422。
图片不是必需，纯文字论文可无图；原 ZIP 可额外保存，但完整成员/清单不可省略。

```bash
# arXiv runner 的实际产物也必须满足上述完整包要求，不承诺旧 V4 MD-only 产物兼容：
qatlas contrib mineru 2501.00010v1
# DOI 的现成完整 bundle：
qatlas contrib mineru 10.1103/PhysRevLett.123.070501 --zip ./complete-result.zip
```

HTTP 支持 `expected_sha256`（整个 ZIP 的传输摘要）、`pdf_sha256`（exact PDF 摘要）、
`source_id`（明确 source 一致性 pin）、`tier`（生产者/请求元数据标签）、`source`
（贡献来源标签）。既有 `overwrite` 输入不会修改旧 revision；DOI metadata 在 PDF
贡献阶段确立，不能借 parse form 覆盖 title/authors。tier 标签不证明某质量模式真实执行。

### 原名、字节、原子发布

**所有合法 ZIP 文件成员**原路径/名称/字节保留，包括 Middle、MD、Structured Content、
ContentList、图片、未知文件。`layout.json` 不改名成 `middle.json`，其他 JSON 不重命名
成 `content_list.json`，不做原件 reserialization。

路径穿越、绝对路径、反斜杠、重名、symlink、CRC/解压错误整包拒收；限制为 ZIP
128MiB、单成员128MiB、解压合计256MiB、文件数10000。单纯字符串内包含 `..`
不是路径穿越判断的替代：合法原名不会被随意静默丢弃。

服务端写入不可变 revision 的 `files/{original_relative_path}`，逐个复核持久存储
SHA/大小；最后写独立生成的 manifest，再在 PG 发布 complete revision/current。
每次重传解析产物产生**新 revision**（201），即使 Middle 文本相同也不覆盖旧发布。
失败不改变既有 current 或历史成员；对象写成功但 PG 发布失败不报告 ready/success。

响应包含 `paper_id`、`source_id`、`source_sha256`、`revision_id` / `revision`、
`is_current`、`tier`、`schema`、`schema_version`、`artifact_sha256`、`manifest_sha256`、
`manifest`、`read_endpoint` 与 `blocks_endpoint`。`artifact_sha256` 是原 Middle
摘要，manifest 摘要/成员摘要是另外的完整性层，不能混用。

## 错误与重试

- 400：非法 multipart/标识/传输 SHA 或 source PDF SHA mismatch。
- 401/403：身份或 scope 不允许。
- 404：明确 source/论文不存在；先上传合法 exact PDF。
- 409：冻结别名不同字节或 source/语义版本不匹配，不能自动换来源。
- 413：请求文件超过入口字节上限。
- 422：完整包缺件、无效 profile、档案安全/完整性验证失败。
- 503：PG/对象存储不可达，不作为版本缺失、成功延期登记或自动恢复的理由。

写请求丢失响应可能是 **UNKNOWN**；先核对服务端 source/parses/manifest。PDF 相同
别名/SHA的幂等性与 parse reupload 新 revision 的语义不同，不盲目重传解析包。
CLI 写前协商服务端 major.minor，服务更高或探针失败时不发送 mutation；已发送
后的版本偏移只警告，不把业务结果误报为“未发送”。

## Contributing by DOI

DOI upload 为出版版合法来源，与明确 arXiv 语义版本分别记录导入别名；作品可由
registry 统一为同一 `qa_`，**显式 source/version pin 永不被 DOI 默认选择替换**。
PDF 贡献可自动解析 DOI 的 OpenAlex title/authors/arXiv linkage；不接受贡献者用
form任意覆写元数据。`verify=warn` 保留既有 advisory 行为；`verify=strict` 在
DOI-not-found 时409、元数据后端不可用/未配置时503。返回 verification 状态供核对；
不能把元数据匹配当作 PDF 字节本身已证明来源正确。

### Canonical resolution

现在先解析明确的 source/version/revision；不再以历史“DOI 总优先”规则改写已固定
来源。语义默认选择与字节 identity 分开，遇到歧义要显式 pin，不猜测最新版。
旧三桶 DOI `pdf/doi/…` 等布局只供旧 PDF 按需发现和历史运维，不是 fresh 写入路径。

## Claim PDF locator 与授权

开启 paper_access 时，claim返回冻结/核验来源的同源认证
`/api/papers/{qa}/pdf?source_id=S`，含 `source_id`、`pdf_requires_auth:true`、
确切 PDF SHA。调用者须携自己的读取认证下载；**不是**提供给第三方免登录抓取的
S3/NAS presign，也不可把 PAT 交给外部解析器。需要云端解析时应先鉴权读取实际PDF，
再通过受支持的 provider 上传协议发送该确切字节，不直接转发受保护API locator。
关闭 paper_access 时，claim只给外部 arXiv URL 与授权目录的SHA，不交付托管原件。

PDF上传、下载归档、搜索或元数据列表不触发推理/RAG正文访问；RAG索引推送在显式
解析完成后进行，不能作为绕过“只有内容访问才推理”的隐式触发链。

## 运维与升级

旧桶不删除，不进行批量回填；只在内容/PDF访问时按需冻结旧 PDF，忽略旧 MD/JSON/
images。新文档与实际 OpenAPI 共同定义当前 API；过去用 S3 versioning 恢复覆盖位的
步骤仍可用于旧资源的明确运维，但不能据此覆盖当前 frozen source/parse revision。
服务端 hosted V1 `/api/v1`、默认 tier=standard 与 legacy V4 模型字段是不同协议，
详见[论文内容契约](paper-content.md)。本兼容性改变随协调下一 minor RC 发布。
