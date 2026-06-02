# 用 MinerU 解析 PDF

QuantumAtlas 有**两条** MinerU 转换路径，分别面向"使用者"和"贡献者"。先看清边界再挑用哪条。

## 两条路径对比

| 维度 | `qatlas markdown` (使用者) | `qatlas mineru` (贡献者) |
|---|---|---|
| **谁出 MinerU 配额** | **server 自己的** `MINERU_API_TOKEN`（写在服务器 `.env`） | **你自己的** `MINERU_API_TOKEN`（写在本地 `.env`） |
| **谁发起 MinerU 请求** | qatlasd 在 server 端调 MinerU API | 你的 client 本地直接调 MinerU API |
| **谁拉 PDF 喂给 MinerU** | qatlasd 在 server 端拉本 edge 的 RustFS presign 直链（`QATLAS_S3_PUBLIC_ENDPOINT` 签）发给 MinerU | server 在 `mineru-claim` 响应里返回同款 RustFS presign 直链给你的 client，client 转给 MinerU |
| **PAT scope** | 不需要（开放读）—— 任何登录用户都能 GET md | 需要 `papers:write` —— claim + 上传都受门禁 |
| **没 PDF 怎么办** | server 返回 `404 {status: no_pdf}`，需要先 `qatlas ingest` 或 `qatlas upload pdf` | 同上：claim 直接 404，提示先 ingest/upload PDF |
| **没 md 怎么办** | server 后台异步起转换 + 立即回 `202` + `Retry-After`，client 轮询到 `200` 拿正文 | client 自己跑 MinerU，结果直接 `upload-mineru` 推回 |
| **失败模式** | server 把失败标记到 job 状态；client 拿到 `failed` 报错退出 | client 控制；超时 / MinerU 报错由 client 主动 `DELETE` claim 释放 |
| **场景**| "我只是想读这篇的 md，server 帮我处理掉" | "我有 MinerU 额度，主动帮库攒解析资产" |

**共同点**：两条路径都用**直链 / share URL**喂 PDF 给 MinerU，**不**把 PDF bytes 通过 server 中转再上传给 MinerU——直链让 MinerU 直接去拉，省一次 server 出入流量。两边喂给 MinerU 的 URL 都是 `internal/mineru.BuildPDFURL` 算出来的同一种 RustFS presign 直链（每 edge 自己的 `QATLAS_S3_PUBLIC_ENDPOINT` 签 5 分钟 ~ claim TTL+10 分钟范围 TTL；RackNerd 用域名 + LE 证书，Alibaba 用 IP + 自签）。这样设计的好处：MinerU 拿到的字节跟 server 已存的 PDF **保证一致**——如果直接给 MinerU `https://arxiv.org/pdf/<id>`，arxiv 上 paper 改了 / 撤稿就会跟 server 旧版漂移。当 RustFS presign 临时不可用（如 S3 端点抖动），claim handler 会 fallback 到 `https://arxiv.org/pdf/<id>` 公网 URL + WARN log 保活。

## 路径 A：`qatlas markdown`（server 静默转换）

```bash
qatlas markdown 2501.00010v1          # 有缓存直接给；无缓存 server 静默转，client 轮询
qatlas markdown 2501.00010v1 -o out.md
qatlas markdown 2501.00010v1 --no-wait    # 不轮询，挂起则退码 75 (EX_TEMPFAIL)
```

- 用 server `.env` 的 `MINERU_API_TOKEN`，**调用方无需自己的 token / `papers:write` scope**（开放读）
- 无缓存 → server 后台起转换 + 立即回 `202`，client 轮询直到 `200`
- 产出的 markdown + images 落对象存储，下次直接命中缓存
- 适合"只想读 markdown"的用户

## 路径 B：`qatlas mineru`（贡献者本地解析）

为什么还保留这条贡献者路径？

1. **配额归属**——贡献者用自己的 token 跑，不吃 server 共享配额
2. **批量贡献**——队列 / `--watch` daemon 模式可一次攒很多篇，主动暖缓存
3. **离线工具链组合**——你可能想用别的解析器 / 自托管 MinerU，跑完后用 `qatlas upload mineru --zip <result.zip>` 推上去

## 前置条件（`qatlas mineru`）

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
    4. 下载 **完整结果 zip**（含 `full.md` + `images/*`）到临时目录
    5. POST `/api/papers/<id>/upload-mineru` 把整 zip 推回，server 解包后 markdown 落 `qatlas-md`，每张图落 `qatlas-images/<canonical>/`
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

=== "daemon 模式（挂着持续贡献）"

    ```bash
    qatlas mineru --watch
    # 或显式给间隔
    qatlas mineru --watch --watch-interval 600
    ```

    跑完一轮 queue → sleep `--watch-interval`（默认 300 秒）→ 再来一轮，循环直到收到 SIGINT/SIGTERM。Ctrl-C 一次会**等当前 paper 完事再退**并释放 claim；两次直接 abort。隐含 `--continue-on-error`（不然单篇 5xx 会让整个 daemon 退）。

    ```bash
    # 后台跑 + 把 stderr 重定向到日志
    nohup qatlas mineru --watch --max 5 > qatlas-mineru.log 2>&1 &
    ```

## 完整 flags

| Flag | 默认 | 含义 |
|---|---|---|
| `<arxiv_id>` (可选) | — | 指定单篇；省略走队列模式 |
| `--max N` | 10 | 队列模式：本次最多处理几篇 |
| `--continue-on-error` | false | 队列模式：单篇失败时继续下一篇（`--watch` 自动启用）|
| `--ttl-seconds N` | server 默认 1800 | claim 租约秒数（最长 7200）|
| `--no-cache` | false | 让 MinerU bypass 它的服务端缓存（重新跑）|
| `--overwrite` | false | server 已有 markdown / images 时仍允许覆盖 |
| `--no-push` | false | 跑 MinerU 但**不**推回 server（zip 留在本地 tmp，方便 debug）|
| `--watch` | false | daemon 模式：循环跑直到收 SIGINT/SIGTERM |
| `--watch-interval N` | 300 | daemon 模式 sleep 秒数 |

加 [通用 client flags](manage-credentials.md#client-flags)。

!!! note "v0.8.0：不再丢图"
    旧版本 (`qatlas mineru` ≤ v0.7.x) 在 step 4 只从 zip 抽出 `full.md`，**所有 `images/*` 都被静默丢弃**，导致详情页图片引用 404。v0.8.0 改为把整个 zip 原样 push 给 server 端 `upload-mineru`，server 复用同款 zip 解析逻辑写入两个桶——client 端贡献的图片现在能完整落地。

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
