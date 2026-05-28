# Development

## Go server 开发环境（当前）

Server 是 Go binary（`cmd/qatlas-server/`），通过 pixi 管理 Go 工具链。前端 SPA
在 `web/`（React + Vite）。

```bash
# 一次性 setup（pixi env 装 go + gxx，进 .pixi/）
pixi install

# 常用任务（pyproject.toml [tool.pixi.tasks]）
pixi run build        # 构建 server binary -> build/qatlas-server
pixi run vet          # go vet（窄到自己代码，秒级；首次冷 cache ~15s 填 .gocache/build）
pixi run test-go      # go test ./internal/... ./cmd/...
pixi run test-py      # uv run pytest -m 'not e2e and not legacy and not network'

# 前端
cd web && npm install
cd web && npm run build         # 出 dist/，复制到 internal/webui/dist/
cd web && npm run dev           # vite dev server (端口 5173)

# Python CLI（不变）
uv sync --extra dev
uv run pytest -m "not e2e and not network and not legacy"
```

> **不要在 pixi env 外手动 `go build`**：你机器上全局 go 是 pixi-managed
> conda-forge build，自带 `CC=x86_64-conda-linux-gnu-cc` baked-in。pixi env
> 里有匹配的 `gxx`，CGO 调用能成功；env 外没有，`go vet` / 任何走 cgo 的
> path 都会卡在 cc not-found 的 futex 死锁上。如果一定要在 env 外编，
> `CGO_ENABLED=0 go build ./cmd/qatlas-server` 也行（本项目所有依赖都是 pure-Go）。

### Known Go server gotchas

这些坑都已经在 `cmd/qatlas-server/main.go` 修了，但解释一下方便后人理解：

1. **PocketBase 默认把 `pb_data/` 写在 binary 同目录的 `./pb_data`**。我们
   binary 装在 `~/.local/bin/`，CWD 一旦不对（systemd unit 的
   `WorkingDirectory` 通常是 git checkout），就会在 checkout 里默写
   `pb_data/`。修法：`cmd/qatlas-server/main.go::injectPBDataDirFlag` 启动
   时检查 os.Args，没带 `--dir=` 就自动补 `--dir=$QATLAS_PB_DATA_DIR`。
   后者默认 `$XDG_DATA_HOME/quantum-atlas/pb_data`（即
   `$HOME/.local/share/quantum-atlas/pb_data`）。**生产路径统一通过
   `QATLAS_PB_DATA_DIR` env 控制**，不要在 systemd `ExecStart` 里再
   硬写 `--dir=...`——cmdline 优先级最高，会让 .env 里的同字段失效，
   排障时容易踩。手动跑 binary（superuser / 迁移）同理，靠 env 即可。

2. **PocketBase 默认 `net.Listen("tcp", "0.0.0.0:NNNN")` 在 modern Go 上
   返回 dual-stack v6 socket**（在 `/proc/<pid>/net/tcp6` 可见，`tcp`
   里没有）。普通 Linux 上 v4 客户端走 IPv4-mapped IPv6 还是能进来，
   所以默认 fine。**但 WSL2 + Windows netsh portproxy** 的 v4-only
   转发规则会把 v4 SYN 投到 v6 socket 时 WSL2 NAT 层拒掉 — edge Caddy
   反代过来全 502。修法：`QATLAS_FORCE_TCP4=1` 触发
   `cmd/qatlas-server/main.go::maybeIPv4Listener` 显式 `net.Listen("tcp4", ...)`。
   **默认 off**，社区部署不需要打开；只在 WSL2 portproxy 场景设
   `Environment=QATLAS_FORCE_TCP4=1` 到 systemd unit。

3. **`.env` 通过 godotenv 加载，路径由 `QATLAS_DOTENV` env 决定**（systemd
   unit 里 `Environment=QATLAS_DOTENV=<APP_HOME>/.env`，例如
   `%h/QuantumAtlas/.env` for user units）。相对路径如
   `WIKI_DIR=../QuantumAtlas-Wiki` 解析 anchor 是 `.env` 所在目录。
   **不**用 systemd `EnvironmentFile=` 因为后者只 inject env vars，
   server 拿不到文件路径就没法做 anchor。

