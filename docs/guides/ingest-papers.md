# 从 arXiv 摄入论文

让 server 直接从 arXiv 抓 PDF + 元数据，并可选立刻调 MinerU 解析为 Markdown。

!!! info "MinerU 是当前唯一受支持的解析器"
    早期版本曾包含一个本地 PDF 解析器作为离线选项，但因第三方 license 约束已从开源版本移除。所有摄入流程现在统一走 MinerU 远程 API（**需要服务端配置 `MINERU_API_TOKEN`**），或者跳过 server 解析 + 手动 `qatlas upload mineru` 推上去。

## 前置条件

- 已装 client：`uv tool install quantum-atlas`
- 已配 `QATLAS_SERVER_URL`
- 已配 `QATLAS_TOKEN`，PAT 带 **`papers:write`** scope（[怎么拿 PAT](manage-credentials.md#mint-pat)）
- 目标 server 端 `.env` 已配 `MINERU_API_TOKEN`（解析这一步要用）

## 最小用例

```bash
qatlas ingest 2501.00010
```

执行流程：

1. client POST `/api/ingest/paper` 提交任务（默认 `--parser mineru`）
2. server 异步从 arXiv 下载 PDF + 元数据 JSON 到 RawDir/S3
3. server 把 PDF 交给 MinerU API 解析为 Markdown
4. client 默认轮询 task 状态直到结束

## 完整 flags

| Flag | 必填 | 含义 |
|---|---|---|
| `<arxiv_id>` (positional) | ✅ | arXiv ID，例如 `quant-ph/9508027` 或 `2501.00010`（不含 `vN` 后缀时 server 自动取最新版）|
| `--parser mineru` | ❌ | 显式声明解析器（当前唯一合法值就是 `mineru`，省略则默认 `mineru`）|
| `--stop-after fetch\|parse` | ❌ | 只跑到指定阶段就停（默认跑完）|
| `--stages a,b` | ❌ | 显式指定要跑的阶段列表，逗号分隔 |
| `--force-fetch` | ❌ | 即使 server 已有 PDF 也重抓 |
| `--force-parse` | ❌ | 即使已有 markdown 也重解析 |
| `--mineru-no-cache` | ❌ | 告诉 MinerU bypass 它的服务端缓存 |
| `--no-poll` | ❌ | 提交后立即返回，不等任务完成 |
| `--poll-interval 1.0` | ❌ | 轮询间隔（秒）|
| `--timeout 600` | ❌ | 总等待超时（秒）|

加上通用 client flags：`--base-url`、`--token`、`--insecure`、`--request-timeout`。

## 两种典型用法

=== "只拿 PDF，不解析"

    ```bash
    qatlas ingest quant-ph/9508027 --stop-after fetch
    ```

    适合先批量拉一堆，回头再决定哪些值得跑 MinerU。

=== "MinerU 一步到位（默认）"

    ```bash
    qatlas ingest 2501.00010
    ```

    省略 `--parser` 等同于 `--parser mineru`。MinerU 是服务化的高质量解析（支持公式、表格、OCR），结果接近排印 Markdown 质量。**server 端必须配 `MINERU_API_TOKEN`** —— 没配的话 server 会回 503 / 400 并提示。这种情况下改用[本地 MinerU 模式](parse-with-mineru.md)。

## 续跑已存在的任务

如果一个 task 中途断了或者你想重新跑 parse 阶段：

```bash
qatlas ingest continue <task_id> --stages parse
```

查看任务状态：

```bash
qatlas ingest status <task_id>
```

## 常见错误

!!! failure "401 Unauthorized"
    PAT 没设 / 过期 / 没勾 `papers:write` scope。去 `/pat` 检查。

!!! failure "404 Paper not found on arXiv"
    arxiv_id 拼错（注意旧式带分类前缀 `quant-ph/...`，新式没前缀 `2501.00010`）。

!!! failure "Timed out waiting for ingest task ..."
    `--timeout` 默认 600s 不够。大论文 + MinerU 通常 5–15 分钟。加 `--timeout 1800` 或 `--no-poll` 让任务后台跑、之后 `status` 查。

!!! failure "503 MinerU not configured on server"
    Server 没配 `MINERU_API_TOKEN`。请改走[本地 MinerU 模式](parse-with-mineru.md)（自己出 MinerU 配额，解析完再用 `qatlas upload mineru` 推回），或者联系 server 管理员补 token。

## 下一步

- 解析完想看 Markdown？`qatlas wiki list --type source` → 拿 `paper-arxiv-<id>` → `qatlas wiki show ...`
- 想用自己的 MinerU 配额？看 [用 MinerU 解析](parse-with-mineru.md)
- 已经有本地 PDF？跳过 ingest，直接 [上传](upload-assets.md)
