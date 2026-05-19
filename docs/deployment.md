# Deployment

## 适用范围

这份文档描述的是 QuantumAtlas 服务的部署方式。

目标是把下面几件事拆清楚：

- 如何本地启动一个可工作的服务。
- 如何把它安装成长期运行的 systemd 服务。
- 如何在公网入口前放置反向代理和鉴权层。
- 如何在不暴露真实机器名、真实地址或私有路由结构的前提下，给出可复用的 Caddy 示例。

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

只把 Wiki 外置到独立 Git checkout（方便多人 PR / review）；论文资产（`RAW_DIR`）和运行状态（`DATA_DIR`）默认就在仓库内即可，不必单独搬迁路径：

```env
WIKI_DIR=../QuantumAtlas-Wiki
# RAW_DIR=raw          # 默认值，通常不必显式设置
# DATA_DIR=data        # 默认值
NEO4J_URI=bolt://127.0.0.1:7687
SERVER_HOST=127.0.0.1
SERVER_PORT=4200
PUBLIC_BASE_URL=https://atlas.example.com
```

建议：

- 应用仓库按 release tag 或受控分支部署。
- Wiki 仓库单独 checkout，并允许更高频更新；server 侧 checkout 应保持干净，只通过 `git pull --ff-only` 消费远端内容。
- 运行 QuantumAtlas 的服务用户默认只需要读取 `WIKI_DIR`；如果启用 `/api/wiki/sync/pull`，还需要对该 Git checkout 有 fast-forward 更新权限。服务端不会生成或修改 Wiki 页面，Wiki 内容修改应在用户端或独立的 `QuantumAtlas-Wiki` checkout 中完成。
- 运行 QuantumAtlas 的服务用户应对 `RAW_DIR` 和 `DATA_DIR` 有写权限：`RAW_DIR` 用于保存论文资产，`DATA_DIR` 用于保存 share、ingest 状态和版本 manifest。如果要外置（例如挂载到大容量盘），可以显式覆盖，但**不是必需的**。
- 内容生产、LLM 生成、人工编辑和审阅走 `QuantumAtlas-Wiki` 的普通 Git 流程；QuantumAtlas server 不提供 push API，也不通过 Web UI 直接写 Wiki 页面。
- 若 `/api/wiki/sync/status` 提示 Wiki checkout 不在 `main` 或 `master`，应检查部署分支是否符合预期。
- Neo4j 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 `PUBLIC_BASE_URL`。

## 核心环境变量

完整内置默认值以 `atlas/server/config.py` 为准；`.env.example` 只是覆盖模板，不应直接当成生产默认配置。公网部署通常最关心下面这些：

| 变量 | 说明 |
|------|------|
| `WIKI_DIR` | Wiki 知识库目录，推荐指向独立 Git checkout |
| `RAW_DIR` | canonical 论文资产根目录 |
| `DATA_DIR` | 任务、share、ingest 状态目录 |
| `PUBLIC_BASE_URL` | 对外唯一根地址，client、share 链接和 MinerU URL 都基于它 |
| `SHARE_ACCESS_TOKEN` | 可选的常驻 share token；只在你需要稳定分享链接时显式设置 |
| `USER_HEADER` | 可选的上游用户头；留空时 QuantumAtlas 不读取用户头 |
| `SERVER_HOST` / `SERVER_PORT` | QuantumAtlas 服务监听地址和端口 |
| `NEO4J_URI` / `NEO4J_USER` / `NEO4J_PASSWORD` | 图数据库连接配置 |
| `MINERU_*` | 使用 MinerU 解析时的可选配置 |

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
- 如果启用了 MinerU，并且它需要回拉 PDF，`PUBLIC_BASE_URL` 必须能从 MinerU 所在环境访问到。