4. **`go vet ./...` 在本地工作树里会卡死**——不是 Go 本身的问题，是
   `raw/` 目录是 rclone FUSE 挂载（指向云端 Team 网盘）。`go list/vet
   ./...` 默认会遍历所有子目录 `stat .go` 文件，触发 FUSE→网络拉云端
   listing，10+ 分钟都不一定回。**永远走 `pixi run vet`**（task 已经
   窄到 `./internal/... ./cmd/...`，不碰 raw/）。`go build ./cmd/qatlas-server`
   单包指定也安全。底线：本地任何 Go 命令都**别用 `./...` glob**。

5. **Go 1.26.3 vs pixi env**：`go.mod` 写 `go 1.26.2`，pixi 装到 1.26.3。
   升 go.mod 前先 `pixi search -c conda-forge go` 确认 conda-forge 有匹配
   版本；conda go 包默认 `GOTOOLCHAIN=local`（在 `.pixi/envs/default/etc/
   conda/env_vars.d/go.json`）禁止自动下载新 toolchain，go.mod 要求更高
   就会卡死编不出来。

### 部署到后端服务器

```bash
pixi run build
scp build/qatlas-server <TARGET>:/tmp/qatlas-server-go
ssh -t <TARGET> 'sudo bash /tmp/qa-go-update-system.sh'
```

binary 装到 `~/.local/bin/qatlas-server`（无 sudo 写入），systemd unit 视部
署方式可以是 user 单元（`~/.config/systemd/user/qatlas.service`，需要
`loginctl enable-linger`）或 system 单元（`/etc/systemd/system/qatlas.service`，
`User=<svcuser>`，restart 要 sudo）。日常运维（system unit 示例）：

```bash
systemctl status qatlas         # 读，免 sudo
journalctl -u qatlas -f         # 读，免 sudo
sudo systemctl restart qatlas   # 写，要 sudo
```

具体单元模板与权限分工见 [deployment.md §systemd](deployment.md)。

## 加新 PAT scope（P14 之后）

PAT scope 系统数据驱动——加一条新 scope 走一个 5–6 文件的小回路，**不需要碰任何 handler 的 import**。背景见 `docs/contribution-workflow.md` §2 的用户视角文档，本节是开发者的 step-by-step。

### 词表设计

scope 命名遵循 `<resource>:<action>` 约定：

- `resource` = 资源类名复数（`papers` / `shares` / `wiki`...），跟 URL 路径段对齐
- `action` = 动词原形（`read` / `write` / 将来可能的 `admin` / `delete`），跟 HTTP method 大致对齐
- **`write` 默认 imply `read`**（在 `scopePolicies` 表里加两行即可，下面有示例）

不要随意加 `manage` / `full` 这种泛化动词；总是优先拆出更细粒度的 read/write。`*` 通配符**只用作内部 session-token 的隐式 scope**，外部输入由 `ValidateScopes` 拒绝。

### 加一条新 scope 的清单

以"加一个 `wiki:write` scope 允许 PAT 持有者调将来的 `POST /api/wiki/sync/pull`"为例：

**1. `internal/pat/scopes.go`** —— 加常量 + 描述 + 词表项 + 策略行：

```go
const (
    // ...existing scopes...
    ScopeWikiWrite = "wiki:write"
)

var ScopeDescription = map[string]string{
    // ...
    ScopeWikiWrite: "Trigger wiki sync (git pull) from the SPA or CLI",
}

var AllScopes = []string{
    // ...existing order...
    ScopeWikiWrite,
}

var scopePolicies = [][3]string{
    // ...existing...
    {ScopeWikiWrite, "wiki", "write"},
    // 若 wiki:write 应隐式包含将来加的 wiki:read，再加一行:
    // {ScopeWikiWrite, "wiki", "read"},
}
```

**2. `internal/pat/scopes_test.go`** —— 给 `TestNewEnforcer` / `TestAllows` 各加几行（直接抄已有的 `papers:write` 模式），确认拿到新 scope 时 `(wiki, write)` 通过，不拿时拒绝：

```go
{ScopeWikiWrite, "wiki", "write", true},
{ScopeWikiWrite, "papers", "write", false}, // 不串扰别的 resource
```

`TestScopeDescription_Coverage` 是循环检测的——只要 1 行 desc map 加好就自动覆盖到。

**3. `internal/routes/<对应 file>.go`** —— 把新 endpoint 用 `scopeGuard(enforcer, "wiki", "write", ...)` 包起来。注意三个参数：

