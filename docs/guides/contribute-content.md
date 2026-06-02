# 贡献工作流

这份文档描述如何把内容贡献到 QuantumAtlas，包括两条并列的协作路径：

- **Raw 资产贡献**：把论文 PDF、解析 Markdown、元数据落到 `RAW_DIR`。
- **Wiki 协作**：在独立 Git 仓库里编辑知识页面，按需触发服务器拉取。

如果你想理解项目分层和设计动机，先看 [architecture.md](../concepts/architecture.md)。如果你只想跑起来一个服务，看 [deployment.md](../deployment/operations.md)。本文档定位是「内容贡献的 how-to」。

---

## 0. 配置加载约定

服务端与所有 `qatlas` 客户端命令都通过 [pydantic-settings](https://docs.pydantic.dev/latest/concepts/pydantic_settings/) 从仓库根目录的 `.env` 自动读取配置（见 `atlas/server/config.py`）。

- 项目自有字段统一加 `QATLAS_` 前缀（如 `QATLAS_SERVER_URL`、`QATLAS_WIKI_DIR`、`QATLAS_USER_HEADER`）；旧名（`PUBLIC_BASE_URL`、`WIKI_DIR`、`USER_HEADER` 等）作 alias 兼容。
- 第三方 / SDK 标准名（`NEO4J_*`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`MINERU_*`）**不加**前缀。
- 已有的 OS 环境变量优先级高于 `.env`。
- 临时关闭 `.env` 加载：`QATLAS_SKIP_DOTENV=1`（alias: `QUANTUMATLAS_SKIP_DOTENV`）。

**客户端 `.env` 只需要写自己用到的几项**，不需要写服务端字段：

```env
# 用于 qatlas 命令默认指向哪台服务器（也可以每次用 --base-url 显式传）
QATLAS_SERVER_URL=https://quantum-atlas.ai

# 远端是自签 HTTPS（开发环境 Caddy `tls internal`）时打开；等价于 CLI 的 --insecure
# QATLAS_INSECURE=1

# 仅在使用 qatlas mineru 本地解析时需要
MINERU_API_TOKEN=mn_xxxxx
# 其余 MINERU_* 字段都有合理默认值，按需覆盖
```

不需要在客户端写 `QATLAS_SERVER_HOST` / `QATLAS_SERVER_PORT` / `NEO4J_*` / `QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` 这些纯服务端字段——它们在客户端 .env 里出现也无害，但毫无意义。`QATLAS_WIKI_DIR` 在 client 上也有用，指向本地 clone 的 wiki 仓库。

服务端的 `.env` 见 [deployment.md](../deployment/operations.md) 的「推荐的单机生产目录」段。

---

## 1. Raw 资产贡献的三条路径

`RAW_DIR` 是论文资产的 canonical store（默认 `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw`，或显式 `QATLAS_RAW_DIR` 覆盖）。文件名遵循 arXiv 规范并按 YYMM 分片：

| 风格 | 例子 | 存储路径（相对 `$RAW_DIR`） |
|---|---|---|
| 老式（pre-Apr 2007） | `quant-ph/9508027v1` | `pdf/9508/9508027v1.pdf` |
| 新式（post-Apr 2007） | `2501.00010v1` | `pdf/2501/2501.00010v1.pdf` |

`markdown/` / `json/` / `images/` 子目录采用同样的分片布局。版本号 `vN` 是**强制**的——所有 upload 端点都拒绝不带版本的 ID。

下面三条路径都会落到这一套布局。选哪条取决于谁出算力、谁出资产。

### Path A：服务器侧按 arXiv ID 抓取

最轻量。贡献者只需要给一个 arXiv ID，服务端负责抓 PDF 并解析（fetch + parse）。**服务端严格 ff-only，不会自动跑 LLM 抽取，也不会写 wiki 或 Neo4j**——抽取/wiki 这两步走人工 PR 流程。

```bash
qatlas ingest quant-ph/9508027              # 默认走 MinerU 解析
qatlas ingest 2501.00010 --parser mineru   # 显式声明也可以，等同上一行
```

`--parser` 是**可选**——开源版本目前只支持 `mineru` 这一个 parser，省略则按默认走。
`qatlas ingest` 会同步走 fetch + parse 流水线并轮询任务状态。完整选项：

```bash
qatlas ingest --help
```

关键选项：

| 选项 | 行为 |
|---|---|
| `--parser mineru` | 可省略；保留为显式 flag 是为了将来 wire 协议扩展（万一以后又加了新 parser，签名不动）。`mineru` 需要服务端配置 `MINERU_API_TOKEN` |
| `--stop-after fetch` / `--stop-after parse` | 在指定阶段后停止（`parse` 是末尾阶段，等价于跑完） |
| `--stages a,b` | 只跑明确列出的阶段（`fetch` / `parse`），跳过的阶段如果有本地资产会被复用 |
| `--force-fetch` / `--force-parse` | 即使本地已有 PDF/Markdown 也强制重做 |
| `--mineru-no-cache` | 让 MinerU 绕过它自己的缓存 |
| `--no-poll` | 提交后立即返回，不等任务完成 |

任务状态可以单独查询：

```bash
qatlas ingest status TASK_ID
```

要在 `stop_after=fetch` 之后续跑 parse，用：

```bash
qatlas ingest continue TASK_ID --stages parse
```

底层端点：

- `POST /api/ingest/paper`
- `POST /api/ingest/{task_id}/continue`
- `GET /api/ingest/{task_id}` / `GET /api/ingest/stages` / `GET /api/ingests`

### Path B：鉴权用户直接上传 PDF

适用场景：贡献者本地已有 PDF（或 arXiv 不可达），希望直接把资产推到服务器。

```bash
qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf
qatlas upload pdf 2501.00010v1     --pdf paper.pdf --overwrite
```

对应端点 `POST /api/papers/{arxiv_id}/upload-pdf` 行为：

- arXiv ID 必须携带版本号 `vN`，否则 400。
- 对 PDF 做最基本的 `%PDF-` magic 校验，不通过 400 并清理临时文件。
- 默认拒绝覆盖（409）；显式 `--overwrite` 才允许替换。
- 单文件大小上限：PDF 100 MiB。
- 论文 metadata（题目 / 作者 / 摘要 / 引用）由服务器从 OpenAlex 上游同步进 Neo4j catalog，不再走 upload 端点（v0.7.0 起）。

上传 MinerU 结果包用：

```bash
qatlas upload mineru 2501.00010v1 --zip mineru-result.zip --source mineru
```

对应端点 `POST /api/papers/{arxiv_id}/upload-mineru`：

- 接受完整 MinerU 结果 zip（必含 `full.md`，可选 `images/*`）。
- server 端解包：`full.md` 写到 `qatlas-md` 桶，每张 `images/<name>` 写到 `qatlas-images/<yymm>/<stem>/`。
- 单文件上限 200 MiB（zip 整体）。
- `source` 是可选审计标签（如 `mineru-client-v0.8`、`hand-edited`），不影响存储位置。

!!! info "v0.8.0 BREAKING"
    旧的 `qatlas upload markdown` 子命令和 `POST upload-markdown` 端点在 v0.8.0 删除（旧路径会丢图）。改用 `upload mineru` 推完整 zip。

### Path C：本地跑 MinerU 后把解析结果推回云端

适用场景：贡献者愿意用**自己的** MinerU 配额（`MINERU_API_TOKEN` 写在自己电脑的 `.env`），服务器配置可以完全不带这个 token。

**前置条件**：要处理的 PDF 必须已经在服务器的 `RAW_DIR` 里（通过 Path A 或 Path B 进入）。`qatlas mineru` 不接受用户自带 URL——这是为了保证生成的 markdown 永远对应 raw 中已知的 PDF，不会产生孤儿数据。

```bash
# 队列模式：从服务器的“需要 MinerU”队列里取至多 --max 个还没人处理的论文，
# 逐个领取（claim）→ 跑 MinerU → 上传完整 zip → 自动释放 claim。
qatlas mineru
qatlas mineru --max 20 --continue-on-error

# 单篇模式：处理指定 arxiv 论文（同样要求服务器已有 PDF）。
qatlas mineru quant-ph/9508027v1

# daemon 模式：挂着持续贡献，跑完一轮 sleep --watch-interval 再来。
qatlas mineru --watch
qatlas mineru --watch --watch-interval 600 --max 5

# 只跑 MinerU 不上传（zip 留在本地临时目录，claim 立刻释放）
qatlas mineru 2501.00010v1 --no-push
```

**并发模型（claim/lease）**：

- 想处理一篇 → `POST /api/papers/{arxiv_id}/mineru-claim` 申请一个短期租约（默认 30 分钟，最长 2 小时，可用 `--ttl-seconds` 调整）。
- 服务端原子地写 `DATA_DIR/mineru-claims/{key}.json`；如果已有未过期 claim → 409 + 现有租约元数据。
- 上传成功 → 服务端自动删除 claim。
- 客户端异常中断 → 用 `DELETE /api/papers/{arxiv_id}/mineru-claim/{claim_id}` 主动释放（`qatlas mineru` 在 except 分支和 SIGINT 处理里都会自动调用）。
- 没释放也没事：lease 到期后会被下一个 claim 请求覆盖。

`qatlas mineru` 流程：

1. 从本地 `.env` 读 `MINERU_API_TOKEN` 等字段。
2. 没传 arxiv_id → `GET /api/papers/needs-mineru?limit=<max>` 取队列；传了就只处理那一篇。
3. 对每篇 → POST claim；服务端返回 `pdf_url`（指向 arxiv.org 原 PDF URL）+ `claim_id`。
4. 提交 MinerU → 按 `MINERU_POLL_INTERVAL` / `MINERU_TIMEOUT` 轮询。
5. 完成后下载**整个 MinerU 结果 zip** → POST `/api/papers/{arxiv_id}/upload-mineru?source=mineru`。server 解包写两桶（markdown + images），上传成功 = claim 自动释放。
6. 异常路径（MinerU 失败 / 上传失败 / Ctrl+C）→ DELETE claim 主动释放。

也可以纯手工：用任意解析器在本地生成 MinerU-shape zip，再走 Path B 的 `qatlas upload mineru`（不涉及 claim 机制，但仍要求 PDF 先在 raw 里）。

---

## 2. 鉴权与审计

服务器使用 PocketBase 内嵌的 GitHub OAuth 流程做浏览器登录，并通过 `authGuard`（`internal/routes/auth.go`）门禁写操作。读口（wiki / pages / stats / search / graph / lint）保持公开（因为 wiki 仓库本身就是公开的）。

`authGuard` 接受**三种**凭据，按到达顺序检查：

1. **System PAT**（环境变量加载） —— server 启动时从 `QATLAS_SYSTEM_PAT` env 读取；命中常时比较直接通过 authGuard。永不过期、跟 PocketBase 完全解耦、pb_data 挂了还能用——专供 CI / cron / 灾难恢复等 ops 路径。详见 [auth-model.md § System PAT](../concepts/auth-model.md#system-pat)。
2. **Personal Access Token (PAT)** —— bearer 以 `qat_` 开头，从 SPA `/pat` 页面创建，明文一次性显示。**强制设置过期时间**（7 / 30 / 60 / 90 / 365 天，最长 1 年）。**每条 PAT 携带显式 scope 列表**，默认空集 = 什么写口都调不了，必须勾选具体 scope 才能用。撤销 = 同页 Revoke。**人工脚本 / 想钉到具体身份的 ingest 工具推荐这条。**
3. **PocketBase 用户 session token** —— OAuth 登录后浏览器自动持有（`pb.authStore`），SPA 内所有调用自动带在 `Authorization` 头里。**没有 UI 入口去手动 copy**——只在浏览器里用得了。隐式拥有全部权限，跳过 scope 检查。

任何写口都同时接受这三种形式；非浏览器调用在 `Authorization: Bearer <...>` 里塞 PAT 或 system PAT 都行。

### Scope 词表

参考 GitHub fine-grained PAT 设计。**没勾任何 scope 的 PAT 调写口直接 403**——这是有意为之的安全默认。

| Scope | 覆盖端点 | 说明 |
|---|---|---|
| `papers:write` | `POST /api/papers/.../upload-pdf` / `upload-mineru` / `mineru-claim`，`DELETE .../mineru-claim/{id}` | 上传 PDF / MinerU 结果包、跑 MinerU 任务 |
| `papers:read` | `GET /api/papers/...` 各只读 endpoint（stats / needs-mineru 等） | 读取 paper catalog 元数据 |
| `wiki:read` / `wiki:write` | `/api/wiki/*` | wiki 内容只读 / 同步 |
| `graph:read` | `/api/graph/*` | Neo4j 查询 |

scope 的 obj/act 在 `scopeGuard` 抛 403 时会回显在 `detail` 里——CLI 报错能直接告诉你"该 PAT 缺 `papers:write` scope，去 /pat 重发一条"。

底层用 [casbin](https://casbin.org/) 做 enforce，model + policies 都 hardcode 在 `internal/pat/scopes.go`（不外置文件，部署简单）；将来加 path-pattern scope（如"仅允许 quant-ph/* 命名空间"）改 matcher 为 `keyMatch` 即可，调用方代码无需动。

**Scope 运维须知**（仅当你在改 scope 词表或部署 server 时需要看）：

- enforcer 在 `cmd/qatlasd/main.go` 启动时一次性构建（`pat.NewEnforcer()`），失败 = `log.Fatalf` 进程退出，**没有降级路径**。启动日志里看到 `build PAT scope enforcer: ...` 是 fatal 不是 warning——通常意味着改 `scopes.go` 时引入了 model / policies 语法错误，回滚或修语法再重启。
- 词表是**编译时静态**的（`scopes.go::scopePolicies` 表 + `AllScopes` 切片），加 / 删 scope = 改代码 + 重新编译 + 重启 server，**不支持**运行时 `AddPolicy` / 热加载。也正因如此 enforcer 实例可以安全在所有 handler 间共享（`Enforce()` 并发读安全）。
- 新加 endpoint 时如果忘记把 enforcer 传给 `scopeGuard`（传 `nil`），那个 endpoint 会对**每个**请求返回 500 `"scopeGuard: enforcer not configured (server wiring bug)"`——fail loud 而不是放行。code review 时确认所有 `RegisterXXX(se, cfg, ..., enforcer)` 签名一路传递。
- `pat_tokens.scopes` 列 JSON 损坏时（手动编辑 / 迁移失误），`decodeScopes` 返回 nil，该 PAT 被视为"无 scope"，所有写口收到 403，**不会**返回 500。修复直接在 admin UI 或 SQLite CLI 改回合法 JSON。fail-closed 行为由 `internal/routes/auth_test.go::TestDecodeScopes` 钉住。

### 获取 (1) PAT

**A. 浏览器（多数人）**

1. 浏览器登录后打开 `https://<server>/pat`；
2. "New token" → 填名字（如 `nightly-ci`）+ 可选 description + 必选过期天数（默认 90 天）+ 勾选需要的 scope；
3. 服务器返回的明文**只显示一次**——立即拷到 GH Actions secret / systemd `EnvironmentFile` / 本地 `.env`；
4. 之后这条 PAT 在列表里只显示前缀（`qat_xxxxxxxx…`）、scope 标签和 last-used 时间戳，需要换号就 Revoke 再创建一条。

**B. 服务端 shell（运维 / CI 自动化）**

如果你 SSH 上了部署服务端二进制的主机，可以直接用 `qatlasd pat` 子命令而不开浏览器：

```bash
sudo -u qatlasd /opt/quantum-atlas/qatlasd pat mint \
    --user me@example.com --name nightly-ci \
    --scopes papers:write --expires-in-days 365
# stdout: qat_xxxxxxxxxxxxxxxxxxxxxxxxxxx
# stderr: minted PAT id=... prefix=qat_xxxx... user=me@example.com scopes=[papers:write] expires_at=...
```

`mint` 输出的明文走 stdout（可以 `SECRET=$(qatlasd pat mint ...)` 直接捕获到变量），元数据走 stderr。配套 `list` / `revoke <id>` / `scopes` 三个子命令分别看现状、撤销、查 scope 词表。这条路径绕开 `sessionGuard`（前提：你已经是有 shell 权限的运维人，DB 文件你本来就能直接改），适合 nightly secret 之类不开浏览器的场景。

**PAT 不能用来管理 PAT**：`/api/pat` HTTP 端点用 `sessionGuard` 而非 `authGuard`——只接受浏览器 session token，PAT auth 一律 403。这跟 GitHub fine-grained PAT 一致：一条 PAT 即便泄露也不能 mint 出更多 PAT，限制爆炸半径。服务端 CLI 之所以可以"绕过"，是因为执行者已经是 shell 权限持有者，跟 HTTP 远端调用是两套信任模型。

### 获取 (2) Session token

浏览器登录 GitHub OAuth 后，session 自动存在 `pb.authStore`（前端的 localStorage 里），SPA 调用 `/api/*` 时自动附 `Authorization: Bearer <session>`，14 天到期会自动续期，**无需手动复制 / 粘贴**。

**没有 `/token` 这种 UI 页面让你 copy session token**——如果你想脱离浏览器调用（CLI / curl / CI），请用上面的 PAT（人工身份）或 System PAT（运维身份）。这是有意设计的限制：session token 短期、自动续期、绑浏览器；要给"长跑 / 自动化"用就该走专门的长寿命凭据。

涉及到的服务端配置：

- `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET`：启动时把 GitHub OAuth provider 注入 users collection（`internal/auth/oauth.go::syncGitHubProvider`，幂等）。
- `QATLAS_ADMIN_GITHUB_LOGINS`：GitHub 用户名白名单（逗号分隔），未来用于自动 admin 提权（handler 待补）。
- `QATLAS_USER_HEADER`：仍可用，反代/SSO 注入审计头时由 upload 端点写入 `uploaded_by` 字段；与 PocketBase auth 并行，不互斥。

PAT 的实现说明：每条 PAT 在 SQLite `pat_tokens` 集合里只存 bcrypt 哈希（`token_hash` 字段 hidden=true，admin UI 都不显示），明文从不持久化；scope 列表以 JSON 字符串存在 `scopes` 字段；过期时间存 `expires_at`（强制非空）；服务端用 `last_used_at` 记录每次成功认证的时间戳。代码见 `internal/pat/{pat.go,scopes.go,migrations.go}` 和 `internal/routes/{pat.go,auth.go,scope_guard.go}`。

客户端 CLI 调用写口时，要带 bearer token。**推荐用 `qatlas auth login`**（类似 `gh auth login`），凭证存在 `~/.config/qatlas/hosts.yml`，之后任何 `qatlas` 子命令自动捡起来，不必每次 `export`：

```bash
# 一次性配置：
qatlas auth login -H quantum-atlas.ai
# Paste your PAT plaintext: <paste qat_... here, hidden input>

qatlas auth status       # 查看已登录的所有 host
qatlas auth token        # 打印当前 host 的 token，方便 pipe 给 curl
qatlas auth logout -H quantum-atlas.ai   # 撤销本地凭证

# 之后所有 qatlas 子命令直接用：
qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf
```

token 解析优先级（同 `gh`）：`--token` 命令行 > `QATLAS_TOKEN` 环境变量 > `~/.config/qatlas/hosts.yml` 里匹配当前 host 的条目 > 无凭证。

**纯环境变量方式**仍然支持（适合一次性脚本 / CI runner）：

```bash
export QATLAS_SERVER_URL=https://quantum-atlas.ai
export QATLAS_TOKEN=qat_xxxxx                    # 或 --token 命令行覆盖

qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf

# 直接 curl 也行：
curl -X POST -H "Authorization: Bearer $QATLAS_TOKEN" \
  -F pdf=@paper.pdf -F metadata=@meta.json \
  "https://quantum-atlas.ai/api/papers/quant-ph/9508027v1/upload-pdf?overwrite=true"
```

具体反代配置（Caddy 现在已经是纯 reverse_proxy）见 [deployment.md](../deployment/operations.md)。

---

## 3. Wiki 协作：以 Git 仓库为主边界

Wiki 内容**不必经过服务器**就能更新。推荐做法是把 Wiki 仓库作为应用仓库的兄弟目录 checkout，所有人都直接对它 clone / branch / push / PR：

```text
~/work/
├── QuantumAtlas/          # 应用代码仓库
└── QuantumAtlas-Wiki/     # Wiki 内容仓库（任意人都可以 clone 编辑）
```

应用侧只需要在 `.env` 里指向这个 checkout：

```env
QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
```

服务器上的 Wiki checkout 应当保持干净——它**只读**，且只接受 fast-forward 更新。当 Wiki 仓库有新 commit 被合入主分支后，任何能访问服务器 API 的客户端都可以触发服务器拉取，无需登录服务器 shell：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  https://atlas.example/api/wiki/sync/pull
```

对应端点 `POST /api/wiki/sync/pull` 的动作是 `git fetch --prune` + `git pull --ff-only`：

- 本地有未提交改动 → 409。
- 不是 Git 仓库 → 409。
- 不能 fast-forward（远端有 force-push 或本地领先）→ 409。
- 远端不可达 / git 命令失败 → 502。

成功响应里会带 `old_commit` / `new_commit` / `changed` 字段。

当前状态可以通过 `GET /api/wiki/sync/status` 查看（只读本地 Git 信息，不访问远端）。如果服务器 checkout 不在 `main` / `master` 分支，状态会带 warning。

Wiki 页面本身的格式规范（页面类型、frontmatter schema、命名前缀、lint 错误码）见 [wiki-conventions.md](../reference/wiki-schema.md)。

---

## 4. 推荐协作节奏

1. **摄入论文或资料**：按 Path A / B / C 之一把 raw 资产入库，保留证据链。
2. **整理 Wiki 页面**：在 `QuantumAtlas-Wiki` 仓库里 commit / PR / review，让分类、摘要、引用和状态可审阅。
3. **触发服务器同步**：调用 `POST /api/wiki/sync/pull`，再让稳定的 Wiki 页面同步到 Neo4j，用关系图做依赖发现和路径查询。Graph 始终是派生视图，不是另一份手工维护的 truth。
4. **下游使用**：从算法或原语继续生成实现，经过验证和资源估计后再进入下游。
