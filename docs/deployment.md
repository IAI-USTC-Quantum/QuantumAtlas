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

按"运行用户家目录下、git checkout 与状态分离"的常规约定：

```
/home/<USER>/
├── QuantumAtlas/            # git checkout (跟踪稳定分支)，主要给 server 读：
│   ├── .env                 # 运行配置；server 用 godotenv 读这个文件
│   ├── pb_data/             # PocketBase SQLite + uploads (gitignored)
│   └── ...                  # 仓库源码 (server 不强依赖，但 /api/wiki/sync
│                            # 操作的本地 wiki repo 路径相对此目录解析)
├── QuantumAtlas-Wiki/       # 单独 checkout，给 server 当 wiki source
└── .local/bin/
    └── quantumatlas         # binary（user-writable，sudoless deploy）
```

为什么 binary 放 `~/.local/bin/` 而不是 `/usr/local/bin/`：

- `~/.local/bin/` 归运行用户所有，**滚 binary 不需要 sudo**——本地
  `scp build/quantumatlas TARGET:/tmp/quantumatlas-go` 后远端
  `install -m 0755 /tmp/quantumatlas-go ~/.local/bin/quantumatlas`
  全部以普通用户身份完成。
- `/usr/local/bin/` 是 root-owned，每次 binary 滚动都得 sudo install。
  在频繁迭代的项目里不必要。
- systemd 单元可以引用任意路径——`ExecStart=/home/<USER>/.local/bin/quantumatlas`
  跟 `/usr/local/bin/quantumatlas` 在 systemd 视角下完全等价。
- 唯一仍需 sudo 的动作是 `systemctl restart qatlas`（system unit
  归 root），这没法绕。一次性 `sudo systemctl restart qatlas` 比每次
  滚 binary 都 sudo 安全得多。

### 2. systemd unit 模板

`/etc/systemd/system/qatlas.service`：

```ini
[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=<USER>
Group=<USER>

# Server 用 github.com/joho/godotenv 加载 .env。把绝对路径作为
# QATLAS_DOTENV 传进来，server 会用它的所在目录作为相对路径 anchor
# （WIKI_DIR=../QuantumAtlas-Wiki 因此能解析到 /home/<USER>/QuantumAtlas-Wiki）。
# 不要用 systemd 的 EnvironmentFile= 指令 —— 那个只把内容注入 env，
# 拿不到文件路径，server 就没办法做相对路径 anchor。
Environment=QATLAS_DOTENV=/home/<USER>/QuantumAtlas/.env

# 仅在被 v4-only portproxy 包裹的 host (典型: WSL2 + Windows netsh)
# 才设这个。Plain Linux 云 VPS 不要打开，让 server 走 PocketBase 默认
# dual-stack v6 socket 同时服务 v4 + v6 client。
# Environment=QATLAS_FORCE_TCP4=1

WorkingDirectory=/home/<USER>/QuantumAtlas

# --dir= 必填：PocketBase 默认把 pb_data/ 写在 binary 同目录，
# 而 ~/.local/bin/ 不是合适的 data 路径。显式指定到 git checkout 下的
# pb_data/（.gitignore 已经过滤）。
ExecStart=/home/<USER>/.local/bin/quantumatlas serve --http=0.0.0.0:<HTTP_PORT> --dir=/home/<USER>/QuantumAtlas/pb_data
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

# Hardening：read-only 系统目录 + 只把数据/wiki/可选挂载点打开写权限。
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
ReadWritePaths=/home/<USER>/QuantumAtlas /mnt/team
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
```

启用 + 起动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now qatlas.service
sudo systemctl status qatlas.service
```

### 3. 日常 deploy 流程

本地：

```bash
pixi run build                # 出 build/quantumatlas (CGO-free, static)
scp build/quantumatlas TARGET:/tmp/quantumatlas-go
ssh TARGET "install -m 0755 /tmp/quantumatlas-go ~/.local/bin/quantumatlas"
ssh -t TARGET "sudo systemctl restart qatlas"
```

只有最后一步要 sudo。本地/远端读 systemd 状态（`systemctl status` /
`journalctl -u qatlas`）不需要 sudo。

### 4. .env 必填字段

参考 `.env.example`。Server 侧最小集：

```env
QATLAS_SERVER_URL=https://your-domain.tld
QATLAS_SERVER_HOST=0.0.0.0
QATLAS_SERVER_PORT=4200
WIKI_DIR=../QuantumAtlas-Wiki        # 相对 .env 所在目录
RAW_DIR=/srv/quantum/raw             # 或挂载点
DATA_DIR=/srv/quantum/data
GITHUB_CLIENT_ID=<oauth_app_client_id>
GITHUB_CLIENT_SECRET=<oauth_app_secret>
QATLAS_ADMIN_GITHUB_LOGINS=alice,bob
```

GitHub OAuth App callback URL 配 `https://your-domain.tld/api/oauth2-redirect`。

