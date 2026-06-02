# `qatlas` 客户端 CLI 参考

`qatlas` 是 Python 包 `quantum-atlas` 提供的 console script，按 `qatlas <subcommand>` 形式分发到子模块。

## 顶层

```
qatlas [--version] [--help] <subcommand> [args...]
```

| 顶层选项 | 含义 |
|---|---|
| `--version` / `-V` | 打印 `qatlas X.Y.Z` |
| `--help` / `-h` | 列出所有子命令 + 别名 |

## 通用 client flags

所有调 server 的子命令（ingest / upload / mineru 等）都接受这几个 flag：

| Flag | 默认 | 含义 |
|---|---|---|
| `--base-url <url>` | `$QATLAS_SERVER_URL` 或本地 .env 默认 | 显式指向 server |
| `--token <plaintext>` | (按 [precedence](../guides/manage-credentials.md#precedence)) | bearer token |
| `--insecure` | false / `$QATLAS_INSECURE=1` | 跳过 TLS 校验 |
| `--request-timeout <seconds>` | 120.0 | 单 HTTP 请求超时 |

---

## 客户端命令

### `qatlas ingest`

让 server 抓 arXiv 论文 + 可选解析。需要 `papers:write` scope。

```
qatlas ingest <arxiv_id> [--parser mineru] [options...]
qatlas ingest continue <task_id> [options...]
qatlas ingest status <task_id>
```

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | arXiv ID（旧式 `quant-ph/9508027` 或新式 `2501.00010`）|
| `--parser mineru` | ❌ | `mineru` | 显式声明解析器；开源版本只支持 `mineru` 一种 |
| `--stop-after fetch\|parse` | ❌ | — | 跑到指定阶段就停 |
| `--stages a,b` | ❌ | — | 逗号分隔的精确阶段列表 |
| `--force-fetch` | ❌ | false | 已有 PDF 也重抓 |
| `--force-parse` | ❌ | false | 已有 markdown 也重解析 |
| `--mineru-no-cache` | ❌ | false | bypass MinerU server-side cache |
| `--no-poll` | ❌ | false | 提交后立返，不等任务结束 |
| `--poll-interval <sec>` | ❌ | 1.0 | 轮询间隔 |
| `--timeout <sec>` | ❌ | 600 | 总等待时间上限 |

调用：`POST /api/ingest/paper`，轮询 `GET /api/ingest/{task_id}`。

详细 how-to：[从 arXiv 摄入论文](../guides/ingest-papers.md)。

---

### `qatlas upload pdf`

上传本地 PDF。需要 `papers:write` scope。

```
qatlas upload pdf <arxiv_id> --pdf <path> [--overwrite]
```

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | **必含版本** `vN`，如 `2501.00010v1` |
| `--pdf <path>` | ✅ | — | 本地 PDF 文件 |
| `--overwrite` | ❌ | false | 字节不同时允许覆盖（旧版本保留在 S3 versioning 中）|

调用：`POST /api/papers/{arxiv_id}/upload-pdf`（multipart），自动加 `?expected_sha256=<hex>`。

详细 how-to：[上传 PDF / Markdown](../guides/upload-assets.md)，详细 API：[Upload API](upload-api.md)。

---

### `qatlas upload mineru`

上传 MinerU 结果 zip（`full.md` + 可选 `images/*`）。server 解包后 markdown 落 `qatlas-md` 桶、每张图落 `qatlas-images/<canonical>/`。需要 `papers:write` scope。

```
qatlas upload mineru <arxiv_id> --zip <path> [--source <tool>] [--overwrite]
```

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 必含版本 |
| `--zip <path>` | ✅ | — | 本地 MinerU 结果 zip（保留原样，不要自己解开）|
| `--source <tool>` | ❌ | — | 解析 pipeline 名（写入审计：mineru-client-v0.8 / manual / ...）|
| `--overwrite` | ❌ | false | 字节不同时允许覆盖（markdown 和 images 分别走 dedup）|

调用：`POST /api/papers/{arxiv_id}/upload-mineru`。

!!! warning "v0.8.0 BREAKING"
    旧的 `qatlas upload markdown` 已**删除**。原命令只接受 `.md` 单文件、会丢图；改成 `upload mineru` 推完整 zip。详见 [Upload API §Breaking change v0.8.0](upload-api.md#breaking-change-v080)。

---

### `qatlas mineru`

本地跑 MinerU 解析 server 上的 PDF，推回完整 MinerU bundle。需要 `papers:write` scope + 本地 `MINERU_API_TOKEN`。

```
qatlas mineru [arxiv_id] [options...]
```

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` (可选) | ❌ | — | 指定单篇；省略走队列模式 |
| `--max N` | ❌ | 10 | 队列模式最多处理几篇 |
| `--continue-on-error` | ❌ | false | 队列模式：单篇失败继续下一篇 |
| `--ttl-seconds N` | ❌ | server 默认 1800 | claim 租约（最长 7200）|
| `--no-cache` | ❌ | false | 让 MinerU bypass 服务端缓存 |
| `--overwrite` | ❌ | false | server 已有 markdown 时仍允许覆盖 |
| `--no-push` | ❌ | false | 跑 MinerU 但不推回（留 tmp zip）|
| `--watch` | ❌ | false | daemon 模式：跑完一轮 sleep `--watch-interval` 再继续（Ctrl-C 干净退出）|
| `--watch-interval N` | ❌ | 300 | daemon 模式 sleep 秒数 |

调用链：`POST /api/papers/{id}/mineru-claim` → MinerU API → **`POST /api/papers/{id}/upload-mineru`**（推完整 zip）→ `DELETE /api/papers/{id}/mineru-claim/{cid}`。

详细：[用 MinerU 解析](../guides/parse-with-mineru.md)。

---

### `qatlas markdown`

取某篇论文的 Markdown：**有缓存直接给，无缓存时 server 用自身 token 静默后台转换**，本命令轮询直到 ready。开放读，**不需要** `papers:write` scope 或本地 MinerU token——配额由 server 出。与 `qatlas mineru`（贡献者用自己 key 主动跑）是两个层面，互不冲突。

轮询语义：先 GET content 端点触发转换（命中缓存即直接拿到正文）；拿到 `202` 后从 `Operation-Location` 头解析 job 状态资源 URL，轮询该资源直到 `status==done`，再取一次正文。轮询节奏为**带 jitter 的指数退避**，下界尊重 server 的 `Retry-After` 头，上界为 `--max-poll-interval`。

```
qatlas markdown <arxiv_id> [options...]
```

| Flag | 必填 | 默认 | 含义 |
|---|---|---|---|
| `<arxiv_id>` | ✅ | — | 带版本号，如 `2501.00010v1` |
| `-o, --output <file>` | ❌ | stdout | 写到文件而非 stdout |
| `--poll-interval N` | ❌ | 3.0 | 轮询退避的起始间隔（秒）；实际间隔指数增长并加 jitter |
| `--max-poll-interval N` | ❌ | 30.0 | 轮询退避的上限间隔（秒）|
| `--timeout N` | ❌ | 1800.0 | 等待 pending 转换的最长秒数；超时退出 75，server 仍在跑，稍后重跑即可 |
| `--no-wait` | ❌ | false | 不轮询：server 仍在转就立即退 `75` (EX_TEMPFAIL) |

退出码：`0` 拿到 markdown；`75` (EX_TEMPFAIL) 仍在转换 / 超时（`--no-wait` 或 `--timeout` 触发）；`1` 转换失败或无 PDF。

调用：`GET /api/papers/{id}/markdown` 触发 + `GET /api/papers/{id}/markdown/status` 轮询（详见 [REST API](rest-api.md)）。

---

### `qatlas auth`

管理本地存储的 PAT。

```
qatlas auth login [-H <host>] [--token <plaintext>] [--with-token]
qatlas auth logout [-H <host>]
qatlas auth status [-H <host>]
qatlas auth token [-H <host>]
```

| 子命令 | 行为 |
|---|---|
| `login` | 交互式（或 `--token` / `--with-token < file`）存 PAT 到 `~/.config/qatlas/hosts.yml` |
| `logout` | 删该 host 的本地条目（不调 server）|
| `status` | 列已登录的 host + 脱敏 token 预览 |
| `token` | 把指定 host 的明文 token 打到 stdout（用于 shell 替换：`curl -H "Authorization: Bearer $(qatlas auth token)"`）|

| Flag | 默认 | 含义 |
|---|---|---|
| `-H` / `--host` | `$QATLAS_SERVER_URL` 或交互式询问 | 操作哪个 host |
| `-t` / `--token` | — | 给 `login` 用：非交互直接传 PAT 明文 |
| `--with-token` | false | 给 `login` 用：从 stdin 读 PAT |

文件 layout 是 YAML，0600 权限，详见 [管理凭据](../guides/manage-credentials.md)。

---

## Wiki 工具命令

### `qatlas wiki`

```
qatlas wiki <list|show|search|links|lint|sync|stats|ingest|create> [options...]
```

| 子命令 | 主要 flag | 行为 |
|---|---|---|
| `list` | `--type <T> --tags a,b --status published` | 列 Wiki 页面（本地 git checkout）|
| `show <page_id>` | `--raw` | 展开页面（pretty / raw markdown）|
| `search <query>` | `--limit 10` | 全文搜索 |
| `links <page_id>` | `--backlinks` | 列出/反列页面间链接 |
| `lint` | `--fix --verbose` | 运行所有 W001–W008 检查 |
| `stats` | — | 仓库统计：页面数 / 按类型 / 按 status |
| `ingest <arxiv_id>` | `--no-fetch --no-parse --no-extract` | 旧 monolith pipeline（开发期，新代码用 `qatlas ingest`）|
| `create <id>` | `--title T --type entity --category primitive --tags a,b --status draft --content ... --file ... --subdir ...` | 生成页面模板文件 |

详细：[写 Wiki 页面](../guides/write-wiki-pages.md) / [Lint](../guides/lint-wiki.md) / [Schema](wiki-schema.md)。

---

## 电路工具命令

### `qatlas designer`

```
qatlas designer <algorithm_id> [-o <path>] [--n-qubits N] [--params k=v,...] [--no-optimize]
```

把 algorithm Wiki page 编译成 Quantum IR。详细：[电路工具链](../guides/circuit-toolchain.md)。

### `qatlas codegen`

```
qatlas codegen <ir_file> --backend qiskit|qpanda [-o <path>] [--include-imports] [--measure-all]
```

IR → 后端代码。

### `qatlas validator`

```
qatlas validator <ir_file> [--compare-with <algo_id>] [--check-codegen <code_file>]
                 [--method unitary|statevector|sampling] [--n-shots 1024]
```

验证 IR 或生成代码的正确性。

### `qatlas estimator`

```
qatlas estimator <ir_file> [--format markdown|json] [-o <path>]
                 [--hardware <name>] [--detailed]
```

资源估计（gate 计数、depth、两比特门数、wall time 估算）。

### `qatlas extractor`（实验性）

LLM 辅助从 paper markdown 抽取算法描述。需要 `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`。

---

## 运维 / 兼容性命令（不常用）

| 命令 | 说明 |
|---|---|
| `qatlas parser` (alias `parse`) | 旧 monolith parser CLI；新代码用 `qatlas ingest` |

## 别名

| 别名 | 等同于 |
|---|---|
| `parse` | `parser` |
| `design` | `designer` |
| `generate` | `codegen` |
| `validate` | `validator` |
| `estimate` | `estimator` |
| `extract` | `extractor` |

---

## 环境变量影响

| 变量 | 谁读 | 作用 |
|---|---|---|
| `QATLAS_SERVER_URL` | 所有调 server 的命令 | 默认 server URL |
| `QATLAS_TOKEN` | 同上 | 默认 bearer token |
| `QATLAS_INSECURE` | 同上 | 默认跳过 TLS 校验 |
| `QATLAS_WIKI_DIR` | `wiki` 子命令 | 本地 Wiki git checkout 路径 |
| `MINERU_API_TOKEN` 等 `MINERU_*` | `mineru` 命令 | 本地 MinerU 调用配置 |
| `XDG_CONFIG_HOME` | `auth` 命令 | hosts.yml 父目录 |

完整列表：[环境变量参考](env-vars.md)。
