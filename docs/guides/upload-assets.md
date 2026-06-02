# 上传 PDF / MinerU 结果

当你**手里已经有 PDF 或解析后的 MinerU 结果包**——本地论文、扫描件、别处跑过 MinerU 的产物——用 `qatlas upload` 直接推到 server。

## 前置条件

- PAT 带 `papers:write` scope
- arXiv ID **必须带版本后缀**（`v1` / `v2`），因为 server 按 `<id>v<n>` 寻址对象

## 上传 PDF

最小用例：

```bash
qatlas upload pdf 2501.00010v1 --pdf paper.pdf
```

论文元数据（题目 / 作者 / 摘要 / 引用）由服务器从 OpenAlex 上游同步进 Neo4j catalog（v0.7.0 起），不再走 upload 端点。

## 上传 MinerU 结果

```bash
qatlas upload mineru 2501.00010v1 --zip mineru-result.zip --source mineru
```

`--zip` 是 **MinerU 返回的完整结果 zip**（`full_zip_url` 指向的那个文件，原样保存）。server 端会解包、把 `full.md` 写到 markdown bucket、每张 `images/<name>` 写到 images bucket，保证一篇论文的 md 和图片**同时**落到对应桶里。

`--source` 字段记录是哪个 pipeline 解的（mineru-client-v0.8 / manual / …），出现在审计日志。

!!! warning "v0.8.0 BREAKING：`qatlas upload markdown` 没了"
    旧的 `qatlas upload markdown` 子命令 + `POST upload-markdown` 端点在 v0.8.0 删除。原因：旧路径只接受 `.md` 文件，**会丢掉所有图片**，详情页看到的图片引用全是 404。改用 `upload mineru` 推完整 zip 即可解决。

    迁移：
    ```diff
    - qatlas upload markdown 2501.00010v1 --markdown full.md --source mineru
    + qatlas upload mineru   2501.00010v1 --zip mineru-result.zip --source mineru
    ```

## sha256 dedup：上传一次还是上传两次？

server 端的 object 都带 `x-amz-meta-sha256` metadata，所以**同一字节再次上传是 200 OK + `{unchanged:true}` 短路**——零写入、幂等。换句话说：

| 场景 | 结果 |
|---|---|
| 第一次上传 | **201 Created**，对象写入 |
| 重传**完全相同**字节（哪怕换机器、换路径）| **200 OK** `{unchanged:true, ...}` —— 安全可重试 |
| 上传**不同字节**到同一 arxiv_id | **409 Conflict**，body 含 `existing_sha256` + `new_sha256` + `existing_path`，**旧对象不动** |
| 上传不同字节 + `--overwrite` | **201 Created**，新版本写入；**旧版本被 bucket versioning 自动保留** |

mineru 上传时整 zip 一起 sha 校验 + 每个解出来的对象（md / 每张图）再各自 sha dedup。**整 zip 完全没变**直接全部 unchanged 短路；**部分变**则只重写真的不同的对象。

!!! tip "client 端 sha256 校验"
    client 在上传前 stream sha256，作为 `?expected_sha256=<hex>` query 传给 server。server stage 完字节后比对，不匹配 → 400 + `{actual_sha256, expected_sha256}`，**任何 S3 写之前就拒**。

## `--overwrite` 什么时候用

只在你**确定要替换**旧对象时用。理由：

1. dedup 已经挡了"同字节重传"的常见无用覆盖
2. 真的字节变了 → 通常是"有意义的修订"，值得显式确认
3. 即使你 `--overwrite` 了，旧版本仍在 S3 noncurrent versions 里，**可恢复**

`upload mineru` 走 `--overwrite` 时是**bundle 级别**——markdown + 每张图都会按各自 sha 决定写还是 unchanged，不会用整 zip 一刀切。

## 并发安全（多 client 同时上传）

server 用 S3 `If-None-Match: "*"` conditional PUT，**多 client 同时上传同一 arxiv_id 的同一字节** → 全部短路 200 unchanged，**保证最多一个 201**。**不同字节并发** → 一个 201 + 其余 409，**不可能静默覆盖**。

`upload mineru` 内部每张图、每个 md 各自走这条 conditional PUT 流水线；保证 markdown 写之前所有 image 先落盘（markdown 是 completion marker）。

参看 [对象寻址](../concepts/storage-architecture.md)（即 storage-architecture）和 [上传 API 详解](../reference/upload-api.md) 了解底层语义。

## 完整 flags

### `qatlas upload pdf`

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 必含版本（`v1` 等）|
| `--pdf <path>` | ✅ | — | 本地 PDF 文件 |
| `--overwrite` | ❌ | false | 允许覆盖 |

### `qatlas upload mineru`

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 必含版本 |
| `--zip <path>` | ✅ | — | 本地 MinerU 结果 zip（含 `full.md` + 可选 `images/*`）|
| `--source <tool>` | ❌ | — | 记录解析工具 / pipeline（写入审计）|
| `--overwrite` | ❌ | false | 允许覆盖 markdown 和 images |

加 [通用 client flags](manage-credentials.md#client-flags) 全部支持。

## 响应解读

成功响应（201 / 200）：

```json
{
  "arxiv_id": "2501.00010v1",
  "pdf_path": "pdf/2501/2501.00010v1.pdf",
  "pdf_sha256": "ab12cd34...",
  "pdf_bytes": 1234567,
  "pdf_unchanged": false,
  "unchanged": false,
  "overwritten": false
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
