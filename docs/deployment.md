# Deployment

## 适用范围

这份文档描述的是 QuantumAtlas 服务的部署方式。

目标是把下面几件事拆清楚：

- 如何本地启动一个可工作的服务。
- 如何把它安装成长期运行的 systemd 服务。
- 如何在公网入口前放置反向代理和鉴权层。
- 如何在不暴露真实机器名、真实地址或私有路由结构的前提下，给出可复用的 Caddy 示例。

> 本文不收录任何一次性 ops 脚本。生产部署时把"运维动作"通过本文档的
> "思路 + 模板"自己拼出来；过往一次性脚本只保留在维护者 `/tmp/` 直到完成，
> 然后丢弃。这样仓库不积累陈旧脚本，每次环境改动都强迫维护者重新理解。

## Go server 部署（当前路径）

QuantumAtlas server 是 Go binary（编译产物 `build/quantumatlas`，~35MB
statically linked，CGO-free）。下游部署 = 把 binary 推到目标 host +
装 systemd unit + 反代。这里给出一份**单机部署模板**，路径都用占位变量
（`<USER>` / `<APP_HOME>` / `<WIKI_DIR>` / `<HTTP_PORT>`），运维替换后即可。

### 1. 目录布局（推荐）

按 XDG Base Directory（[freedesktop spec][xdg-spec]）+ FHS 拆分：git
checkout 只放代码 + 配置；用户级状态去 `$XDG_DATA_HOME`（默认
`$HOME/.local/share/`）；系统级状态去 `/var/lib/`。**不再**把 wiki /
raw / data / pb_data 默认塞进 git checkout 内。

[xdg-spec]: https://specifications.freedesktop.org/basedir-spec/latest/

用户级（per-user systemd 或 `--user` ExecStart）：

```
/home/<USER>/
├── QuantumAtlas/                  # git checkout，只放代码 + .env
│   ├── .env                       # 运行配置；server 用 godotenv 读
│   └── ...                        # 仓库源码（再无 wiki/ raw/ data/ pb_data/）
├── QuantumAtlas-Wiki/             # 兄弟 checkout — WIKI_DIR 默认值
├── .local/
│   ├── bin/quantumatlas           # binary（user-writable，sudoless deploy）
│   └── share/quantum-atlas/       # XDG_DATA_HOME 下，所有 stateful 状态
│       ├── raw/                   # RAW_DIR 默认值（PDF / MinerU 输出）
│       ├── data/                  # DATA_DIR 默认值（shares / claims）
│       └── pb_data/               # PBDataDir 默认值（PocketBase SQLite）
```

系统级（多用户共享，shared /var/lib 模式，类 Grafana / Gitea）：

```
/etc/quantum-atlas/.env            # 配置；ExecStart 用 QATLAS_DOTENV 指过来
/usr/local/bin/quantumatlas        # 系统 binary
/var/lib/quantum-atlas/            # FHS 状态根
├── raw/
├── data/
├── pb_data/
└── QuantumAtlas-Wiki/             # 也可以放别处，用 QATLAS_WIKI_DIR 指
```

两种布局都不要求显式覆盖 `.env`：server 会按 `$XDG_DATA_HOME` /
`$HOME` 自动算出默认。**只**在需要存到非默认路径（FHS / 共享挂载点 /
独立分区）时显式覆盖 `QATLAS_RAW_DIR` 等。

binary 路径选 `~/.local/bin/` vs `/usr/local/bin/` 的取舍：

- `~/.local/bin/` 归运行用户所有，**滚 binary 不需要 sudo**——本地
  `scp build/quantumatlas TARGET:/tmp/quantumatlas-go` 后远端
  `install -m 0755 /tmp/quantumatlas-go ~/.local/bin/quantumatlas`
  全部以普通用户身份完成。配 user-mode systemd 单元时连 restart
  也免 sudo。
- `/usr/local/bin/` 是 root-owned，每次 binary 滚动都得 sudo install。
  典型 system-mode 部署（FHS / 多用户共享）会这么放。
- systemd 单元可以引用任意路径——`ExecStart=/home/<USER>/.local/bin/quantumatlas`
  跟 `/usr/local/bin/quantumatlas` 在 systemd 视角下完全等价。

### 2. systemd unit 模板

