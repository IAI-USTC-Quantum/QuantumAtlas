# qatlasd 服务端环境变量

本文档描述 **`qatlasd` server**（Go binary）当前如何加载 / 解析 / 使用环境变量。

> **Client / Server 配置完全分离**：
>
> - **Server** (`qatlasd`): 三入口 **CLI flag > OS env > `.env` 文件 > default**（本页主题）
> - **Client** (`qatlas` Python CLI): **只读 YAML** `~/.config/qatlas/config.yaml`，首次跑任意 `qatlas <cmd>` 自动创建模板（不再支持 CLI flag / OS env / `QATLAS_DOTENV`，自 v0.17.0 起）
>
> 设计哲学：server 是 long-lived daemon，运维要同时支持 systemd / docker / k8s / nohup 多形态，所以三入口；client 是 short-lived 命令，用户配置一次长期复用，YAML 单入口最简单。client 配置参考见 [`qatlas config` reference](../reference/cli-qatlas.md#qatlas-config)。

---

## 1. 加载机制

`qatlasd` 用 [`joho/godotenv`](https://github.com/joho/godotenv) 在启动早期把 `.env` 文件内容灌进 `os.Environ`，之后所有字段读取统一走 `os.Getenv`。代码入口：

```
cmd/qatlasd/main.go::loadDotEnv()          # 找并 load .env
internal/config/config.go::Load(dotenv)    # 从 os.Environ 读出 Config 结构体
```

### 1.1 .env 文件查找顺序

`loadDotEnv` 按以下顺序检查文件是否存在；**第一个找到的文件被 load**，后续不再尝试：

| # | 来源 | 何时使用 |
|---|---|---|
| 1 | `$QATLAS_DOTENV` 环境变量指向的路径 | systemd unit 通过 `Environment=QATLAS_DOTENV=<path>` 注入；docker 容器 mount config 时 |
| 2 | `./.env`（启动时 `os.Getwd()`） | 手动 / nohup 直接跑、dev 机器、`pixi run server` |
| — | 都没找到 | 完全靠进程已有 env（systemd `Environment=KEY=val` 单条注入、docker `-e`、shell `export`） |

### 1.2 godotenv 覆盖语义

`godotenv.Load()` 的 **non-override** 模式：**进程里已经存在的 env var 不被 .env 文件覆盖**。所以效果上的优先级是：

1. **已存在的 OS env var**（systemd `Environment=` 单条注入、docker `-e`、shell `export`）
2. **.env 文件里的值**（通过 1.1 找到的那一个）
3. **代码里的 default**（见 §2 各字段「默认」列）

> ⚠️ **non-override 的调试陷阱**：在 shell 里 `export NEO4J_URI=bolt://test` 跑过一次后，**之后改 `.env` 里的 `NEO4J_URI=` 不会生效**——shell 残留的值赢。如果改 .env 后行为没变，先 `unset` 该 env 再启动，或开新 shell。systemd 不受影响（每次启动是干净 env）。
>
> 我们故意选 non-override 而不是 override，理由是 CI / 容器场景里 `docker -e KEY=val` 应该胜过 image 里 baked-in 的 `.env`——这是 dotenv 生态的事实标准（python-dotenv / Node dotenv / Ruby dotenv 默认全是 non-override）。

### 1.3 .env 路径作为相对路径锚点

`Config.WikiDir` 等"路径型"字段如果填的是**相对路径**（例：`WIKI_DIR=../QuantumAtlas-Wiki`），**相对 .env 文件所在的目录解析**，不是相对 CWD 或 systemd `WorkingDirectory`。

```go
// internal/config/config.go::Load
anchor := ""
if dotenvPath != "" {
    anchor = filepath.Dir(dotenvPath)
}
cfg.WikiDir = expandPath(defaultIfEmpty(cfg.WikiDir, defaultWikiDir()), anchor)
```

如果通过 OS env var 而非 .env 注入，且值是相对路径，**没有锚点** —— 行为是相对 CWD。**推荐总是用绝对路径**避免歧义。

### 1.4 字段读取风格

`internal/config/config.go` 用 3 个 helper 读 env，**不引入 viper / envconfig 等第三方 config 库**：

```go
firstEnv("QATLAS_SERVER_URL", "PUBLIC_BASE_URL")    // 返回第一个非空，否则 ""
firstEnvDefault("4200", "QATLAS_SERVER_PORT", ...)  // 同上但带 fallback
firstEnvIntDefault(0, "SOME_INT_VAR")               // 同上但 int
```

支持**多 alias**（老名字保留为后备，新代码用第一个）。

---

## 2. 完整 env 列举

按域分组。所有项目自有变量带 `QATLAS_` 前缀；第三方 SDK 标准名（`NEO4J_*` / `GITHUB_*`）保留原始命名。

### 2.1 进程绑定 / 标识

| Env | 默认 | 用途 |
|---|---|---|
| `QATLAS_HTTP_ADDR` | — | 直接给 `host:port`（若设，覆盖下面 `_HOST` / `_PORT`） |
| `QATLAS_SERVER_HOST` (alias `SERVER_HOST`) | `127.0.0.1` | bind host |
| `QATLAS_SERVER_PORT` (alias `SERVER_PORT`) | `4200` | bind port |
| `QATLAS_FORCE_TCP4` | — | `1` / `true` / `yes` 强制 IPv4 socket（绕过 dual-stack 行为） |
| `QATLAS_EDGE_NAME` | — | 折进 S3 client User-Agent，用于 S3 事件审计区分多 edge 写入方 |
| `QATLAS_USER_HEADER` (alias `USER_HEADER`) | — | 反代注入的审计 header 名（caddy-security 时代遗留，可与 PocketBase auth 并行） |

### 2.2 文件系统路径

| Env | 默认 | 用途 |
|---|---|---|
| `QATLAS_DOTENV` | — | **显式 .env 文件路径**；查找顺序见 §1.1 |
| `QATLAS_WIKI_DIR` (alias `WIKI_DIR`) | `<.env 目录>/../QuantumAtlas-Wiki` | wiki repo checkout（git pull / lint / search 都在这） |
| `QATLAS_RAW_DIR` (alias `RAW_DIR`) | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw` | LocalStore 文件后端目录；S3 backend 启用时不读 |
| `QATLAS_DATA_DIR` (alias `DATA_DIR`) | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data` | 业务派生数据（claim leases 等） |
| `QATLAS_PB_DATA_DIR` (alias `PB_DATA_DIR`) | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data` | PocketBase SQLite + collections + uploads。通过 `--dir=` 自动注入 PocketBase cobra 根命令 |
| `XDG_DATA_HOME` | `~/.local/share` | 上面 3 个 dir 默认值的 base |

### 2.3 公开 URL

| Env | 默认 | 用途 |
|---|---|---|
| `QATLAS_SERVER_URL` (alias `PUBLIC_BASE_URL`) | — | server 自报的对外 URL；当前主要用于生成 share URL（v0.9.0 share 端点删除后已基本无用，保留作为客户端反查参考） |

### 2.4 S3 / RustFS 后端（**all-or-nothing 6 字段**）

下表前 6 个字段：要么**全部填**走 `S3Store`，要么**全部空**走 `LocalStore(RawDir)` fallback。**半填启动 fatal**，错误消息列出哪几个 set 哪几个 unset。第 7 个（OpenAlex bucket）可选。

| Env | 默认 | 用途 |
|---|---|---|
| `QATLAS_S3_ENDPOINT` | — | S3 兼容 endpoint（必须含 scheme `http://` / `https://`） |
| `QATLAS_S3_PUBLIC_ENDPOINT` | — | 给客户端签 presigned URL 用的"公网入口"；空 ⇒ 用 internal endpoint 签（dev 模式） |
| `QATLAS_S3_BUCKET_PDF` | — | per-kind bucket (`pdf/<yymm>/<id>v<n>.pdf`) |
| `QATLAS_S3_BUCKET_MD` | — | per-kind bucket (`md/<yymm>/<id>v<n>.md`) |
| `QATLAS_S3_BUCKET_IMAGES` | — | per-kind bucket (`images/<yymm>/<id>v<n>/<filename>`) |
| `QATLAS_S3_ACCESS_KEY_ID` | — | svcacct access key（**绝不用 root key**） |
| `QATLAS_S3_SECRET_ACCESS_KEY` | — | svcacct secret |
| `QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT` | — | 可选第 7 字段；缺失时 server 仍 serve，只是 `openalex` 子命令拒跑 |

**已被 fail-fast 拒绝的旧字段**（设了就启动失败）：

| Env | 状态 |
|---|---|
| `QATLAS_S3_BUCKET` | v0.6.0 单桶时代名；`validateS3Config` 见到这个 env 立刻 fail-fast，错误文案指引迁移到 per-kind 三字段 |

### 2.5 Neo4j 图数据库

| Env | 默认 | 用途 |
|---|---|---|
| `NEO4J_URI` | — | Bolt URL（例：`bolt://10.144.18.10:7687`）。未设时 catalog 功能 disabled，相关 API endpoint 降级为 `{available:false}` |
| `NEO4J_USERNAME` (alias `NEO4J_USER`) | — | 用户名 |
| `NEO4J_PASSWORD` | — | 密码 |
| `NEO4J_DATABASE` | — | DB 名（多 DB 部署用） |

### 2.6 鉴权 / 白名单

| Env | 默认 | 用途 |
|---|---|---|
| `GITHUB_CLIENT_ID` | — | OAuth2 provider 启动时注入 users collection 的 OAuth2 providers |
| `GITHUB_CLIENT_SECRET` | — | OAuth2 provider 启动时注入 |
| `QATLAS_ALLOWED_GITHUB_LOGINS` | — | CSV，登录白名单（大小写不敏感）。`OnRecordAuthWithOAuth2Request` 钩子强制校验。**fail-closed**：此项 ∪ `_ADMIN_` 同时为空 ⇒ 谁都登不了 |
| `QATLAS_ADMIN_GITHUB_LOGINS` | — | CSV，admin 提权白名单；隐式视为允许登录（并进 ALLOWED 白名单） |
| `QATLAS_SYSTEM_PAT` | — | 系统 PAT 明文（CI / ops 脚本用，bypass 浏览器 OAuth）。`internal/pat/system_pat.go` 启动时读 |
| `QATLAS_SYSTEM_PAT_SCOPES` | — | CSV scope 列；不设时默认全 scope |

### 2.7 已弃用（设了**没有效果**，纯 noop）

| 旧名 | 状态 |
|---|---|
| `QATLAS_WRITE_TOKEN` | 已删；Phase-A 临时共享密钥，被 PocketBase auth 替代 |
| `QATLAS_SESSION_SECRET` | 已删 |
| `QATLAS_POCKETBASE_URL` | 已删；server 自带 PocketBase |
| `QATLAS_REQUIRE_RELEASE_TAG` | 已删；旧 FastAPI 的 release-tag 启动护栏 |
| `CLI_TOKEN_*` | 已删；更早的 token 字段族 |
| `QATLAS_SERVER_DEBUG` | 从未被读过的幽灵字段；v0.16.0 从 `.env.example` 清理 |
| 无 `QATLAS_` 前缀的 alias（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `PUBLIC_BASE_URL` / `USER_HEADER`） | **v0.17.0 移除**——v0.16.0 起 `Load()` 按字段 emit `slog.Warn`（journald 可见），给运维一个 minor 的迁移窗口 |

> `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` 仍然有效，但只用于 client 侧 `qatlas extractor` 实验性子命令；qatlasd server 从未读过它们，所以不在 server 启动 env 范围里。

### 2.8 pb_data 多进程防护（v0.17.0+）

PocketBase + SQLite 的多写者语义是"WAL 允许多 reader / 单 writer"——两个 qatlasd 进程指同一份 `pb_data` 时，**不会启动失败**但**几乎必然数据腐败**（schema migration 撞，`_collections` / `_externalAuths` 写撞）。

自 v0.17.0 起 server 启动时在 `<pb_data>/qatlasd.lock` 上取**OS 级 advisory flock**（`flock(2)`，库 `github.com/gofrs/flock`）。第二个 qatlasd 启动撞到锁 → fatal exit，错误信息含 pb_data 路径 + lock 路径 + 两条出路（换 pb_data 目录 / 紧急绕过）。

- 进程崩溃 / `kill -9` / OOM → **内核自动释放 lock**，下次重启正常（不像 pid 文件可能 stale）
- 操作员子命令（`qatlasd pat mint` / `users list` 等）**不**取这个 lock —— 它们走 SQLite 自己的短读事务，可以跟 running `serve` 共存
- 紧急绕过：`QATLAS_SKIP_PB_DATA_LOCK=1`（**仅** disaster recovery / 实验，**不要**用于生产）

要真跑两个 qatlasd，必须**两份不同的 `QATLAS_PB_DATA_DIR`**（其余 `QATLAS_S3_*` / `NEO4J_URI` 等都可以共享，多边缘 active-active 本来就是这套）。

### 2.9 CLI flag 接口（v0.17.0+，easytier 风格）

`qatlasd serve` 自带 20 个项目自有 flag（加上 PocketBase 继承的 `--http` / `--https` / `--origins` / `--dir` / `--encryptionEnv`），每个都标 `[env: QATLAS_FOO=]`：

```bash
qatlasd serve --help    # 看完整列表
```

```
--server-url string             [env: QATLAS_SERVER_URL=]
--user-header string            [env: QATLAS_USER_HEADER=]
--edge-name string              [env: QATLAS_EDGE_NAME=]
--force-tcp4                    [env: QATLAS_FORCE_TCP4=]
--wiki-dir string               [env: QATLAS_WIKI_DIR=]
--raw-dir string                [env: QATLAS_RAW_DIR=]
--data-dir string               [env: QATLAS_DATA_DIR=]
--pb-data-dir string            [env: QATLAS_PB_DATA_DIR=]
--system-pat string             [env: QATLAS_SYSTEM_PAT=]
--system-pat-scopes strings     [env: QATLAS_SYSTEM_PAT_SCOPES=]
--neo4j-uri string              [env: NEO4J_URI=]
--neo4j-username string         [env: NEO4J_USERNAME=]
--neo4j-password string         [env: NEO4J_PASSWORD=]
--neo4j-database string         [env: NEO4J_DATABASE=]
--s3-endpoint string            [env: QATLAS_S3_ENDPOINT=]
--s3-public-endpoint string     [env: QATLAS_S3_PUBLIC_ENDPOINT=]
--s3-bucket-pdf string          [env: QATLAS_S3_BUCKET_PDF=]
--s3-bucket-md string           [env: QATLAS_S3_BUCKET_MD=]
--s3-bucket-images string       [env: QATLAS_S3_BUCKET_IMAGES=]
--s3-bucket-openalex string     [env: QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT=]
--s3-access-key-id string       [env: QATLAS_S3_ACCESS_KEY_ID=]
--s3-secret-access-key string   [env: QATLAS_S3_SECRET_ACCESS_KEY=]
```

**优先级**：CLI flag > OS env > `.env` 文件 > 内置 default。跟 easytier / clap / viper 习惯一致。

**典型用法**：docker run 一行起，不用 `.env`：

```bash
docker run -it --rm \
    -p 4200:4200 \
    -v /srv/qatlas/pb_data:/data \
    ghcr.io/iai-ustc-quantum/qatlasd:v0.17.0 serve \
        --http 0.0.0.0:4200 \
        --pb-data-dir /data \
        --neo4j-uri bolt://neo4j.example:7687 \
        --neo4j-username neo4j \
        --neo4j-password ... \
        --s3-endpoint https://rustfs.example \
        --s3-bucket-pdf qatlas-pdf \
        --s3-bucket-md qatlas-md \
        --s3-bucket-images qatlas-images \
        --s3-access-key-id ... \
        --s3-secret-access-key ...
```

**已知限制 — OAuth credentials 必须用 env**：`GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` / `QATLAS_ALLOWED_GITHUB_LOGINS` / `QATLAS_ADMIN_GITHUB_LOGINS` **没有** CLI flag。原因：PocketBase 的 Bootstrap 阶段（mount OAuth provider 到 settings table）跑在 cobra parse argv **之前**，CLI flag 来不及。这四个仅通过 env（`.env` / `Environment=` / `docker -e`）配置生效。

---

## 3. 不同部署形态下的注入方式

`qatlasd` 是 long-lived daemon，部署形态影响 env 注入接口。下面列出当前生产 / dev 支持的所有形态。

### 3.1 systemd 单元（生产 RackNerd / Alibaba 现状）

systemd 部署有**两条等价路径**——选你团队习惯的，不要混用：

#### 3.1.a Inline `Environment=`（v0.17.0+ 推荐：单文件无外部依赖）

把每个字段直接写进 unit 文件的 `Environment=`，**不需要任何 .env 文件**：

```ini
[Service]
ExecStart=/home/timidly/.local/bin/qatlasd serve --http=127.0.0.1:4200
User=timidly
Group=timidly
WorkingDirectory=/home/timidly

Environment=QATLAS_PB_DATA_DIR=/home/timidly/.local/share/qatlasd/pb_data
Environment=QATLAS_WIKI_DIR=/home/timidly/QuantumAtlas-Wiki
Environment=NEO4J_URI=bolt://10.144.18.10:7687
Environment=NEO4J_USERNAME=neo4j
Environment=NEO4J_PASSWORD=...
Environment=QATLAS_S3_ENDPOINT=http://10.144.18.10:9000
Environment=QATLAS_S3_BUCKET_PDF=qatlas-pdf
Environment=QATLAS_S3_BUCKET_MD=qatlas-md
Environment=QATLAS_S3_BUCKET_IMAGES=qatlas-images
Environment=QATLAS_S3_ACCESS_KEY_ID=...
Environment=QATLAS_S3_SECRET_ACCESS_KEY=...
Environment=GITHUB_CLIENT_ID=...
Environment=GITHUB_CLIENT_SECRET=...
Environment=QATLAS_ALLOWED_GITHUB_LOGINS=alice,bob
Environment=QATLAS_ADMIN_GITHUB_LOGINS=alice
```

**优点**：unit 文件是 single source of truth；`systemctl cat qatlasd` 一眼看完；不依赖文件 mode / 路径。
**缺点**：unit 文件 644 给 root 可见，secret 在 `systemctl show qatlasd` 输出里——同主机上其它用户可读。生产敏感字段建议改 `LoadCredential=`（systemd 250+，可配 0600 文件）。

#### 3.1.b 外部 .env + `QATLAS_DOTENV=`（v0.16 之前默认；适合多 unit 共享配置）

```ini
[Service]
ExecStart=/home/timidly/.local/bin/qatlasd serve --http=127.0.0.1:4200
Environment=QATLAS_DOTENV=/home/timidly/QuantumAtlas/.env
User=timidly
Group=timidly
WorkingDirectory=/home/timidly/QuantumAtlas
```

也可以让 systemd 自己 load .env（不经 godotenv），用 `EnvironmentFile=`：

```ini
EnvironmentFile=/etc/qatlasd/qatlasd.env
```

三种方式区别：

| 方式 | 相对路径锚点（如 `WIKI_DIR=../foo`） | 修改后生效需要 |
|---|---|---|
| `Environment=` inline | **无锚点**（相对 CWD） | `systemctl daemon-reload && systemctl restart qatlasd` |
| `Environment=QATLAS_DOTENV=...` + godotenv | **.env 所在目录** 是锚点（§1.3） | 改 .env 后直接 `systemctl restart qatlasd` |
| `EnvironmentFile=...` + systemd | **无锚点**（相对 CWD） | 改文件后 `systemctl restart qatlasd` |

**生产**：推荐绝对路径，三种方式没差别。
**dev**：相对路径只在 `Environment=QATLAS_DOTENV=...` 模式可控（锚点稳定），其它两种推荐绝对路径避免歧义。

#### 3.1.c 混合：CLI flag 覆盖 unit env

`Environment=` / `.env` 里写一份 baseline，`ExecStart` 用 flag 覆盖个别字段——典型场景是同一份配置在两台 edge 跑、只 edge name / port 不同：

```ini
ExecStart=/home/timidly/.local/bin/qatlasd serve \
  --http=127.0.0.1:4200 \
  --edge-name=alibaba \
  --force-tcp4
Environment=QATLAS_DOTENV=/etc/qatlasd/shared.env
```

CLI flag > OS env > .env 文件，所以 `--edge-name=alibaba` 总是赢，无论 .env 里写没写 `QATLAS_EDGE_NAME=`。

### 3.2 直接跑 / nohup

```bash
# 跑在当前目录，自动捡 ./.env
cd /path/to/config && nohup ./qatlasd serve > qatlasd.log 2>&1 &

# 显式指 .env，不依赖 CWD
QATLAS_DOTENV=/etc/qatlasd/.env nohup ./qatlasd serve > qatlasd.log 2>&1 &

# 完全不要 .env，env vars 单条 export
export QATLAS_HTTP_ADDR=127.0.0.1:4200
export QATLAS_S3_ENDPOINT=https://s3.example.com
# ... 其它字段
nohup ./qatlasd serve > qatlasd.log 2>&1 &

# 一次性临时 override（覆盖 .env 里的字段）
QATLAS_SERVER_PORT=8080 ./qatlasd serve
```

### 3.3 Docker / OCI

```bash
# env 列表注入
docker run \
  -e QATLAS_HTTP_ADDR=0.0.0.0:4200 \
  -e QATLAS_S3_ENDPOINT=... \
  -e NEO4J_URI=... \
  ghcr.io/<org>/qatlasd:v0.x.y serve

# env file 注入（docker --env-file 跟 godotenv 语法兼容）
docker run --env-file ./prod.env ghcr.io/<org>/qatlasd:v0.x.y serve

# Mount .env 让 godotenv 自己读
docker run \
  -v $PWD/prod.env:/etc/qatlasd/.env:ro \
  -e QATLAS_DOTENV=/etc/qatlasd/.env \
  ghcr.io/<org>/qatlasd:v0.x.y serve
```

### 3.4 Kubernetes

```yaml
spec:
  containers:
  - name: qatlasd
    image: ghcr.io/<org>/qatlasd:v0.x.y
    env:
    - name: QATLAS_HTTP_ADDR
      value: "0.0.0.0:4200"
    envFrom:
    - configMapRef:
        name: qatlasd-config        # 非密字段
    - secretRef:
        name: qatlasd-secrets       # 含 NEO4J_PASSWORD / S3 keys 等
```

### 3.5 `qatlasd service install` 子命令

辅助生成 systemd unit 的交互式 cobra 子命令。当前默认生成 §3.1.b（`Environment=QATLAS_DOTENV=` 引用外部 .env）形式：

```bash
qatlasd service install --dotenv-path /home/timidly/QuantumAtlas/.env --force
```

`--dotenv-path` 优先级：CLI flag > `$QATLAS_DOTENV` > `~/QuantumAtlas/.env` > `./.env`（TTY 时自动检测并要确认）。

> 想生成 §3.1.a（inline `Environment=` 单文件无外部依赖）形式的 unit 吗？目前 `service install` 还**没有** `--inline-env` 选项，要手写 unit 文件。tracked as future enhancement.

---

## 4. 启动时验证

`internal/config/config.go::Load` 启动时执行以下校验，**任何一项失败立即 fatal**：

| 校验 | 触发条件 |
|---|---|
| **S3 all-or-nothing** | 6 个 S3 字段（含 3 个 per-kind bucket + 4 个连接字段，其中 access/secret 二选一计为 2）半填 |
| **`QATLAS_S3_BUCKET` legacy sentinel** | 该 env var 被设（任何非空值） |
| **`QATLAS_S3_ENDPOINT` scheme** | 缺 `http://` / `https://` 前缀 |
| **PAT scope enforcer build** | `pat.NewEnforcer()` 构造失败（编译时静态 policy，正常不会失败） |
| **`QATLAS_PB_DATA_DIR` 路径** | 如果设了，flag injection 时检查路径可写 |

启动成功后，下列事实会写进 stdout / journal log，可用来确认 env 是否真生效：

| log 行 | 含义 |
|---|---|
| `loaded .env path=/home/timidly/QuantumAtlas/.env` | 确认 godotenv 找到的文件路径 |
| `no .env located; relying on process environment alone` | 完全靠 OS env vars 跑 |
| `raw store: S3 backend http://10.144.18.10:9000/qatlas-pdf` | S3 backend 启用；**每个 bucket 一行**（PDF/MD/Images 必有 3 行；OpenAlex 配了再多 1 行） |
| `raw store: S3 backend <internal> (presign via <public>)` | dual-endpoint 模式生效 |
| `raw store: LocalStore /path/to/raw` | S3 fallback 到本地文件后端 |
| `bucket versioning: enabled (was: ...)` | 每个 per-kind bucket 启动时 `SetBucketVersioning(Enabled)` |
| `system PAT enabled length=N scopes=[*]` | `QATLAS_SYSTEM_PAT` 已加载 |
| `mineru claim granted arxiv_id=... claim_id=... ...` | mineru-claim 端点被调时的审计 log |

---

## 5. 跨工具 .env 文件格式兼容性

`qatlasd` 用的 `joho/godotenv` 解析格式跟下列工具**互相兼容**（同一份 .env 可被多个工具读）：

- Ruby `dotenv` (rb)
- Node `dotenv` (js)
- Python `python-dotenv`（qatlas client 用）
- Docker `--env-file` / docker-compose `env_file:`
- systemd `EnvironmentFile=`（注意：systemd 不支持变量展开，只支持 `KEY=VAL` 字面值）

**支持**：`KEY=VAL`、`KEY="quoted"`、`KEY='single quoted'`、`#` 注释、跨行（双引号内）、空行、变量内插 `${OTHER_KEY}`（双引号 / 无引号时展开；单引号字面）。

**不支持**：shell-style export `export KEY=VAL`（被忽略前缀）、shell expansion `$(cmd)` 或 `~/`（字面值）。

---

## 6. 相关源码

| 文件 | 内容 |
|---|---|
| `cmd/qatlasd/main.go::loadDotEnv` | .env 文件查找 + load |
| `internal/config/config.go::Load` | 从 os.Environ 读出 Config struct |
| `internal/config/config.go::firstEnv / firstEnvDefault / firstEnvIntDefault` | helper 函数 |
| `internal/config/config.go::validateS3Config` | S3 all-or-nothing + legacy `_BUCKET` 拒 |
| `internal/config/config.go::defaultXDGSubdir / defaultWikiDir` | 默认路径 |
| `internal/pat/system_pat.go::LoadSystemPAT` | 读 `QATLAS_SYSTEM_PAT` / `_SCOPES` |
| `cmd/qatlasd/service_cmd.go` | `qatlasd service install` 子命令；TTY 模式检测 .env |
| `cmd/qatlasd/main.go::injectPBDataDirFlag` | `QATLAS_PB_DATA_DIR` → PocketBase `--dir=` 注入 |

---

## 7. 完整 .env 模板

参见 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example) —— 含所有字段 + 每个字段的角色注释（client 用 / server 用 / 共用）。

## 8. `qatlasd config` 子命令

binary 自带三个 config 工具，复刻 code-server / kubectl / gh 的常见用法。**全部 short-circuit 在 main 早期跑**——不依赖 .env / Neo4j / S3 配置正确，即便配置半填、Neo4j 没起、S3 字段缺失也能跑（这是它存在的意义之一）。

### `qatlasd config init`

写一份最小化默认 `.env` 模板（embed 在 binary 内的 [`cmd/qatlasd/templates/default.env`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/cmd/qatlasd/templates/default.env)，含 GitHub OAuth / Neo4j / S3 / SystemPAT 等最常用字段）到磁盘。完整字段参考仍是 repo 根的 `.env.example`。

```bash
# 写到 XDG 默认位置（$XDG_CONFIG_HOME/qatlasd/.env 或 ~/.config/qatlasd/.env）
qatlasd config init

# 写到指定路径（典型生产）
sudo qatlasd config init --path /etc/quantum-atlas/.env

# 已存在不覆盖（exit 1）；要覆盖加 --force
qatlasd config init --path /etc/quantum-atlas/.env --force
```

文件 mode 强制 **0600**，避免 secret 被 group/other 读到。写完后会提示用 `qatlasd serve` 或 `qatlasd service install --dotenv-path ...` 把它接进 systemd。

> 模板特意不全 —— 只列必填 / 强烈推荐字段。要看所有 alias / dev-only flag / 第三方 SDK 字段，去看 [`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)。

### `qatlasd config path`

打印**当前进程**会 load 的 .env 路径（resolve 顺序跟 §1.1 一致）。找不到任何文件时 exit 1 + stderr 报错。

```bash
qatlasd config path
# /etc/quantum-atlas/.env
```

适合 systemd 单元 / shell 脚本里调试"server 真的读到这个 .env 吗"。

### `qatlasd config show`

按 KEY=VALUE 形式打印**当前进程**可见的 QuantumAtlas 相关 env vars（按 name 排序，空值跳过，前缀过滤到 `QATLAS_*` / `NEO4J_*` / `MINERU_*` / `OPENAI_*` / `ANTHROPIC_*` / `GITHUB_CLIENT_*`）。

```bash
qatlasd config show
# NEO4J_URI=bolt://localhost:7687
# NEO4J_PASSWORD=***
# QATLAS_S3_ENDPOINT=https://rustfs.example.com
# QATLAS_S3_SECRET_ACCESS_KEY=***
# QATLAS_SERVER_URL=https://atlas.example.com
```

**默认脱敏**：name 含 `TOKEN` / `SECRET` / `KEY` / `PASSWORD`（大小写不敏感）的 value 替成 `***`。要看明文（debug 用，仅在私有终端）：

```bash
qatlasd config show --no-redact
```

⚠️ **`show` 反映的是 `qatlasd config show` 这次调用看到的 env，不会自动 source 任何 .env**。要查"server 真起来时会看到什么"，先 source 那份 .env：

```bash
set -a; . /etc/quantum-atlas/.env; set +a
qatlasd config show
```

或者直接登上 systemd 单元的 env：

```bash
sudo systemctl show qatlasd -p Environment
```