### 5. 从旧部署迁移到当前布局

如果之前 binary 装在 `/usr/local/bin/`、unit 写死 system-wide 路径、或
PocketBase data 放在某个跟仓库分开的"-go"目录——一次性迁移思路：

1. `sudo systemctl stop qatlas.service`
2. 把 binary 复制到 `~/.local/bin/quantumatlas`（user-writable）
3. 把 pb_data 移到 `~/<repo>/pb_data/` （.gitignore 已过滤）
4. 改 unit 的 `ExecStart` / `WorkingDirectory` / `--dir=` 指向新路径
5. 删旧的系统 binary / 旧 data 目录
6. `sudo systemctl daemon-reload && sudo systemctl start qatlas.service`
7. 验证：`curl http://127.0.0.1:<HTTP_PORT>/health` 应 `{"status":"healthy"}`，
   `curl /api/stats` 应 `total_pages` > 0（wiki 没丢）。

每步都该有备份（`cp -a <file> <file>.bak-$(date +%s)`）。整个流程
**不写成提交进仓库的脚本**——下次迁移环境可能完全不一样，强迫维护者
重新读这一节比照本机情况自己拼脚本，更不容易把陈旧假设拷过去。

## 旧版 Python 部署

## 最小本地启动

```bash
uv sync --extra dev
docker compose up -d
cp .env.example .env
uv run --script scripts/init_primitives.py
uv run -m atlas.server
```

默认入口：

- QuantumAtlas: `http://127.0.0.1:4200`
- OpenAPI: `http://127.0.0.1:4200/api/docs`
- Neo4j: `http://127.0.0.1:7474`

如果只是跑离线 demo，不需要启动 Neo4j 或 Web 服务：

```bash
uv run --script examples/demo_pipeline.py --algorithm grover --backend qiskit
```

## systemd 安装

用户级安装：

```bash
uv run -m atlas.server.service install --scope user --enable --now
```

如果希望用户级服务在未登录时也能随机器启动：

```bash
loginctl enable-linger "$USER"
```

系统级安装会先生成 unit 文件，再打印需要人工确认执行的 sudo 命令：

```bash
uv run -m atlas.server.service install \
  --scope system \
  --run-as "$USER" \
  --output /tmp/quantum-atlas.service
```

如果需要修改 host 或 port，请先更新 `.env` 或显式传 `--host` / `--port`，然后重新生成并安装 unit。

## 推荐的单机生产目录

只把 Wiki 外置到独立 Git checkout（方便多人 PR / review）；论文资产（`QATLAS_RAW_DIR`）和运行状态（`QATLAS_DATA_DIR`）默认就在仓库内即可，不必单独搬迁路径：

```env
QATLAS_WIKI_DIR=../QuantumAtlas-Wiki
# QATLAS_RAW_DIR=raw          # 默认值，通常不必显式设置
# QATLAS_DATA_DIR=data        # 默认值
NEO4J_URI=bolt://127.0.0.1:7687
QATLAS_SERVER_HOST=127.0.0.1
QATLAS_SERVER_PORT=4200
QATLAS_SERVER_URL=https://atlas.example.com
```

> 旧名（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `PUBLIC_BASE_URL` / `SHARE_ACCESS_TOKEN` / `USER_HEADER` / `DEFAULT_SHARE_EXPIRES_IN` / `QUANTUMATLAS_REQUIRE_RELEASE_TAG`）仍作 alias 保留，新部署推荐用 `QATLAS_*` 前缀。`NEO4J_*` / `OPENAI_*` / `ANTHROPIC_*` / `MINERU_*` 等第三方 SDK 标准名保持原样。

建议：