下面两种 unit 形式都常用，选一种即可。**两种模式的核心取舍**：

| 维度 | A. user-mode (`~/.config/systemd/user/`) | B. system-mode (`/etc/systemd/system/`) |
|---|---|---|
| 文件归属 | 运行用户拥有，不需要 sudo 编辑 | root 拥有，需要 sudo 编辑 |
| restart 权限 | 免 sudo（`systemctl --user restart`） | 需 sudo（`sudo systemctl restart`） |
| 启动时机 | 需要 `loginctl enable-linger <user>` 让未登录也保活 | boot 自起，无需 linger |
| systemd hardening 能力 | 基本（`PrivateTmp` 等部分指令不可用） | 完整（`ProtectSystem` / `ReadWritePaths` 全可用） |
| 适用场景 | 个人维护 / 频繁迭代 / 一人一服务 | 严格 hardening / 多用户共享 / 标准 FHS 部署 |

**A. user-mode systemd unit**（`~/.config/systemd/user/qatlas.service`）：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple

# Server 用 github.com/joho/godotenv 加载 .env。把绝对路径作为
# QATLAS_DOTENV 传进来，server 会用它的所在目录作为相对路径 anchor
# （WIKI_DIR=../QuantumAtlas-Wiki 因此能解析到 %h/QuantumAtlas-Wiki）。
# 不要用 systemd 的 EnvironmentFile= 指令 —— 那个只把内容注入 env，
# 拿不到文件路径，server 就没办法做相对路径 anchor。
# %h 在 user-mode unit 里展开成 $HOME。
Environment=QATLAS_DOTENV=%h/QuantumAtlas/.env

# 仅在被 v4-only portproxy 包裹的 host (典型: WSL2 + Windows netsh)
# 才设这个。Plain Linux 云 VPS 不要打开，让 server 走 PocketBase 默认
# dual-stack v6 socket 同时服务 v4 + v6 client。
# Environment=QATLAS_FORCE_TCP4=1

WorkingDirectory=%h/QuantumAtlas

# pb_data 路径只通过 .env 里的 QATLAS_PB_DATA_DIR 控制（默认
# $XDG_DATA_HOME/quantum-atlas/pb_data），server 启动时自动转换为
# PocketBase 的 --dir= 参数。**不要**在 ExecStart 里硬写 --dir=...：
# cmdline 优先级最高，会让 .env 里的同字段失效，排障时容易踩。
ExecStart=%h/.local/bin/quantumatlas serve --http=0.0.0.0:<HTTP_PORT>
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

[Install]
WantedBy=default.target
```

启用 + 起动（**无 sudo**）：

```bash
systemctl --user daemon-reload
systemctl --user enable --now qatlas.service
systemctl --user status qatlas.service
loginctl enable-linger "$USER"   # 一次性：未登录也保活
```

**B. system-mode systemd unit**（`/etc/systemd/system/qatlas.service`）：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=<USER>
Group=<USER>

Environment=QATLAS_DOTENV=/home/<USER>/QuantumAtlas/.env
# Environment=QATLAS_FORCE_TCP4=1

WorkingDirectory=/home/<USER>/QuantumAtlas
# pb_data 路径同 user-mode：靠 .env 的 QATLAS_PB_DATA_DIR 控制，
# 不要在 ExecStart 里写 --dir=...
ExecStart=/home/<USER>/.local/bin/quantumatlas serve --http=0.0.0.0:<HTTP_PORT>
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

# Hardening：read-only 系统目录 + 只把 stateful 路径打开写权限。
# ReadWritePaths 必须覆盖 .env 里所有非默认目录（RAW_DIR / DATA_DIR /
# PBDataDir / WikiDir），按实际部署调整。下面示例对应"全部走 XDG 默认"：
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
ReadWritePaths=/home/<USER>/QuantumAtlas /home/<USER>/.local/share/quantum-atlas /home/<USER>/QuantumAtlas-Wiki
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
```

