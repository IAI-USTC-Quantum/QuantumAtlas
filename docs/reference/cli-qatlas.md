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

## 通用 client flag

所有调 server 的子命令（ingest / upload / mineru 等）都接受这个 flag：

| Flag | 默认 | 含义 |
|---|---|---|
| `--request-timeout <seconds>` | 120.0 | 单 HTTP 请求超时（per-call 临时调） |

!!! info "v0.17.0 删除了 `--base-url` / `--token` / `--insecure` flag"
    服务器 URL / token / TLS 选项现在**只能**写在 `~/.config/qatlas/config.yaml`。短命令调用是 client 的主要使用场景，每次重新指 server 反而麻烦。如果有"两个项目走两个 server"的需要，用 `XDG_CONFIG_HOME=/path/to/other-config qatlas ...` 临时切（freedesktop 标准机制）。

---

## 客户端命令

### `qatlas config`

inspect / edit user-level YAML 配置文件（路径按平台，见下方表）。**首次跑任何 `qatlas <cmd>` 自动创建模板**——不再需要 `qatlas config init` 步骤。给 `uv tool install` 用户用，不需要 `export` 任何 env。

**跨平台配置文件路径**（由 [`platformdirs`](https://platformdirs.readthedocs.io/) 解析，跟主流 Python CLI 一致）：

| 平台 | 默认路径 | 控制 env |
|---|---|---|
| Linux | `~/.config/qatlas/config.yaml` | `XDG_CONFIG_HOME` |
| macOS | `~/Library/Application Support/qatlas/config.yaml` | — |
| Windows | `%APPDATA%\qatlas\config.yaml` | `APPDATA` |

不确定具体到哪 → `qatlas config path` 打给你。本文档后面的例子用 Linux 形式 `~/.config/qatlas/config.yaml`，mac / win 用户照着替换路径即可。

```
qatlas config <subcommand>
```

| Subcommand | 含义 |
|---|---|
| `path` | 打印 yaml 文件路径（无论是否存在） |
| `set <key> <value>` | 写一个 key=value。`key` 用 **snake_case** YAML 字段名（如 `server_url` / `token` / `mineru_api_token`），不再用 env-var 大写形式。文件不存在时自动建（0600 perms）。敏感字段（含 `token` / `secret` / `key` / `password`）echo 时遮罩 |
| `unset <key>` | 删除一个 key |
| `get <key>` | 打印 key 在 yaml + Field default overlay 后的真值。无值时 exit 1，适合 shell 插值 |
| `show [--unmask]` | dump 所有字段（snake_case key: value 形式）；敏感值遮罩，`--unmask` 完整打印 |

**配置入口**（v0.17.0+ 极简）：

1. **平台原生配置文件路径**（见上表）— **唯一**配置源（首次跑任意命令自动创建）
2. **内置 Field default** — 各字段在 `qatlas/config.py` 的 `ServerConfig` 上定义

没有 CLI flag overrides，没有 OS env vars，没有 `$QATLAS_DOTENV` / `$QATLAS_CONFIG`。这是有意的极简化——client 用户基本是"配一次长期用"的模式，多入口反而增加心智负担（v0.16 起 client 不再借 server 的 `.env` 跑）。

!!! note "想换 config 文件位置？用平台标准 env"
    - **Linux**: `XDG_CONFIG_HOME=/etc/myconfig qatlas <cmd>` → yaml 落 `/etc/myconfig/qatlas/config.yaml`
    - **macOS**: 没有 platformdirs 支持的标准 env，唯一办法是 symlink `~/Library/Application Support/qatlas/`
    - **Windows**: `APPDATA=D:\my-config qatlas <cmd>` → yaml 落 `D:\my-config\qatlas\config.yaml`

    都是 freedesktop / Microsoft / Apple 各自平台的原生机制（platformdirs 透传），不是 qatlas 自己定义的 override。

!!! warning "`qatlas config set` 会抹掉手写注释"
    PyYAML 不保留 round-trip 注释，跟 `gh` / `kubectl config set` 行为一致。要永久注释，直接编辑 yaml 不用 `set`。auto-init 写出的 header 注释每次 `set` 都会被重写，是预期行为。

**典型 workflow**：

```bash
# 首次安装 + 配置（v0.17.0+）
uv tool install --prerelease=allow quantum-atlas
qatlas --help                                       # 任意命令都触发 yaml 自动创建
qatlas config set server_url https://quantum-atlas.ai
qatlas config set token qat_xxxxxxxx                # 从 https://quantum-atlas.ai/pat 拿
qatlas config set mineru_api_token eyJ0eXBlI...     # 若要跑 qatlas mineru

# 看 yaml 路径
qatlas config path
# /home/you/.config/qatlas/config.yaml

# 看效果
qatlas config show
# server_url: https://quantum-atlas.ai
# token: qat_…xxxx (12 chars)
# ...

# 之后任何 qatlas 子命令直接跑
qatlas wiki list --type source
qatlas mineru --batch-size 3
```

> v0.16 / 更早升级到 v0.17.0：删了 `qatlas config init` 子命令、删了 `.env` → yaml 自动迁移。如果你之前在用 `~/.config/qatlas/.env`，**手工**把内容搬到 `~/.config/qatlas/config.yaml`（字段名从 `QATLAS_SERVER_URL` 改成 `server_url`、`MINERU_API_TOKEN` 改成 `mineru_api_token`，去掉 `QATLAS_` 前缀小写化），原 .env 删掉。

详细：[管理凭证](../guides/manage-credentials.md)、[入门](../getting-started.md)。

---

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
| `--batch-size N` | ❌ | 50 | 队列模式：每批最多多少篇（硬上限 50 = MinerU 单批限制）|
| `--max N` | ❌ | — | **已弃用**，`--batch-size` 的兼容别名；两个都给时 `--batch-size` 优先 |
| `--continue-on-error` | ❌ | false | 队列模式：单篇失败继续下一篇（batch 模式下隐式启用）|
| `--ttl-seconds N` | ❌ | server 默认 1800 | claim 租约（最长 7200）|
| `--no-cache` | ❌ | false | 让 MinerU bypass 服务端缓存 |
| `--overwrite` | ❌ | false | server 已有 markdown 时仍允许覆盖 |
| `--no-push` | ❌ | false | 跑 MinerU 但不推回（留 tmp zip）|
| `--watch` | ❌ | false | daemon 模式：跑完一批 sleep `--watch-interval` 再继续（Ctrl-C 干净退出；daily-limit 命中自动 sleep 到次日 00:01）|
| `--watch-interval N` | ❌ | 300 | daemon 模式 sleep 秒数（不影响 daily-limit 命中后的睡眠时长）|

**退出码**：成功 = 0；失败 = 1；MinerU 每日额度耗尽 = 75（`EX_TEMPFAIL`，CI 可视为 transient 重试）。

调用链（单篇模式）：`POST /api/papers/{id}/mineru-claim` → MinerU 单 task → `POST /api/papers/{id}/upload-mineru` → `DELETE /api/papers/{id}/mineru-claim/{cid}`。

调用链（队列 / daemon 模式，v0.15.0+）：list `needs-mineru` → 逐篇 `mineru-claim` → 一次 `POST /api/v4/extract/task/batch` → 周期 `GET /api/v4/extract-results/batch/{id}` → 每 done 立即 `upload-mineru` + `DELETE mineru-claim`。

详细：[用 MinerU 解析](../guides/parse-with-mineru.md)。

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