- `enforcer`：从 `Register<Module>` 函数签名拿（如果模块还没接 enforcer，去 `cmd/qatlas-server/main.go::registerRoutes` 加一个传参，参考 `RegisterPapers` / `RegisterShares` 的写法）
- `obj` / `act`：必须跟 `scopePolicies` 表里的 obj/act **严格一致**（字符串匹配，无歧义），不要写成 scope 名 `wiki:write`——那是用户面的 label，不是 enforcer 的 (obj, act) 元组

**4. 重启 server 验证**（**不需要写 migration**——scope 字段是 JSON 字符串，存什么值由 `ValidateScopes` 在入口拦截，加新 scope 只是放宽 validator）：

```bash
pixi run build && pixi run test-go && pixi run vet
# 重启后浏览器 /pat 页面应该看到新 scope 出现在 checkbox 列表（因为 GET /api/pat/scopes 自动暴露 AllScopes）
```

**5. e2e**（可选，建议）—— 抄 `tests/integration/test_production_smoke.py::test_pat_scope_enforcement` 加一个 case：mint 只带新 scope 的 PAT → 调 `POST /api/wiki/sync/pull` → 期望 200/4xx（不是 403）；mint 不带 → 期望 403 且 detail 含 `wiki:write`。

### 不需要做的事

- **不用改 SPA**：`web/src/routes/pat.tsx` 通过 `GET /api/pat/scopes` 拉词表，新 scope 自动出现在 checkbox 里。前端**没有任何 hardcoded scope 字符串**。
- **不用改用户文档**（除非 scope 语义复杂到需要专门解释）：scope 的一行描述就是 `ScopeDescription` map 那一行，会同时显示在 UI 和 `/api/pat/scopes` JSON 里。
- **不用迁移**：`pat_tokens.scopes` 是开放 schema 的 JSON 字符串，validator 在 input 端把关，老数据自然继续有效。

### 进阶：path-pattern scope（将来）

当前 matcher 是字符串相等：

```
[matchers]
m = r.scope == p.scope && r.obj == p.obj && r.act == p.act
```

未来想加"`papers:quant-ph:write` 只允许写 `/api/papers/quant-ph/*`"这种细粒度，把 matcher 换成 casbin 内建的 `keyMatch`：

```
m = r.scope == p.scope && keyMatch(r.obj, p.obj) && r.act == p.act
```

policies 写成 `{ScopePapersQuantPhWrite, "papers/quant-ph/*", "write"}`，端点调 `scopeGuard(enforcer, "papers/" + arxivNamespace, "write", ...)`。`Allows` helper、`ValidateScopes`、SPA 词表展示**全都不用改**——keyMatch 是 casbin 自带函数，模型字符串改一行就够。

### 关于 sessionGuard

`scopeGuard` 是给"PAT 也能调"的写口用的。如果新加的端点是 PAT 不应该能调的（如各种 admin 操作），用 `sessionGuard` 替代——它只接受浏览器 session token，PAT-auth 一律 403。`/api/pat` 系列就是这么处理的（防 leaked PAT 自我复制）。判断标准：**这件事一个长寿命的 token 该不该有权做**。能批量上传论文？是，PAT OK，scopeGuard。能管理别的 PAT、改用户权限、删 collection？不是，sessionGuard。

### PAT 防滥用 — rate-limit / 日志 / 并发

- **rate-limit**（`internal/pat/ratelimits.go::EnsureDefaults`，OnBootstrap 注入）：
  `POST /api/pat` audience=@auth ≤ 10 req/min（防 leaked JWT 批量 mint）；
  audience=@guest ≤ 30 req/min（封顶匿名 hammer，反正会 401，但 log 干净）。
  规则**幂等**——admin 在 UI 里手动调紧（如改成 5/min）我们尊重；admin 删除则下次重启被补回。其他端点（`/api/shares/`、`/api/papers/*`）未设 rate-limit——PAT 143 bit 熵下 brute-force 不可行，给合法 batch caller 留余地。需要追加时改 `DefaultRateLimitRules` 这个 slice。
- **Authorization 头不进日志**：PocketBase activity logger 只记 method/url/status/referer/userAgent/auth-collection/IP，不打 headers / body。grep `apis/middlewares.go::logRequest` 确认。
- **last_used_at race**（`internal/pat/pat.go::MarkUsed`）：单字段 UPDATE 而不是 `app.Save(rec)`，避免并发用同一 PAT 时 `updated` 时间戳 jitter 或 OnRecordUpdate hook 多次触发。回归测 `internal/routes/pat_markused_test.go::TestPATMarkUsed_ConcurrentCallsDoNotRace`（20 个 goroutine 并发，无错）。

