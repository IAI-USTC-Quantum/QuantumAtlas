# 上传 PDF / Markdown

当你**手里已经有 PDF 或解析后的 Markdown**——本地论文、扫描件、别处跑过 MinerU 的产物——用 `qatlas upload` 直接推到 server。

## 前置条件

- PAT 带 `papers:write` scope
- arXiv ID **必须带版本后缀**（`v1` / `v2`），因为 server 按 `<id>v<n>` 寻址对象

## 上传 PDF

最小用例：

```bash
qatlas upload pdf 2501.00010v1 --pdf paper.pdf
```

带元数据 JSON（题目 / 作者 / 摘要 / 类目）：

```bash
qatlas upload pdf 2501.00010v1 --pdf paper.pdf --metadata meta.json
```

## 上传 Markdown

```bash
qatlas upload markdown 2501.00010v1 --markdown paper.md --source mineru
```

`--source` 字段记录是谁解析的（mineru / pymupdf / manual / ...），出现在审计日志。

## sha256 dedup：上传一次还是上传两次？

server 端的 object 都带 `x-amz-meta-sha256` metadata，所以**同一字节再次上传是 200 OK + `{unchanged:true}` 短路**——零写入、幂等。换句话说：

| 场景 | 结果 |
|---|---|
| 第一次上传 | **201 Created**，对象写入 |
| 重传**完全相同**字节（哪怕换机器、换路径）| **200 OK** `{unchanged:true, pdf_sha256:...}` —— 安全可重试 |
| 上传**不同字节**到同一 arxiv_id | **409 Conflict**，body 含 `existing_sha256` + `new_sha256` + `existing_path`，**旧对象不动** |
| 上传不同字节 + `--overwrite` | **201 Created**，新版本写入；**旧版本被 bucket versioning 自动保留** |

!!! tip "client 端 sha256 校验"
    client 在上传前 stream sha256，作为 `?expected_sha256=<hex>` query 传给 server。server stage 完字节后比对，不匹配 → 400 + `{actual_sha256, expected_sha256}`，**任何 S3 写之前就拒**。

## `--overwrite` 什么时候用

只在你**确定要替换**旧对象时用。理由：

1. dedup 已经挡了"同字节重传"的常见无用覆盖
2. 真的字节变了 → 通常是"有意义的修订"，值得显式确认
3. 即使你 `--overwrite` 了，旧版本仍在 S3 noncurrent versions 里，**可恢复**

## metadata 与 PDF 各自判断

`upload pdf` 接两 part：PDF + 可选 metadata JSON。它们**独立判断 overwrite**：

```bash
# PDF 没变，metadata 改了：可以
qatlas upload pdf 2501.00010v1 --pdf paper.pdf --metadata new-meta.json
# 响应：{"pdf_unchanged":true, "metadata_unchanged":false, "unchanged":false}
```

如果 PDF 也变了且没 `--overwrite`，**两个 part 都不写**，整体 409。

## 并发安全（多 client 同时上传）

server 用 S3 `If-None-Match: "*"` conditional PUT，**多 client 同时上传同一 arxiv_id 的同一字节** → 全部短路 200 unchanged，**保证最多一个 201**。**不同字节并发** → 一个 201 + 其余 409，**不可能静默覆盖**。

参看 [对象寻址](../concepts/storage-architecture.md)（即 storage-architecture）和 [上传 API 详解](../reference/upload-api.md) 了解底层语义。

## 完整 flags

### `qatlas upload pdf`

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 必含版本（`v1` 等）|
| `--pdf <path>` | ✅ | — | 本地 PDF 文件 |
| `--metadata <path>` | ❌ | — | 可选 metadata JSON |
| `--overwrite` | ❌ | false | 允许覆盖 |

### `qatlas upload markdown`

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 必含版本 |
| `--markdown <path>` | ✅ | — | 本地 markdown |
| `--source <tool>` | ❌ | — | 记录解析工具（写入审计）|
| `--overwrite` | ❌ | false | 允许覆盖 |

加 [通用 client flags](manage-credentials.md#client-flags) 全部支持。

## 响应解读

成功响应（201 / 200）：

```json
{
  "arxiv_id": "2501.00010v1",
  "pdf_path": "pdf/2501/2501.00010v1.pdf",
  "pdf_sha256": "ab12cd34...",
  "pdf_size": 1234567,
  "metadata_path": "json/2501/2501.00010v1.json",
  "metadata_sha256": "...",
  "unchanged": false,
  "pdf_unchanged": false,
  "metadata_unchanged": false
}
```

冲突响应（409）：

```json
{
  "detail": "PDF for 2501.00010v1 already exists with different content",
  "existing_sha256": "ab12...",
  "new_sha256": "ef56...",
  "existing_path": "pdf/2501/2501.00010v1.pdf",
  "hint": "Pass --overwrite to replace; old version retained in S3 versioning."
}
```

## 怎么列已上传的论文

```bash
# 还没解析过 MinerU 的列表
qatlas mineru --no-push   # 不真跑，只展示候选

# 通过 wiki 看哪些有 paper page
qatlas wiki list --type source
```

## 下一步

- 继续做 MinerU 解析？[parse-with-mineru](parse-with-mineru.md)
- 写 paper Wiki 页面？[write-wiki-pages](write-wiki-pages.md)
- 详细 API 参考（status code 全量）？[reference/upload-api](../reference/upload-api.md)
