# Deployment

## 适用范围

这份文档描述的是 QuantumAtlas 服务本体的部署方式，而不是某一台具体机器的私有配置。

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

开发环境可以直接用仓库内默认目录，但生产更建议外置运行时数据：

```env
WIKI_DIR=/srv/quantumatlas/wiki
RAW_DIR=/srv/quantumatlas/raw
DATA_DIR=/srv/quantumatlas/data
NEO4J_URI=bolt://127.0.0.1:7687
SERVER_HOST=127.0.0.1
SERVER_PORT=4200
PUBLIC_BASE_URL=https://atlas.example.com
SHARE_ACCESS_TOKEN=replace-with-a-long-random-string
CLI_TOKEN_SECRET=replace-with-a-different-long-random-string
```

建议：

- 应用仓库按 release tag 或受控分支部署。
- Wiki 仓库单独 checkout，并允许更高频更新；server 侧 checkout 应保持干净，只通过 `git pull --ff-only` 消费远端内容。
- 内容生产、LLM 生成、人工编辑和审阅走 `QuantumAtlas-Wiki` 的普通 Git 流程；QuantumAtlas server 不提供 push API，也不通过 Web UI 直接写 Wiki 页面。
- 若 `/api/wiki/sync/status` 提示 Wiki checkout 不在 `main` 或 `master`，应检查部署分支是否符合预期。
- Neo4j 仅对后端服务暴露，不直接开放到公网。
- 公开访问统一走 `PUBLIC_BASE_URL`。

## 核心环境变量

完整默认值以 `.env.example` 和 `atlas/server/config.py` 为准。公网部署通常最关心下面这些：

| 变量 | 说明 |
|------|------|
| `WIKI_DIR` | Wiki 知识库目录，推荐指向独立 Git checkout |
| `RAW_DIR` | canonical 论文资产根目录 |
| `DATA_DIR` | 任务、share、ingest 状态目录 |
| `PUBLIC_BASE_URL` | 对外唯一根地址，client、share 链接和 MinerU URL 都基于它 |
| `SHARE_ACCESS_TOKEN` | 可选的常驻 share token；用于公开资源访问 |
| `USER_HEADER` | 上游注入的用户头，默认是 `X-Forwarded-User` |
| `CLI_TOKEN_SECRET` | CLI bearer token 的签名密钥 |
| `CLI_TOKEN_EXPIRES_IN` | CLI token 默认有效期 |
| `SERVER_HOST` / `SERVER_PORT` | QuantumAtlas 服务监听地址和端口 |
| `NEO4J_URI` / `NEO4J_USER` / `NEO4J_PASSWORD` | 图数据库连接配置 |
| `MINERU_*` | 使用 MinerU 解析时的可选配置 |

## 反向代理与鉴权边界

QuantumAtlas 自己不负责浏览器 OAuth 登录流程。更推荐的方式是：

- 由反向代理、SSO 或 API gateway 负责浏览器登录。
- 登录成功后，由上游把可信用户标识注入 `USER_HEADER`。
- QuantumAtlas 只读取这个用户头做审计和 CLI token 签发。

推荐的路径边界：

- `/share/*` 公开，只校验 share token，不要求 OAuth。
- `/health` 可以按需要对负载均衡或监控开放。
- `/cli-token` 和 `/api/auth/cli-token` 必须要求已登录用户访问。
- `/api/*` 对浏览器请求可以走 OAuth；对带 `Authorization: Bearer ...` 的 CLI 请求可以直接转发，让 QuantumAtlas 自己验签。

## 脱敏 Caddy 示例

下面这个示例假设：

- QuantumAtlas 监听在 `127.0.0.1:4200`。
- 另有一个独立的 OAuth 网关监听在 `127.0.0.1:4180`。
- OAuth 网关认证通过后，会返回 `X-Auth-Request-User`。
- QuantumAtlas 期望从 `X-Forwarded-User` 读取可信用户标识。

```caddyfile
atlas.example.com {
    encode zstd gzip

    # OAuth gateway endpoints (example: oauth2-proxy / auth portal)
    handle /oauth2/* {
        reverse_proxy 127.0.0.1:4180
    }

    # Public paths: share links and health checks stay outside browser login.
    @public {
        path /health /share/*
    }
    handle @public {
        reverse_proxy 127.0.0.1:4200
    }

    # CLI/API requests that already carry a bearer token can go straight through.
    @api_with_bearer {
        path /api/*
        header Authorization *
    }
    handle @api_with_bearer {
        reverse_proxy 127.0.0.1:4200
    }

    # Browser-facing routes that require an authenticated user.
    @needs_login {
        path / /wiki* /graph* /api/* /cli-token /api/auth/cli-token
    }
    handle @needs_login {
        forward_auth 127.0.0.1:4180 {
            uri /oauth2/auth
            copy_headers X-Auth-Request-User
        }

        reverse_proxy 127.0.0.1:4200 {
            header_up -X-Forwarded-User
            header_up X-Forwarded-User {http.request.header.X-Auth-Request-User}
        }
    }

    # Optional: make the landing page public by changing the matcher above.
    handle {
        reverse_proxy 127.0.0.1:4200
    }
}
```

这个示例只表达部署边界，不绑定某个具体域名、云厂商、内网网段或主机名。你可以把 `127.0.0.1:4180` 换成任何能够完成 OAuth/SSO 的上游服务。

## 运行建议

- 不要信任来自公网客户端自己带上的 `X-Forwarded-User`；应由反向代理清理后重新注入。
- `/share/*` 公开并不意味着管理接口也应公开；`/api/shares` 仍应受保护。
- `CLI_TOKEN_SECRET` 必须是与 `SHARE_ACCESS_TOKEN` 不同的独立随机串。
- 如果启用了 MinerU，并且它需要回拉 PDF，`PUBLIC_BASE_URL` 必须能从 MinerU 所在环境访问到。