## API 概览（Go server，对外 HTTP 端点）

实际由 `internal/routes/*.go` 与 `cmd/qatlas-server/main.go` 注册。鉴权列规则：

- **open** = 未鉴权也能读（公开 wiki / 公共信息）
- **session** = 浏览器 PocketBase session token（OAuth 登录后从 `/token` 拿）
- **scope** = `session` 或带相应 scope 的 PAT（`qat_` 前缀，从 `/pat` 创建）。
  scope 在 `internal/pat/scopes.go` 维护，详见 [contribution-workflow.md](contribution-workflow.md) §2。

> `*` 在 scope 列表示 session token 走通配；列里写 `papers:write` 等具体
> scope 时，PAT 必须显式 opt-in 该 scope 才能调（fine-grained 模型）。

### 元 / 公开

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/health` | open | 健康检查 |
| GET | `/api/server/info` | open | server 版本、build commit、配置摘要 |
| GET | `/{path...}` | open | 前端 SPA（fallback 到 `index.html`） |

### Wiki

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/pages` | open | 页面列表（支持 type / tag 过滤） |
| GET | `/api/pages/{page_id}` | open | 单页详情（含 frontmatter + render markdown） |
| GET | `/api/stats` | open | wiki 统计（页面数、tag 分布等） |
| GET | `/api/search?q=...` | open | 全文搜索 |
| GET | `/api/lint` | open | wiki lint 报告（dry-run） |
| GET | `/api/wiki/sync/status` | open | 本地 wiki checkout 的 branch / commit / dirty |
| POST | `/api/wiki/sync/pull` | open | `git fetch --prune` + `git pull --ff-only` |

### 图谱

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/graph/stats` | open | Neo4j 节点 / 边统计 |
| GET | `/api/graph/schema` | open | label / relationship type 列表 |
| POST | `/api/graph/query` | open | 受限 Cypher 查询（read-only） |

### 论文资产（统一走 `/api/papers/{path...}` 通配子路由）

`path` 末段决定子操作；列表只标核心的：

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/papers/{arxiv_id}/resources` | open | 列出已有资产（pdf / markdown / metadata） |
| POST | `/api/papers/{arxiv_id}/upload-pdf` | `papers:write` | 上传 PDF（multipart） |
| POST | `/api/papers/{arxiv_id}/upload-markdown` | `papers:write` | 上传 markdown（multipart，可标 source=mineru/manual） |
| POST | `/api/papers/{arxiv_id}/mineru-claim` | `papers:write` | 占位 MinerU 任务（防多人并发跑同一篇） |
| DELETE | `/api/papers/{arxiv_id}/mineru-claim/{claim_id}` | `papers:write` | 释放 claim |

> 摄入流程 = `qatlas ingest`（CLI，从 arXiv 抓 PDF / metadata 写本地）+
> 上面这两个 upload 端点（推 server）。Go server 不再保留 FastAPI 时代的
> `/api/ingest/*` 多阶段 task 状态，旧端点仅在 `atlas/server/` FastAPI
> mock 里能跑，见 [python-legacy.md](python-legacy.md)。

### Share 链接

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/shares/` | `shares:write` | 颁发 share token（含 TTL，默认走 `QATLAS_DEFAULT_SHARE_EXPIRES_IN`） |
| GET | `/api/shares/` | `shares:read` | 列出当前用户的 share token |
| DELETE | `/api/shares/{token}` | `shares:write` | 撤销 share token |
| GET | `/share/{token}` | open | share 入口（302 到资产或渲染 landing） |
| GET | `/share/{token}/{path...}` | open | share 内子资源 |

### PAT 管理

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/pat` | **session-only** | 创建 PAT（指定 name / scopes / expires_at） |
| GET | `/api/pat` | **session-only** | 列出当前用户的 PAT（不返回明文 token） |
| DELETE | `/api/pat/{id}` | **session-only** | 删除 PAT |
| GET | `/api/pat/scopes` | open | 暴露当前 server 支持的 scope 词表 + 描述 |

> `/api/pat` 写口故意**只接受 session token**（拒 PAT 自创建 PAT），避免泄
> 露的 PAT 自我复制。同款限制参考 GitHub fine-grained PAT。