启用 + 起动（需要 sudo）：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now qatlas.service
sudo systemctl status qatlas.service
```

### 3. 日常 deploy 流程

**A. user-mode**——零 sudo：

```bash
pixi run build                # 出 build/quantumatlas (CGO-free, static)
scp build/quantumatlas TARGET:/tmp/quantumatlas-go
ssh TARGET "install -m 0755 /tmp/quantumatlas-go ~/.local/bin/quantumatlas && systemctl --user restart qatlas"
```

**B. system-mode**——最后一步需 sudo：

```bash
pixi run build
scp build/quantumatlas TARGET:/tmp/quantumatlas-go
ssh TARGET "install -m 0755 /tmp/quantumatlas-go ~/.local/bin/quantumatlas"
ssh -t TARGET "sudo systemctl restart qatlas"
```

两种模式下，读 systemd 状态（`systemctl [--user] status` /
`journalctl [--user] -u qatlas`）都不需要 sudo。

### 4. .env 必填字段

参考 `.env.example`。Server 侧最小集（**只有真正想覆盖默认时才写**
`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR`）：

```env
QATLAS_SERVER_URL=https://your-domain.tld
QATLAS_SERVER_HOST=0.0.0.0
QATLAS_SERVER_PORT=4200

# 显式覆盖示例（不写就走 XDG / sibling 默认）：
# QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw
# QATLAS_DATA_DIR=/srv/quantum-atlas/data
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data

GITHUB_CLIENT_ID=<oauth_app_client_id>
GITHUB_CLIENT_SECRET=<oauth_app_secret>
# 未来 admin 提权白名单，handler 待补；现在写了也不会生效。
# QATLAS_ADMIN_GITHUB_LOGINS=alice,bob
```

GitHub OAuth App callback URL 配 `https://your-domain.tld/api/oauth2-redirect`。

### 5. 从旧部署迁移到当前布局

如果之前 binary 装在 `/usr/local/bin/`、unit 写死 system-wide 路径、或
wiki / raw / data / pb_data 直接放在 git checkout 里——一次性迁移思路
见 [docs/migration-storage-layout.md](migration-storage-layout.md)，
该文档覆盖：

- 把 wiki / raw / data / pb_data 从仓库内搬到 `$XDG_DATA_HOME/quantum-atlas/`
- binary 从 `/usr/local/bin/` 挪到 `~/.local/bin/`
- systemd unit 调整 + 启动验证

每步都该有备份（`cp -a <path> <path>.bak-$(date +%s)`）。整个流程
**不写成提交进仓库的脚本**——下次迁移环境可能完全不一样，强迫维护者
重新读这一节比照本机情况自己拼脚本，更不容易把陈旧假设拷过去。

### 6. 对象存储（RustFS）

PDF / MinerU 输出等大 blob 走 S3 兼容对象存储而不是本地 `RAW_DIR`。
Go server 通过 `internal/objstore` 抽象层接 minio-go SDK，**填齐
`QATLAS_S3_*` 四字段就切 RustFS，留空就 fallback 本地 `RAW_DIR`**
（dev / CI 无外部依赖）。**注意：四字段是 all-or-nothing**——半填会
启动直接报错退出，避免 reader / writer 跑两套后端。