- 应用仓库按 release tag 或受控分支部署。
- Wiki 仓库单独 checkout，并允许更高频更新；server 侧 checkout 应保持干净，只通过 `git pull --ff-only` 消费远端内容。
- 运行 QuantumAtlas 的服务用户默认只需要读取 `WIKI_DIR`；如果启用 `/api/wiki/sync/pull`，还需要对该 Git checkout 有 fast-forward 更新权限。服务端不会生成或修改 Wiki 页面，Wiki 内容修改应在用户端或独立的 `QuantumAtlas-Wiki` checkout 中完成。
- 运行 QuantumAtlas 的服务用户应对 `RAW_DIR` 和 `DATA_DIR` 有写权限：`RAW_DIR` 用于保存论文资产，`DATA_DIR` 用于保存 share、ingest 状态和版本 manifest。如果要外置（例如挂载到大容量盘），可以显式覆盖，但**不是必需的**。
- 内容生产、LLM 生成、人工编辑和审阅走 `QuantumAtlas-Wiki` 的普通 Git 流程；QuantumAtlas server 不提供 push API，也不通过 Web UI 直接写 Wiki 页面。
- 若 `/api/wiki/sync/status` 提示 Wiki checkout 不在 `main` 或 `master`，应检查部署分支是否符合预期。
- Neo4j 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 `QATLAS_SERVER_URL`。

## 核心环境变量

完整内置默认值以 `atlas/server/config.py` 为准；`.env.example` 只是覆盖模板，不应直接当成生产默认配置。公网部署通常最关心下面这些：

| 变量 | 说明 |
|------|------|
| `QATLAS_WIKI_DIR` | Wiki 知识库目录，推荐指向独立 Git checkout（alias: `WIKI_DIR`） |
| `QATLAS_RAW_DIR` | canonical 论文资产根目录（alias: `RAW_DIR`） |
| `QATLAS_DATA_DIR` | 任务、share、ingest 状态目录（alias: `DATA_DIR`） |
| `QATLAS_SERVER_URL` | 对外唯一根地址，client、share 链接和 MinerU URL 都基于它（alias: `PUBLIC_BASE_URL`） |
| `QATLAS_INSECURE` | client 跳过 TLS 校验的开关；等价于客户端 CLI 加 `--insecure` |
| `QATLAS_SHARE_ACCESS_TOKEN` | 可选的常驻 share token；只在你需要稳定分享链接时显式设置（alias: `SHARE_ACCESS_TOKEN`） |
| `QATLAS_USER_HEADER` | 可选的上游用户头；留空时 QuantumAtlas 不读取用户头（alias: `USER_HEADER`） |
| `QATLAS_SERVER_HOST` / `QATLAS_SERVER_PORT` | QuantumAtlas 服务监听地址和端口（alias: `SERVER_HOST` / `SERVER_PORT`） |
| `NEO4J_URI` / `NEO4J_USER` / `NEO4J_PASSWORD` | 图数据库连接配置（保留 Neo4j 官方驱动标准名） |
| `MINERU_*` | 使用 MinerU 解析时的可选配置（保留 MinerU 厂家标准名） |

## 反向代理与鉴权边界

QuantumAtlas 自己不负责浏览器 OAuth 登录流程。更推荐的方式是：

- 由反向代理、SSO 或 API gateway 负责浏览器登录。
- 登录成功后，上游可以把已认证用户标识注入一个用户头，例如 `X-Token-Subject`。
- QuantumAtlas 默认不读取用户头；只有显式设置 `USER_HEADER` 时才把它用于日志/审计，不用于鉴权。

推荐的路径边界：

- `/share/*` 公开，只校验 share token，不要求 OAuth。
- `/health` 可以按需要对负载均衡或监控开放。
- `/token` 必须要求已登录用户访问。
- `/api/*` 对浏览器和 CLI 请求都应经过鉴权层；如需审计用户，再由反向代理或 SSO 层注入你显式配置的 `USER_HEADER`。

## Share 机制

QuantumAtlas 的 share 是“按路径授权的公开链接”，不是用户登录态，也不是 API 鉴权。`/share/{token}` 和 `/share/{token}/{path}` 默认应允许公网访问；任何拿到 share URL 的人都能访问该 token 允许的资源。

当前有两类 share token：

