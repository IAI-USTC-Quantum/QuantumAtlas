# 用 MinerU 解析 PDF

`qatlas mineru` 用**你自己的 `MINERU_API_TOKEN`** 在本地跑 [MinerU](https://mineru.net) 解析 server 上已有的 PDF，然后把 Markdown 推回。

为什么不在 server 端解析？两个原因：

1. **配额属于触发者**——一个共享 server 配置 token 容易被滥用
2. **server 不挂在 MinerU 上**——一个慢解析不能拖死 server

## 前置条件

```bash
# 1. PAT 带 papers:write
qatlas auth login -H <server>

# 2. 配 MinerU token（client 端本地 .env）
echo 'MINERU_API_TOKEN=msk_...' >> ~/.config/qatlas/.env
```

PDF 必须**已经在 server 上**（通过 `qatlas ingest` 或 `qatlas upload pdf` 推上去）。`qatlas mineru` 会通过 server 颁发的临时 share URL 把 PDF 喂给 MinerU。

## 两种模式

=== "单篇模式"

    ```bash
    qatlas mineru 2501.00010v1
    ```

    流程：

    1. client POST `/api/papers/<id>/mineru-claim` 拿 30 分钟原子 claim + 临时 share URL
    2. 用 `MINERU_API_TOKEN` 提交解析任务给 MinerU
    3. 轮询 MinerU 直到 done（带 timeout）
    4. 下载 `full.md` 到临时目录
    5. POST `/api/papers/<id>/upload-markdown` 推回，标记 `source=mineru`
    6. 完成后 server 端 claim 自动释放

=== "队列模式（推荐多人协作）"

    ```bash
    qatlas mineru --max 10
    ```

    流程：

    1. GET `/api/papers/needs-mineru?limit=10` 拿 server 列表
    2. 对每一篇尝试 claim，已被别人 claim 的**静默跳过**
    3. 成功 claim 的逐篇处理（如上）

    多个贡献者同时跑 `qatlas mineru` 不会撞 MinerU 配额——claim 是 atomic。

## 完整 flags

| Flag | 默认 | 含义 |
|---|---|---|
| `<arxiv_id>` (可选) | — | 指定单篇；省略走队列模式 |
| `--max N` | 10 | 队列模式：本次最多处理几篇 |
| `--continue-on-error` | false | 队列模式：单篇失败时继续下一篇 |
| `--ttl-seconds N` | server 默认 1800 | claim 租约秒数（最长 7200）|
| `--no-cache` | false | 让 MinerU bypass 它的服务端缓存（重新跑）|
| `--overwrite` | false | server 已有 markdown 时仍允许覆盖 |
| `--no-push` | false | 跑 MinerU 但**不**推回 server（留在本地 tmp）|

加 [通用 client flags](manage-credentials.md#client-flags)。

## MinerU 环境变量（可选调优）

| 变量 | 默认 | 含义 |
|---|---|---|
| `MINERU_API_TOKEN` | — | **必填**，从 <https://mineru.net> 拿 |
| `MINERU_API_BASE_URL` | `https://mineru.net` | 自部署 MinerU 实例时改 |
| `MINERU_MODEL_VERSION` | `vlm` | `vlm` / `pipeline` |
| `MINERU_LANGUAGE` | `ch` | 主语言 hint |
| `MINERU_IS_OCR` | `false` | 强制 OCR（扫描件用）|
| `MINERU_ENABLE_FORMULA` | `true` | 公式识别 |
| `MINERU_ENABLE_TABLE` | `true` | 表格识别 |
| `MINERU_POLL_INTERVAL` | `3` | 轮询间隔（秒）|
| `MINERU_TIMEOUT` | `1800` | 单篇总超时（秒，30 分钟）|

## claim 是怎么回事

claim 是 server 颁发的**原子租约**：

```bash
POST /api/papers/<id>/mineru-claim
  → 201 {claim_id: "...", pdf_url: "https://.../share/<token>/...pdf", expires_at: "..."}

DELETE /api/papers/<id>/mineru-claim/<claim_id>
  → 200 (释放)
```

server 维护 `<data_dir>/mineru-claims/*.json`：

- claim 期间，其他 client 对同一 arxiv_id 调 `mineru-claim` **会被拒（409）**
- 30 分钟（可调）后 server 自动认为放弃
- 处理完成 / 失败时 client 显式 DELETE 释放

`qatlas mineru` 自动处理整个生命周期（含异常时的释放），手工调 API 时务必保证 release。

## 常见问题

!!! failure "MINERU_API_TOKEN must be set"
    client 端 `.env` / 环境变量没配。在 `~/.config/qatlas/.env` 或 `~/QuantumAtlas/.env` 加：

    ```bash
    MINERU_API_TOKEN=msk_xxx
    ```

!!! failure "skip (HTTP 409): paper already has markdown"
    Server 已有 markdown。要么跳过，要么 `--overwrite`。

!!! failure "skip (HTTP 409): paper already claimed by other client"
    别人正在跑。等 30 分钟租约过期，或换一篇。

!!! failure "MinerU task did not finish within MINERU_TIMEOUT=1800s"
    大论文（>50 页带很多图表）需要更长时间。`export MINERU_TIMEOUT=3600` 或单篇模式跑。

!!! failure "Markdown upload for X failed: HTTP 400 ... expected_sha256 mismatch"
    下载到磁盘的 markdown 在上传期间被改了 / 磁盘损坏。重跑通常解决。

## 排查 server 端列表

```bash
# 看 server 上还有多少 PDF 没解析
curl https://<server>/api/papers/needs-mineru?limit=5 | jq

# 看具体某篇的资产清单
qatlas wiki show paper-arxiv-2501.00010v1
```

## 下一步

- 解析好的 markdown 想沉淀成 wiki 页面？[写 Wiki 页面](write-wiki-pages.md)
- 想分享原始 PDF 给协作者？[分享与下载](share-and-download.md)