物理部署、bucket / IAM user / policy 的创建、rotate 流程，以及配套的
幂等 bootstrap 脚本 [`scripts/rustfs_bootstrap.sh`](../scripts/rustfs_bootstrap.sh)
统一收录在 [docs/storage-design.md `## RustFS 部署后置`](storage-design.md#rustfs-部署后置bucket--user--policy-自助配置)。

简言之：

```bash
export RUSTFS_ENDPOINT=https://raw.your-domain.tld
export RUSTFS_ROOT_ACCESS_KEY=<root_ak>      # 维护者密码管理器，不在 git
export RUSTFS_ROOT_SECRET_KEY=<root_sk>
bash scripts/rustfs_bootstrap.sh
# 末尾打印出绑死单桶的 access_key / secret_key
# 之后写进 server .env：
#   QATLAS_S3_ENDPOINT=https://raw.your-domain.tld
#   QATLAS_S3_BUCKET=qatlas-raw
#   QATLAS_S3_ACCESS_KEY_ID=<上面打印的>
#   QATLAS_S3_SECRET_ACCESS_KEY=<上面打印的>
# 重启 server，启动 log 会打印 `raw store: S3 backend ...` 确认切换成功
```

切到 S3 后端后，`/share/{token}/...` 下载会自动 302 到 RustFS
presigned URL（5min TTL，绕过 server 节省 VPS 带宽）；本地 RawDir
后端继续 ServeFile 走老路。`/api/papers/{arxiv_id}/resources` 返回的
URL 不区分后端 —— 客户端拿到 share URL 后直接 GET 即可，redirect 由
server 透明处理。

边缘 Caddy 多加一个站点把 `raw.your-domain.tld` 反代到 RustFS `:9000`
即可，模板见 storage-design 文档对应章节。

## 旧版 Python 部署

旧的 `atlas/server/` FastAPI server（`uv run -m atlas.server`、
`uv run -m atlas.server.service install`、外置 caddy-security 鉴权链等）
已不是生产路径，独立收录在 [docs/python-legacy.md](python-legacy.md)，
仅供 client 兼容性测试或排查历史 issue 时参考。新部署一律走上面的 Go
server 路径。

## 推荐的单机生产目录

代码 + 配置在 git checkout，stateful 状态走 XDG_DATA_HOME（用户级）或
`/var/lib/`（系统级）。默认值不需要在 `.env` 里显式写——`server` 启动
时按 `$XDG_DATA_HOME` / `$HOME` 自动算出来。只有当默认值不合适（共享
盘 / FHS / 独立分区）才在 `.env` 里覆盖。

```env
# 一切都跑默认时，server 侧 .env 只需要这点：
QATLAS_SERVER_URL=https://atlas.example.com
QATLAS_SERVER_HOST=127.0.0.1
QATLAS_SERVER_PORT=4200
NEO4J_URI=bolt://127.0.0.1:7687

# 想覆盖默认时：
# QATLAS_WIKI_DIR=../QuantumAtlas-Wiki                # 默认就是这个
# QATLAS_RAW_DIR=/srv/quantum-atlas/raw               # 默认 XDG，FHS 覆盖
# QATLAS_DATA_DIR=/srv/quantum-atlas/data
# QATLAS_PB_DATA_DIR=/var/lib/quantum-atlas/pb_data
```

> 旧名（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `PUBLIC_BASE_URL` / `SHARE_ACCESS_TOKEN` / `USER_HEADER` / `DEFAULT_SHARE_EXPIRES_IN` / `QUANTUMATLAS_REQUIRE_RELEASE_TAG`）仍作 alias 保留，新部署推荐用 `QATLAS_*` 前缀。`NEO4J_*` / `OPENAI_*` / `ANTHROPIC_*` / `MINERU_*` 等第三方 SDK 标准名保持原样。

建议：

- 应用仓库按 release tag 或受控分支部署。
- Wiki 仓库单独 checkout，并允许更高频更新；server 侧 checkout 应保持干净，只通过 `git pull --ff-only` 消费远端内容。
- 运行 QuantumAtlas 的服务用户默认只需要读取 `WIKI_DIR`；如果启用 `/api/wiki/sync/pull`，还需要对该 Git checkout 有 fast-forward 更新权限。服务端不会生成或修改 Wiki 页面，Wiki 内容修改应在用户端或独立的 `QuantumAtlas-Wiki` checkout 中完成。
- 运行 QuantumAtlas 的服务用户应对 `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` 有写权限。三者默认都落在 `$XDG_DATA_HOME/quantum-atlas/`（即 `$HOME/.local/share/quantum-atlas/`）下，正常的 systemd `User=<svc>` 已经自动满足；只在显式覆盖到 FHS / 独立分区时检查权限。
- 内容生产、LLM 生成、人工编辑和审阅走 `QuantumAtlas-Wiki` 的普通 Git 流程；QuantumAtlas server 不提供 push API，也不通过 Web UI 直接写 Wiki 页面。
- 若 `/api/wiki/sync/status` 提示 Wiki checkout 不在 `main` 或 `master`，应检查部署分支是否符合预期。
- Neo4j 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 `QATLAS_SERVER_URL`。

## 核心环境变量

字段语义、是否必填、是否分 client/server 角色等完整说明以
[`.env.example`](../.env.example) 顶部对照表为准（同时也是 client / server
共享的 canonical 文档；Go server 的运行时默认在
`internal/config/config.go`）。本节只列出公网部署最容易踩的几个：

| 变量 | 何时需要 | 备注 |
|---|---|---|
| `QATLAS_SERVER_URL` | 必填 | 对外唯一根地址；share 链接、CLI 默认、MinerU 回调都基于它 |
| `QATLAS_SERVER_HOST` / `QATLAS_SERVER_PORT` | 默认 `127.0.0.1:4200` | 直接面向公网通常改 `0.0.0.0:<port>`，反代场景保留 `127.0.0.1` |
| `NEO4J_URI` / `NEO4J_USER` / `NEO4J_PASSWORD` | 启用图谱时必填 | 不连图库可留空 |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | 启用 GitHub OAuth 登录时必填 | 启动时由 `internal/auth/oauth.go` 注入 users collection |
| `QATLAS_SHARE_ACCESS_TOKEN` | 想要稳定公开 share 入口时设 | 默认走每用户动态 share token |
| `QATLAS_USER_HEADER` | 上游反代/SSO 注入审计身份头时设 | 不参与鉴权，仅用于日志 |
| `QATLAS_REQUIRE_RELEASE_TAG` | 要求代码版本对齐 release tag 时设 | 生产保护开关 |
| `QATLAS_FORCE_TCP4` | WSL2 + Windows netsh portproxy 场景设 | 普通 Linux VPS 不要打开 |

其余字段（`QATLAS_WIKI_DIR` / `QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` /
`QATLAS_PB_DATA_DIR` / `QATLAS_S3_*` / `MINERU_*` / `OPENAI_*` 等）都在
`.env.example` 里有详细注释，按需取消注释即可。

## 反向代理与鉴权边界

Go server 内嵌的 PocketBase 自带 OAuth + session 管理。**不需要**外置
caddy-security / oauth2-proxy 这类身份代理；反代只承担 SNI 选路 + TLS
终结 + 反向转发到后端。鉴权完全在 server 内部：

- 浏览器：访问 `/auth-with-oauth2`（PocketBase 内置）走 GitHub OAuth，
  登录后从 `/token` 页面拷 14d 寿命的 session token，或者在 `/pat` 页
  创建 fine-grained PAT（前缀 `qat_`，过期可选）。
- CLI / 自动化：`Authorization: Bearer <token>`，server 端 `authGuard`
  根据前缀分发——`qat_*` 走 `internal/pat` 包做 prefix lookup + bcrypt
  校验并查 scope；其余走 PocketBase session token 验证。
- 写口分两层：`scopeGuard(enforcer, obj, act, handler)` 给"PAT 可调"
  的写口（papers / shares），强制 scope opt-in；`sessionGuard` 给"PAT
  不可调"的写口（PAT 自管理本身、admin 操作），只接受 session token。

如需在边缘补一层 IP/路径 ACL、按域名分流多服务、或做 raw 对象存储反代
（`raw.your-domain.tld` → RustFS），Caddy 模板下面给出。

### 路径分类与对应处理

| 路径 | 鉴权层 | 反代怎么写 |
|---|---|---|
| `/health` | open | 直接 reverse_proxy；监控可读 |
| `/{path...}`、`/_/`、`/auth-with-oauth2` 等 SPA + PocketBase 内置 | open / 自管 | 直接 reverse_proxy；OAuth 由 server 自己处理 |
| `/api/wiki/...`、`/api/pages`、`/api/search`、`/api/stats`、`/api/graph/*`、`/api/lint` | open（公开读） | 直接 reverse_proxy |
| `/api/papers/...`、`/api/shares/...`、`/api/pat/...` | server 内的 `authGuard` / `scopeGuard` / `sessionGuard` | 直接 reverse_proxy；**不要**剥 `Authorization` header（server 要拿来鉴权） |
| `/share/{token}` / `/share/{token}/{path...}` | open（只校验 token） | 直接 reverse_proxy；公网可访问 |
| `raw.your-domain.tld/*`（启用 S3/RustFS 时） | RustFS 自管（presigned URL） | 反代到 RustFS `:9000` |

> **历史背景**：旧 FastAPI 时代曾经在 Caddy 上挂 caddy-security 做
> GitHub OAuth portal、用 `JWT_SHARED_KEY` 跨边缘共享 JWT，再把
> `X-Token-Subject` header 注入 FastAPI。Go server 迁移后**整条链已下
> 线**——边缘 Caddy 不再读写身份 header，也不验任何 JWT。`QATLAS_USER_HEADER`
> 仍可用，但只用作"反代要审计哪个用户"的日志字段，不参与鉴权决策。

## Share 机制

QuantumAtlas 的 share 是"按路径授权的公开链接"，不是用户登录态，也不是 API 鉴权。`/share/{token}` 和 `/share/{token}/{path}` 默认应允许公网访问；任何拿到 share URL 的人都能访问该 token 允许的资源。

当前有两类 share token：

- 登录用户创建的动态 share token：已登录用户通过受保护的 `POST /api/shares/`（鉴权：`shares:write` scope 或 session token）创建，记录保存在 PocketBase `shares` collection 里（DATA_DIR 不再持久化 share JSON，旧文档残留概念）。请求里可以指定 `paths`、`label` 和 `expires_in`；如果没有指定 `expires_in`，服务使用 `DEFAULT_SHARE_EXPIRES_IN`。这些 token 可以通过 `GET /api/shares/` 查看、`DELETE /api/shares/{token}` 撤销。
- 部署者配置的 `SHARE_ACCESS_TOKEN`：这是额外的、可选的、稳定分享入口。设置后，QuantumAtlas 会把它当作一个不写入 collection、不自动过期的内置 share token，用于访问 canonical paper assets：`papers/pdf`、`papers/markdown`、`papers/json`、`papers/images`。不需要稳定公开链接时不要设置它。

安全边界：

- `/api/shares/` 写口（POST/DELETE）由 server 内 `scopeGuard` 强制鉴权（`shares:write`），反代不需要再叠一层。
- `/share/*` 是公开资源入口，只校验 share token，不校验登录用户。
- share token 只授权配置记录中的资源路径；路径必须是相对路径，不能包含绝对路径、反斜杠或 `..`。
- `SHARE_ACCESS_TOKEN` 应使用足够长的随机值；不要用示例值、短词或可猜测字符串。

## Caddy 示例

Caddy 现在只承担 SNI 选路、TLS 终结和反代，不再挂任何 oauth2-proxy /
caddy-security / forward_auth 链。两个常见模板：

### 单域名 + 自带 LE 证书

```caddyfile
atlas.example.com {
    encode gzip zstd

    # 健康检查可独立路由（让监控不被全局 directives 影响）
    handle /health {
        reverse_proxy 127.0.0.1:4200
    }

    # 其余全部裸反代到 Go server，鉴权在 server 内部完成。
    handle {
        reverse_proxy 127.0.0.1:4200
    }
}
```

### 多线路 / 国内未备案节点（自签证书）

国内未备案 VPS 通常没法挂任何域名走 443，只能 IP + 非标端口 + 自签：

```caddyfile
# 内网走 Let's Encrypt 真证书（如果域名 A 记录指过来）
atlas.example.com {
    reverse_proxy 127.0.0.1:4200
}

# IP + 非标端口模式（自签 Caddy Local CA）
https://203.0.113.10:18443 {
    tls internal
    reverse_proxy 127.0.0.1:4200
}
```

client 端如果要走 IP + 非标端口入口，需要 `qatlas --insecure ...` 或 .env
里 `QATLAS_INSECURE=1` 跳过证书校验；想保留真证书验证又用第二条线路，
就在本机 hosts 把 `atlas.example.com` 覆盖到对应 IP，TLS 走 SNI 仍然信
任原证书。

### 加 raw 对象存储反代（启用 RustFS 时）

```caddyfile
raw.your-domain.tld {
    encode gzip zstd
    reverse_proxy 127.0.0.1:9000   # RustFS s3 endpoint
}
```

share URL 会 302 到 `https://raw.your-domain.tld/...` 的 presigned URL
（5min TTL），bandwidth 绕开 server。

## 运行建议

- 反代上**不要**清洗或注入 `Authorization` header——server 要拿它做
  bearer 鉴权（PAT 或 session token），剥掉会全部 4xx。
- `/api/*` 中的写口 server 已经强制鉴权；反代上不要再叠 ACL，避免双重
  401 / 403 给 debug 添麻烦。
- `/share/*` 公开并不意味着管理接口也应公开；`/api/shares/` 的写口由
  server 内 `scopeGuard` 把关，公网可达没问题。
- 如果启用了 MinerU 并需要它回拉 PDF，`QATLAS_SERVER_URL` 必须能从
  MinerU 所在环境访问到。