- 登录用户创建的动态 share token：已登录用户通过受保护的 `POST /api/shares` 创建，记录保存在 `DATA_DIR/shares`。请求里可以指定 `paths`、`label` 和 `expires_in`；如果没有指定 `expires_in`，服务使用 `DEFAULT_SHARE_EXPIRES_IN`。这些 token 可以通过 `GET /api/shares` 查看、通过 `DELETE /api/shares/{token}` 撤销。
- 部署者配置的 `SHARE_ACCESS_TOKEN`：这是额外的、可选的、用户自定义的稳定分享入口。设置后，QuantumAtlas 会把它当作一个不写入 `DATA_DIR/shares`、不自动过期的内置 share token，用于访问 canonical paper assets：`papers/pdf`、`papers/markdown`、`papers/json`、`papers/images`。不需要稳定公开链接时不要设置它。

安全边界：

- `/api/shares` 是管理接口，必须在 Caddy、SSO 或 API gateway 层要求登录。
- `/share/*` 是公开资源入口，只校验 share token，不校验登录用户。
- share token 只授权配置记录中的资源路径；路径必须是相对路径，不能包含绝对路径、反斜杠或 `..`。
- `SHARE_ACCESS_TOKEN` 应使用足够长的随机值；不要用示例值、短词或可猜测字符串。

## Caddy 示例

下面是一个 caddy-security 的最小模板。它只表达推荐的路径边界，不绑定具体机器、云厂商、内网地址或个人域名：

- `atlas.example.com` 代表你的公网域名。
- QuantumAtlas 示例监听在 `127.0.0.1:4200`。
- 使用这个模板时，QuantumAtlas 默认不读取用户头；如需审计用户，可显式设置 `USER_HEADER=X-Token-Subject`，或改用 `X-Token-User-Name` / `X-Token-User-Email`。
- QuantumAtlas 已不需要自己实现 CLI bearer token 认证；如果历史部署还保留相关配置，删掉即可，不删也不会影响 caddy-security 的入口鉴权。
- caddy-security 可以同时接受浏览器 cookie 和 `Authorization: Bearer ...`：`set token sources header cookie` 表示从请求头或 cookie 取 token，`validate bearer header` 表示允许并校验 bearer header。
- 下面示例把 auth portal 挂在 `/auth/*` 下；也可以改成独立的 auth 子域名。重点是让 portal 路径和 QuantumAtlas 自己的 `/api/*`、前端 `/assets/*`、业务页面分开。

```caddyfile
{
    order authenticate before respond
    order authorize before basicauth

    security {
        oauth identity provider github {
            realm github
            driver github
            client_id {env.GITHUB_CLIENT_ID}
            client_secret {env.GITHUB_CLIENT_SECRET}
            scopes openid email profile
        }

        authentication portal atlas_portal {
            crypto default token lifetime 604800
            crypto key sign-verify {env.ATLAS_AUTH_SECRET}
            enable identity provider github

            # Optional: only set this when sharing login across subdomains.
            # cookie domain example.com

            transform user {
                match realm github
                regex match sub "github.com/(your-github-login-or-org-user)"
                action add role authp/user
                action add role authp/atlas
            }
        }

        authorization policy atlas {
            set auth url https://atlas.example.com/auth/
            set forbidden url https://atlas.example.com/forbidden
            crypto key verify {env.ATLAS_AUTH_SECRET}
            allow roles authp/atlas
            set token sources header cookie
            validate bearer header
            set user identity subject
            inject headers with claims
        }
    }
}

atlas.example.com {
    request_header -X-Token-User-Name
    request_header -X-Token-Subject
    request_header -X-Token-User-Email

    handle /auth/* {
        authenticate with atlas_portal
    }

    handle /forbidden {
        error "Unauthorized" 401
    }

    handle /health {
        reverse_proxy 127.0.0.1:4200
    }

    handle /share/* {
        reverse_proxy 127.0.0.1:4200
    }

    handle {
        authorize with atlas
        reverse_proxy 127.0.0.1:4200 {
            header_up -Authorization
        }
    }
}
```

## 运行建议

- 不要信任来自公网客户端自己带上的用户身份头；反向代理应先清理可能由客户端伪造的身份头，再注入已认证用户信息。
- `/api/*` 必须经过鉴权层；不要把 API 放到未执行 `authorize` 的公开 `handle` 里。
- 对已经由 `authorize with atlas` 验证过的 QuantumAtlas 请求，建议在对应的 `reverse_proxy` 中使用 `header_up -Authorization`，避免把入口 bearer token 继续传给应用。
- `/share/*` 公开并不意味着管理接口也应公开；`/api/shares` 仍应受保护。
- 如果启用了 MinerU，并且它需要回拉 PDF，`QATLAS_SERVER_URL` 必须能从 MinerU 所在环境访问到。