### PocketBase 内置端点

`/api/collections/*`、`/api/admins/*`、`/_/`（admin UI）、`/api/collections/users/auth-with-oauth2`、`/api/oauth2-redirect` 等都来自 PocketBase 框架本身，不在 `internal/routes/` 里手写。OAuth provider 配置在 server 启动时由 `internal/auth/oauth.go::syncGitHubProvider` 用 `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` env 写到 users collection。

## 仓库结构

```text
QuantumAtlas/
├── cmd/qatlas-server/      Go binary entrypoint（生产 server）
├── internal/              Go server 模块（routes/auth/wiki/neo4j/...）
├── web/                   React + Vite SPA 源码
├── atlas/                 Python client / 旧 FastAPI server
├── examples/              独立 demo
├── scripts/               初始化与维护脚本
├── tests/                 Python 测试套件
├── docs/                  补充文档
├── pyproject.toml         Python + pixi 配置
└── go.mod                 Go module 配置
```

`atlas/` 里的核心模块包括：

- `parser`: 从 arXiv 获取论文并解析 PDF。
- `extractor`: 调用 LLM 抽取算法结构。
- `wiki`: 页面 CRUD、搜索、lint、Neo4j 同步。
- `knowledge` / `knowledge_graph`: 图谱模型与 Neo4j 交互。
- `designer`: 从算法定义生成 Quantum IR。
- `codegen`: 生成 Qiskit 或 QPanda 代码。
- `validator`: 电路验证与参考实现对比。
- `estimator`: 资源估计。
- `server`: 旧 FastAPI server，已不是生产路径，仅作兼容性参考
  （见 [python-legacy.md](python-legacy.md)）。

## CI

仓库里有**两条独立的 GitHub Actions 流水线**，分工很明确：

### 1. `Pytest`（每个 PR / push 都跑）

文件：`.github/workflows/pytest.yml`

```bash
uv run pytest -rs -m "not network and not e2e"
```

- 矩阵：Python 3.11 / 3.12
- 触发：push 到 `main` 分支、对 `main` 提 PR、`workflow_dispatch` 手动触发
- 范围：**只跑离线测试**——不发任何外网请求，不依赖任何 secret，不连任何远端服务
- 目的：保护代码本身的正确性，确保 PR 合入前不破坏既有逻辑

Go 侧的 `go test ./...` 走 `.github/workflows/go.yml`，同样在每个 PR / push 跑——包含 `internal/routes/pat_test.go` 的 PAT HTTP 契约测试，因此 sessionGuard 拒 PAT、强制 expiry、scope 403 等关键契约在每次 push 都验证一次。

### 2. `Nightly production smoke`（每天定时 + 手动）

文件：`.github/workflows/nightly.yml`

```bash
uv run pytest -rs -m "e2e and not legacy"
```

- 触发：`cron: '0 18 * * *'`（UTC 18:00 = 北京时间次日 02:00），也可在 Actions 页面手动 `workflow_dispatch`
- 范围：跑所有标了 `e2e` 但不标 `legacy` 的测试（`legacy` = 老 FastAPI ingest 测试，Go server 不实现）
- 目的：**验证线上服务的稳定性**，把"代码是好的"扩展到"线上跑出来也是对的"

关键测试 `tests/integration/test_production_smoke.py`：直接打 `QATLAS_SERVER_TARGETS` 列出的每个公网入口，覆盖 `/health`、`/api/server/info`（Go engine 标记）、公开 read 端点、SPA 静态、auth gate（无凭证 401 / bogus bearer 401 / 合法凭证 400 paths-required）。

#### 需要在 repo Secrets 里配置：

| Secret | 用途 | 不设置会怎样 |
|---|---|---|
| `QATLAS_SERVER_TARGETS` | 线上服务公网入口列表，**逗号或换行分隔**。每条可写成 `URL` 或 `URL\|insecure`（后者跳过 TLS 校验，给自签证书 / 直连 IP 用）。示例：`https://quantum-atlas.ai`<br>`https://47.102.36.175\|insecure` | workflow 用内置默认（两条 production 入口）跑；如要打 staging 才需要这个 secret |
| `QATLAS_CI_TOKEN` | 长效写凭证，给 `test_write_endpoint_accepts_user_token` 验证 auth gate 的 accept path。**推荐 PAT 形式**（`qat_*`，可活 365 天）；JWT 也接受但 14 天就要换。PAT 必须带 `shares:write` scope。生成方法见下节 | 单个 token-required 测试 self-skip，其他正常跑（job 还是绿） |
| `MINERU_API_TOKEN` | MinerU API token，被部分用例兜底使用（如本地启 server 测 mineru 路径） | 仅本地启 server 走 mineru 的 e2e 用例会 `skip` |

