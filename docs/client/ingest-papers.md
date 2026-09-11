# 从 arXiv 摄入论文

让 server 从 arXiv 抓 PDF + 元数据，并调 MinerU 解析为 Markdown。

!!! info "旧 `qatlas ingest` 命令已移除"
    `qatlas ingest` 子命令及其服务端端点（`POST /api/ingest/paper` 等）已
    随客户端重构移除。现在的摄入入口是**读取时的静默抓取**（`qatlas paper
    get markdown`，缓存未命中自动触发）与贡献者工作流（`qatlas contrib`），
    见下文。

!!! info "MinerU 是当前唯一受支持的解析器"
    所有摄入流程统一走 MinerU 远程 API（需要服务端在 config.yaml 的
    `paper_access.mineru` 段配置 `api_tokens`），或者由贡献者本地跑
    `qatlas contrib mineru`（用自己的 MinerU 配额解析后推回）。

## 前置条件

- 已装 client：`uv tool install qatlas-cli`
- 已配 `server_url:`（`qatlas config set server_url https://...`）
- 已跑 `qatlas auth login -s <host>` 拿到 PAT（写进 hosts.yml；静默抓取
  至少带 **`papers:read`** scope，批量提交下载走 `papers:write`，详见
  [怎么拿 PAT](manage-credentials.md#mint-pat)）
- 服务端已开启 `paper_access.enabled: true`

## 最小用例（静默抓取）

```bash
qatlas paper get markdown 2501.00010 -o paper.md
```

执行流程：

1. client 请求 markdown；server 缓存未命中时自动进入抓取管线
   （多范式 PDF 下载 → MinerU 转换），以 LRO 形式回报进度；
2. client 默认轮询 `markdown/status` 直到转换完成并落盘；
3. `--no-wait` 提交后立返，之后用 `qatlas paper status <id>` 查进度。

ID 形态：旧式 `quant-ph/9508027`、新式 `2501.00010`、带版本
`2501.00010v2`、以及 DOI 都可以，server 自动归一化。

## 批量提交（Robust Downloader）

一次最多 50 条标识（DOI / arXiv ID / 论文链接），入队多范式下载阶梯，
需 `papers:write`：

```bash
curl -X POST <server>/api/downloader/fetch \
  -H "Authorization: Bearer <PAT>" \
  -H "Content-Type: application/json" \
  -d '{"items": ["10.1038/s41586-024-07806-9", "arXiv:2401.12345"]}'
```

进度看 `GET /api/downloader/jobs`（本地任务）与
`GET /api/downloader/remote-jobs`（启用 outbound fleet 时的持久化任务），
或 Web 端的 Robust Downloader 页。

## 贡献者自配额路径

- 已有本地 PDF：`qatlas contrib pdf <ARXIV_ID|DOI> --pdf paper.pdf`
  （`--overwrite` 覆盖已有资产）；
- 用自己的 MinerU 配额本地转换再推回：`qatlas contrib mineru <ARXIV_ID>`
  （队列模式 `qatlas contrib mineru`、守护模式 `--watch`、现成 zip 上传
  `--zip`，详见[用 MinerU 解析](parse-with-mineru.md)）。

## 常见错误

!!! failure "401 Unauthorized"
    PAT 没设 / 过期 / scope 不够。去 `/pat` 检查。

!!! failure "404 Paper not found on arXiv"
    arxiv_id 拼错（注意旧式带分类前缀 `quant-ph/...`，新式没前缀 `2501.00010`）。

!!! failure "503 MinerU not configured on server"
    服务端 config.yaml 的 `paper_access.mineru.api_tokens` 为空。改走
    [本地 MinerU 模式](parse-with-mineru.md)，或联系管理员补 token。

## 下一步

- 转换完想拉图片？`qatlas paper get images <id> -o images.zip`
- 想用自己的 MinerU 配额？看 [用 MinerU 解析](parse-with-mineru.md)
- 已经有本地 PDF？直接 [上传](upload-assets.md)
