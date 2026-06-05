# 用 MinerU 解析 PDF（贡献你的额度）

QuantumAtlas 默认只暴露**一条** MinerU 路径——**贡献者本地解析**（`qatlas mineru`）。
公开实例（quantum-atlas.ai）上 server 端**不**提供"读 markdown / server 用自身配额
静默转换"的端点，也**不**通过 API 对外分发 PDF / markdown 字节。

> **Self-hosted 部署**可以通过 `QATLAS_PAPER_ACCESS_ENABLED=true`
> 在受控范围内**额外**开启 server-side on-demand conversion，详见
> [License & Attribution · 论文访问开关](../about/license-and-attribution.md#论文访问开关-self-hosted)
> 与 [env-vars · Server-side MinerU](../reference/env-vars.md#server-side-mineru仅当-qatlas_asset_downloads_enabledtrue-时生效)。
> 那条路径不替代本页讲的贡献者流程——两者并行：开关 ON 时 server 会用
> **自己的** MinerU token 在 `GET /markdown` 缓存未命中时透明跑一次；贡献者
> 仍可继续用自己的配额走 `qatlas mineru` → `upload-mineru` 主动暖缓存。

!!! tip "为什么这算贡献"
    MinerU 给每个注册账号送 **5000 篇 / 天** 的免费解析额度。绝大多数个人用户每天用不
    到这个量，而 catalog 里始终有几千篇 PDF 等着被解析成 markdown（解析完才能进
    全文搜索、被 LLM 抽取、生成 wiki 引用片段）。**`qatlas mineru` 把你闲置的额度
    导给项目**：你贡献的不是磁盘、不是带宽（PDF 已经在 server 上），而是你那个
    MinerU 账号每天没用完的那几千篇配额。

## 完全零基础？四条命令上手 { #zero-to-watch }

如果你从来没装过 qatlas，全程就这四条命令，3 分钟搞定：

```bash
# 1. 装 qatlas（uv 是 https://docs.astral.sh/uv/ 的官方安装器；alpha 版本要 --prerelease=allow）
uv tool install --prerelease=allow quantum-atlas

# 2. 拿一个上传 PAT。会浏览器打开 GitHub OAuth → 颁发 PAT → 自动写到
#    ~/.config/qatlas/hosts.yml（明文密钥不进 shell history）。
qatlas auth login -s quantum-atlas.ai

# 3. 配你的 MinerU token。回车后会出现一个**不显示**的密码输入框，
#    粘贴 JWT（mineru.net 后台复制，eyJ... 开头）后回车即可。
qatlas config set mineru_api_token

# 4. 挂着持续贡献。Ctrl-C 一次=等当前 batch 完成后优雅退出；两次=立即 abort。
qatlas mineru --watch
```

就这样。后面所有章节都是细节展开 —— 跑起来之后再回头看就行。

??? note "几个会让人疑惑的细节"
    - **第 2 步的 PAT 默认 scope 是 `papers:write`**（包含 `papers:read`）。在
      [Profile → PAT](https://quantum-atlas.ai/pat) 也能手工创建带其它 scope 的
      token。
    - **第 3 步没让你输 `server_url`** 是因为 default 已经指向
      `https://quantum-atlas.ai`。换私有 instance 才需要
      `qatlas config set server_url https://your-instance/`。
    - **第 4 步遇到 daily-limit** 不退出——daemon 自动 sleep 到本地次日 00:01
      额度重置后继续跑。想让它跨终端会话存活、自动重启崩溃的进程，看下面
      [把 daemon 挂久一点](#把-daemon-挂久一点)。
    - **JWT 不小心粘错了**？再跑一次 `qatlas config set mineru_api_token`
      覆盖即可，老值会被替换。

## 30 秒上手（已装过 qatlas）

```bash
# 1. PAT（papers:write）——浏览器登录 quantum-atlas.ai → 顶栏 / 任意语言下的 /pat 申请，
#    勾上 "Upload paper PDFs and submit MinerU markdown (includes read)" 一项即可。
qatlas auth login -s quantum-atlas.ai

# 2. MinerU JWT——mineru.net 注册 / OpenXLab 登录 → API 管理后台复制 token。
#    无 value 触发隐藏粘贴框，避免 JWT 进 shell history / ps aux / scrollback。
qatlas config set mineru_api_token
# CI 友好：echo "$MINERU_API_TOKENS" | qatlas config set mineru_api_token
# 想一行搞定（注意暴露 history）：qatlas config set mineru_api_tokens eyJ0eXBlIjoi...

# 3. 挂着持续贡献。第一次 Ctrl-C 会等当前 batch 完事再优雅退出并释放未完成的 claim；
#    第二次直接 abort。
qatlas mineru --watch
```

无空配额、无 PAT 的来宾用户也可以读 catalog 的 stats / 待解析队列（`papers:read`
是 PAT 的默认 scope），但**实际提交 markdown** 需要 `papers:write`。

## 贡献者路径：`qatlas mineru`

为什么走贡献者本地：

1. **配额归属**——贡献者用自己的 MinerU token 跑，不吃 server 共享配额
2. **批量贡献**——队列 / `--watch` daemon 模式可一次攒很多篇，主动暖缓存
3. **离线工具链组合**——你可能想用别的解析器 / 自托管 MinerU，跑完后用
   `qatlas upload mineru --zip <result.zip>` 推上去

**机制**：claim handler 在响应里返回**一次性、短 TTL 的 RustFS presign 直链**
（由各 edge 自己的 `QATLAS_S3_PUBLIC_ENDPOINT` 签发，仅可被持有 claim 的贡献者
在 MinerU job 期内使用），client 把该 URL 转给 MinerU 让它直接拉 PDF 字节，
完事 zip 上传回 server。这条链路是**给已授权贡献者的工作流通道**，不是对外
分发端点。当 RustFS presign 临时不可用，claim handler 会 fallback 到
`https://arxiv.org/pdf/<id>` 公网 URL + WARN log 保活。

## 前置条件

```bash
# 1. PAT 带 papers:write
qatlas auth login -s <server>

# 2. 配 MinerU token —— 写到 user-level config，无 value 触发隐藏粘贴框
#    （JWT 不会进 shell history / ps aux / scrollback）。
qatlas config set mineru_api_token
# 或直接编辑 yaml：echo 'mineru_api_token: eyJ...' >> $(qatlas config path)
```

PDF 必须**已经在 server 上**（通过 `qatlas ingest` 或 `qatlas upload pdf` 推上去）。

## 三种模式

=== "单篇模式"

    ```bash
    qatlas mineru 2501.00010v1
    ```

    流程：

    1. client POST `/api/papers/<id>/mineru-claim` 拿 30 分钟原子 claim + 临时 presign URL
    2. 用 `mineru_api_token` 提交解析任务给 MinerU（**单 task API** `POST /api/v4/extract/task`，单篇不走 batch）
    3. 轮询 MinerU 直到 done（带 timeout）
    4. 下载 **完整结果 zip**（含 `full.md` + `images/*`）到临时目录
    5. POST `/api/papers/<id>/upload-mineru` 把整 zip 推回，server 解包后 markdown 落 `qatlas-md`，每张图落 `qatlas-images/<canonical>/`
    6. 完成后 server 端 claim 自动释放

=== "队列模式（推荐多人协作）"

    ```bash
    qatlas mineru                  # 默认 batch_size=50（MinerU 单批硬上限）
    qatlas mineru --batch-size 20  # 想缩小批量
    ```

    自 v0.15.0 起队列模式走 **MinerU batch API**（`POST /api/v4/extract/task/batch`）：

    1. GET `/api/papers/needs-mineru?limit=<batch_size>` 拿 server 列表
    2. 对每篇逐一 claim + sha256 校验（**一次性预处理**），失败的释放 claim、不影响其它论文
    3. 把所有 survivors 用**一次** `submit_url_batch` 调用塞进 MinerU（最多 50 篇 / 批）
    4. 周期性 `get_batch` 轮询；任何 `state=done` 的条目立刻下载 zip → upload → release claim，**不**等整批完事
    5. `state=failed` 的条目按 err_msg 分类（daily-limit / fatal / 其它）后 release claim
    6. 全批 terminal 或触发 daily-limit 后退出

    多个贡献者同时跑 `qatlas mineru` 不会撞 MinerU 配额——claim 是 atomic；同一批内某篇失败也不阻塞其它论文。

=== "daemon 模式（挂着持续贡献）"

    ```bash
    qatlas mineru --watch
    # 显式给间隔（队列空时的睡眠秒数；daily-limit 命中时改睡到次日 00:01）
    qatlas mineru --watch --watch-interval 600
    ```

    跑完一批 queue → sleep `--watch-interval`（默认 300 秒）→ 再来一批，循环直到收到 SIGINT/SIGTERM。Ctrl-C 一次会**等当前 batch 完事再退**并释放所有 in-flight claim；两次直接 abort。隐含 `--continue-on-error`（不然单 paper 5xx 会让整个 daemon 退）。

    **Daily-limit 触发**：MinerU 每天 5000 篇免费额度耗尽（`-60018` / `-60019` / HTTP 429 / 关键词命中）后，daemon **自动跳过 `--watch-interval`，直接 sleep 到下一个本地 00:01**（quota 重置时刻），避免空轮询浪费请求。一次性运行命中 daily-limit 则退出码 **75 (EX_TEMPFAIL)**，CI 可视为可重试错误。

    ```bash
    # 后台跑 + 把 stderr 重定向到日志
    nohup qatlas mineru --watch --batch-size 50 > qatlas-mineru.log 2>&1 &
    ```

## 把 daemon 挂久一点

`--watch` 是个普通 foreground 进程；想让它跨终端会话存活，挑你顺手的就行：

=== "tmux / screen"

    ```bash
    tmux new -s mineru
    qatlas mineru --watch --batch-size 50
    # Ctrl-B d 后台化；tmux a -t mineru 回来看进度
    ```

    最轻量，**没有自动重启**——daemon 自己崩了不会自愈。

=== "nohup"

    ```bash
    nohup qatlas mineru --watch > qatlas-mineru.log 2>&1 &
    disown
    tail -f qatlas-mineru.log
    ```

    不依赖 tmux/screen，但同样没有自愈。

=== "systemd --user"

    `~/.config/systemd/user/qatlas-mineru.service`：

    ```ini
    [Unit]
    Description=QuantumAtlas MinerU contributor daemon
    After=network-online.target

    [Service]
    Type=simple
    ExecStart=%h/.local/bin/qatlas mineru --watch --batch-size 50
    Restart=on-failure
    RestartSec=30
    # 让它能在你登出后继续跑（默认 user instance 会随登出停掉）：
    #   loginctl enable-linger $USER

    [Install]
    WantedBy=default.target
    ```

    ```bash
    systemctl --user daemon-reload
    systemctl --user enable --now qatlas-mineru
    journalctl --user -fu qatlas-mineru
    ```

    **首选生产部署方式**——崩了自动重启，开机自启，logs 集中到 journal。

=== "Agent CLI（GitHub Copilot CLI / Claude Code 等）"

    把 daemon 挂在 agent 自己的后台 shell 里，让 agent 周期性自己看进度。
    Copilot CLI 用法：

    ```text
    # agent 内部：
    bash mode="async" shellId="mineru-watch"
      command: qatlas mineru --watch --batch-size 50 2>&1 | tee /tmp/qatlas-mineru.log

    # 过一阵子拉新 stderr 输出：
    read_bash shellId="mineru-watch"

    # 优雅停：
    bash: pgrep -af "qatlas mineru" | head -2
    bash: kill -INT <pid>   # SIGINT 触发"完成当前 batch 后退出"
    ```

    无 `detach=true` 时进程随 agent session 一起退；要跨 session 存活就用 systemd
    或 `bash detach=true`。**已实测**（v0.15.0 client + 生产 `quantum-atlas.ai`）
    在 agent CLI 里跑这条流程能完整走完 claim → submit → poll → upload → release，
    且 SIGINT 会被 daemon 接住做 graceful shutdown。

## 完整 flags

| Flag | 默认 | 含义 |
|---|---|---|
| `<arxiv_id>` (可选) | — | 指定单篇；省略走队列模式 |
| `--batch-size N` | 50 | 队列模式：每批最多多少篇（硬上限 50 = MinerU 单批限制）|
| `--max N` | — | **已弃用**，`--batch-size` 的兼容别名；两个都给时 `--batch-size` 优先 |
| `--continue-on-error` | false | 队列模式：单篇失败时继续（batch 模式下**隐式启用**——一篇失败不阻塞同批其它论文）|
| `--ttl-seconds N` | server 默认 1800 | claim 租约秒数（最长 7200）|
| `--no-cache` | false | 让 MinerU bypass 它的服务端缓存（重新跑）|
| `--overwrite` | false | server 已有 markdown / images 时仍允许覆盖 |
| `--no-push` | false | 跑 MinerU 但**不**推回 server（zip 留在本地 tmp，方便 debug）|
| `--watch` | false | daemon 模式：循环跑直到收 SIGINT/SIGTERM |
| `--watch-interval N` | 300 | daemon 模式 sleep 秒数（不影响 daily-limit 命中后的睡眠时长）|

加 [通用 client flags](manage-credentials.md#client-flags)。

## 错误分类（v0.15.0+）

`qatlas mineru` 把 MinerU 返回的 17 种 fatal code + 9 种 retryable code + 2 种 daily-limit code 按行为分三类：

| 类别 | 触发码 / 信号 | client 行为 |
|---|---|---|
| **DailyLimit** | `-60018` / `-60019` / HTTP 429 / 关键词（`限额`、`额度`、`tomorrow`、`5000` 等）| daemon 睡到次日 00:01；one-shot 退出 75 |
| **Fatal** | `A0202` / `A0211`（token）/ `-60002..-60017` / HTTP 401/403 | 释放 claim、log 错误并提示具体含义（如"页数超 200"、"token 过期"），**不重试**——人工介入 |
| **Retryable** | `-10001` / `-60001` / `-60007..-60010` / `-60020..-60022` / HTTP 5xx / 408 | 继续轮询（get_batch 失败时立即重试），下次 batch 自然带上 |
| 未分类 | 其它 | 保守处理：当作普通失败，**不**触发 daily-limit 退避 |

**为什么不实现客户端 PDF split**：MinerU 单文件上限 200 页（`-60006` Fatal），超长 paper 自动跳过 + 释放 claim。本地拆 PDF 后再合并 markdown 会破坏交叉引用、图片相对路径、表格连续性等结构信息，得不偿失。

!!! note "v0.15.0：batch + daily-limit"
    队列 / daemon 模式从 v0.15.0 起走 batch API。同等 PDF 数量比逐篇模式快约 N 倍（N = batch 大小，理由：每篇省一次 submit 往返 + 共享 MinerU 内部 batch scheduler）。Daily-limit 自动退避避免了 daemon 在配额耗尽后整夜空轮询的浪费。

!!! note "v0.8.0：不再丢图"
    旧版本 (`qatlas mineru` ≤ v0.7.x) 在 step 4 只从 zip 抽出 `full.md`，**所有 `images/*` 都被静默丢弃**，导致详情页图片引用 404。v0.8.0 改为把整个 zip 原样 push 给 server 端 `upload-mineru`，server 复用同款 zip 解析逻辑写入两个桶——client 端贡献的图片现在能完整落地。

## MinerU 环境变量（可选调优）

| 变量 | 默认 | 含义 |
|---|---|---|
| `mineru_api_token` | — | **必填**，从 <https://mineru.net> 拿 |
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
  → 201 {claim_id: "...", pdf_url: "<short-TTL presign URL>", expires_at: "..."}

DELETE /api/papers/<id>/mineru-claim/<claim_id>
  → 200 (释放)
```

server 维护 `<data_dir>/mineru-claims/*.json`：

- claim 期间，其他 client 对同一 arxiv_id 调 `mineru-claim` **会被拒（409）**
- 30 分钟（可调）后 server 自动认为放弃
- 处理完成 / 失败时 client 显式 DELETE 释放

`qatlas mineru` 自动处理整个生命周期（含异常时的释放），手工调 API 时务必保证 release。

## 常见问题

!!! failure "MINERU_API_TOKENS must be set"
    client 端 yaml 没配 `mineru_api_token:`。用 `qatlas config set mineru_api_token`
    无 value 触发隐藏粘贴框（JWT 不进 shell history / ps aux），从
    mineru.net 拿你的 JWT 粘进去即可：

    ```bash
    qatlas config set mineru_api_token
    # CI / 脚本：echo "$MINERU_API_TOKENS" | qatlas config set mineru_api_token
    # 老式直接编辑 yaml：echo 'mineru_api_token: eyJ...' >> "$(qatlas config path)"
    ```

!!! failure "skip (HTTP 409): paper already has markdown"
    Server 已有 markdown。要么跳过，要么 `--overwrite`。

!!! failure "skip (HTTP 409): paper already claimed by other client"
    别人正在跑。等 30 分钟租约过期，或换一篇。

!!! failure "MinerU task did not finish within MINERU_TIMEOUT=1800s"
    大论文（>50 页带很多图表）需要更长时间。`export MINERU_TIMEOUT=3600` 或单篇模式跑。

!!! failure "[daily-limit] MinerU quota exhausted; exiting 75 (EX_TEMPFAIL)"
    今日 5000 篇免费额度用完。一次性运行退出 75；CI 把 75 视为 transient，下次任务自然重试。要立即继续就改用 daemon 模式（`--watch`），它会自动 sleep 到次日 00:01。

!!! failure "[fatal] MinerU rejected batch submission: code -60006 (文件页数超过限制（最多 200 页）)"
    MinerU 单文件上限 200 页。本 paper 自动 skip + release claim；client 不重试。要解析需先手工拆分 PDF（**`qatlas mineru` 不实现客户端 split**——拆完后再 ingest）。

!!! failure "[fatal] MinerU rejected batch submission: code A0202 (Token 错误)"
    `mineru_api_token` 错或带 `Bearer ` 前缀。换 token 即可；client 不重试。

!!! failure "[fatal] MinerU rejected batch submission: code A0211 (Token 过期)"
    去 MinerU 重新拿 token，`qatlas config set mineru_api_token` 后重跑。

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