> 配多个 target 时，每个测试都会按 target 各跑一次（pytest 参数化），id 就是 URL 本身。

加 secret 的方法：repo Settings → Secrets and variables → Actions → New repository secret；或 `gh secret set <NAME> --repo <owner>/<repo>`（**stdin 读时必须完全省略 `--body`** —— 写成 `--body -` 会把 secret 设成字面 `"-"`，过去踩过这个坑）。

#### 生成 `QATLAS_CI_TOKEN` 的两条路径

**路径 A：服务端 CLI（推荐）**——无需浏览器、无需 OAuth，直接在 server 主机上跑：

```bash
ssh 1810
sudo -u qatlas-server /opt/quantum-atlas/qatlas-server pat mint \
    --user <你的 GitHub 关联邮箱> \
    --name nightly-ci \
    --scopes shares:write \
    --expires-in-days 365
# stdout 输出一行 qat_...；stderr 是 id / prefix / expiry 摘要
```

可用 `qatlas-server pat list` / `qatlas-server pat revoke <id>` 管理，`qatlas-server pat scopes` 查看完整 scope 词表。

**路径 B：SPA 界面**——浏览器登录后访问 `https://<host>/pat`，点 "New token"，name=`nightly-ci`，勾 `shares:write`，expiry=365 天，复制弹出的 `qat_...` plaintext（**只能看一次**）。

拿到 plaintext 后：

```bash
echo "qat_xxxxxxxxxxxxxxxxxxxxxxxxxxx" | gh secret set QATLAS_CI_TOKEN --repo IAI-USTC-Quantum/QuantumAtlas
gh workflow run "Nightly production smoke" --repo IAI-USTC-Quantum/QuantumAtlas
```

#### 为什么 PAT 管理契约（sessionGuard 拒 PAT / 强制 expiry / scope 403 / 完整生命周期）不在 nightly 里？

那些契约的第一步都要 `POST /api/pat`，而 `/api/pat` 被 `sessionGuard` 守着——**只接受 session JWT，不接受 PAT**。这是 GitHub fine-grained PAT 的设计原则（leaked PAT 不能自我复制）。所以如果 nightly 要跑这些契约，CI 必须配 JWT；而 JWT 14 天就过期，等于每两周要人工换 secret。

折中：契约本身搬到 `internal/routes/pat_test.go`（用 PocketBase `tests.NewTestApp()` harness 离线跑），随 `go test` 在每个 PR / push 验证；nightly 只测"线上服务可达 + auth gate 还在工作"，用 365 天 PAT。

> **为什么 nightly 跑得起来 production 烟测、本地却不太需要打开**
> 线上服务有公网入口、跑着完整的 `.env`；本地开发盒子通常没必要每次都拉真的论文。本地想跑一次：`QATLAS_SERVER_TARGETS=https://atlas.example.com uv run pytest -m e2e tests/integration/test_production_smoke.py`。

## 版本与发布

版本号只由 Commitizen 在发版时维护，不要手改 `pyproject.toml` 里的 `version`。

推荐流程：

1. 日常提交使用 Conventional Commits，例如 `feat:`、`fix:`、`docs:`。
2. 通过 Commitizen 统一更新版本号和 changelog。
3. 用 `v$version` 形式打 tag，并由 GitHub Actions 构建 release。

本地如需预演版本变化：

```bash
uv run --with commitizen cz bump --dry-run
```

如果生产环境要求代码版本必须对齐 release tag，可以设置：

```env
QATLAS_REQUIRE_RELEASE_TAG=true
```

> alias: `QUANTUMATLAS_REQUIRE_RELEASE_TAG` / `REQUIRE_RELEASE_TAG` 仍生效。

## 协作时的注意点

- Wiki 内容和应用代码可以分开演进，不必同频发版。
- 分享链接只用于公开资源访问；登录用户身份由反向代理、SSO 或 API gateway 注入。
- 远程协作者优先通过 API 和 Wiki Git 仓库协作，而不是直接依赖服务器文件系统权限。
