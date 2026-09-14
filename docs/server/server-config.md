# qatlasd 服务端配置（YAML）

`qatlasd` 是 Go 程序。业务配置只从 **YAML 文件**读取，默认 `~/.qatlas/config.yaml`，用 `--config /path/to/config.yaml` 选择其他文件。旧的 Python/Go dotenv 模式已经移除：没有 `CLI > env > .env` 的业务配置优先级，也没有 `--postgres-dsn`、`--system-pat`、`--dotenv-path` 等旧参数。

完整字段由 [`config.example.yaml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/config.example.yaml) 与 `internal/config/config.go` 定义；`qatlasd config init` 的内嵌模板与根示例同步检查。不要把本页示例当成需要全部填写的生产配置。

## 配置文件与命令 { #8-qatlasd-config-子命令 }

```bash
# 创建默认模板，mode 0600；已有文件时拒绝覆盖
qatlasd config init
# 或在调用者有写权限的目录创建
qatlasd config init --config /path/to/config.yaml

# 查看选择的路径和有效配置（敏感值默认脱敏）
qatlasd config path --config /path/to/config.yaml
qatlasd config show --config /path/to/config.yaml
# 编辑好以后启动
qatlasd --config /path/to/config.yaml serve
```

- 配置路径只由 `--config` 或默认路径决定，不搜索工作目录中的 `.env`。
- YAML 未知字段、错误类型/启用部分的非法数值或时长会报错，不能靠拼写错误默默使用默认值。
- 空文件或全注释模板应用默认值；文件不存在会提示 `config init`。
- `paths.*` 等路径按配置文件所在目录解析相对值；生产建议绝对路径。
- PocketBase 的显式 `--http`、`--dir` 优先于 YAML 注入的对应启动参数。这不意味着所有业务字段都有 flag。
- 修改业务配置后需要重启。用户必须自行决定并授权生产的重启/迁移操作。

## 最小本地实例

使用已安装的发布版本，或已经按[开发指南](../contributing.md)构建完整内嵌 UI 的本地程序：

```yaml
http_addr: 127.0.0.1:4200
paths:
  raw_dir: ./state/raw
  data_dir: ./state/data
  pb_data_dir: ./state/pb_data
```

没有 PostgreSQL/S3/OAuth 时可以启动本地进程并查看匿名健康与 UI；这不是完整生产配置。未配置 registry 的端点会按各自契约降级；没有登录配置也不等于自动授予论文或管理权限。

## 常用配置段

| 段/键 | 用途 | 边界 |
|---|---|---|
| `http_addr` | HTTP 监听，默认 `127.0.0.1:4200` | 对外访问应配置反向代理；Docker 的 CMD 显式覆盖容器内监听 |
| `public_url` | 对外 canonical origin | OAuth 回调和公开链接使用，不是监听地址 |
| `paths.raw_dir/data_dir/pb_data_dir` | 对象本地后端、业务状态、PocketBase 数据目录 | 默认 `${XDG_DATA_HOME:-~/.local/share}/qatlasd/{raw,data,pb_data}` |
| `postgres` | paper registry 与 OpenAlex corpus | 配置后启动可能应用数据库迁移，只指向已授权数据库 |
| `s3` | 三类资产桶与服务账号 | 六个核心字段必须全部填写或全部省略；半配置会阻止 serve |
| `auth` | GitHub/Gitea OAuth 与登录/管理名单 | OAuth secret 不进入前端资源；GitHub 空 allowlist 默认拒绝登录 |
| `system_pat` | 可选运维 bearer 与 scope | token 至少 16 字符，需保护配置文件；不是 Vite 前端配置 |
| `search` | 本地/上游检索与可选远程 app | 查询可能访问外部 API；自托管 app 有独立配置 |
| `rag.remote` / `match.remote` | 可选外部服务 | 不配置时功能降级，不在主仓发布这些 app |
| `paper_access` | 自托管论文访问与服务端转换 | 启用会改变资源访问和外部 API 使用边界 |
| `downloader` | 本机、旧 proxy、主动 worker fleet | 详见[下载器](downloader.md)与[worker](downloader-workers.md) |
| `plugins` | 插件发现、连接与 RPC 配置 | 不把外部插件的业务配置放进主服务 |
| `force_tcp4` / `skip_pb_data_lock` | 特定环境诊断开关 | 不建议在生产绕过数据目录锁 |

### PostgreSQL 与存储

```yaml
postgres:
  dsn: postgres://qatlas:REPLACE_ME@127.0.0.1:5432/qatlas_test?sslmode=disable
  max_conns: 10
  corpus_ensure_indexes: true
```

DSN 示例仅供一次性本地测试库。正式环境应按部署策略使用 TLS 与最小权限；不要在聊天、日志或 Git 中保存真实凭据。paper registry 与 OpenAlex corpus 共用连接池，但不是同一类数据。已有大型 corpus 的索引策略见 [ADR 0013](../adr/0013-corpus-schema-base-index-split.md)。

S3 的核心键是 `endpoint`、`bucket_pdf`、`bucket_md`、`bucket_images`、`access_key_id`、`secret_access_key`。全部省略时使用 `paths.raw_dir` 的本地存储；部分填写时 `serve` 拒绝启动。`s3.public_endpoint` 是可选的公开预签名 URL 入口，不代替服务端连接端点。

### OAuth 与运维权限

```yaml
public_url: https://atlas.example.com
auth:
  github_client_id: REPLACE_ME
  github_client_secret: REPLACE_ME
  allowed_logins: [your-login]
  admin_logins: [your-login]
```

OAuth app 的回调为 `https://<server>/auth/callback`。GitHub 的 `allowed_logins` / `admin_logins` 均为空时默认拒绝 GitHub 登录；Gitea 的账号准入由该实例策略决定，两个提供方的管理名单分开配置。详见[鉴权模型](../concepts/auth-model.md)与[OAuth](github-oauth.md)。

可选运维 token 使用 `system_pat.token` 与 `system_pat.scopes`，普通调用者应使用自己账号的 PAT。配置与用户权限要分开：前端 fake-auth 只绕过 UI 登录显示，不授予后端权限。

### 搜索与论文访问 { #server-side-mineru }

```yaml
search:
  providers: [catalog, arxiv, openalex]
  remote:
    enabled: false
    url: ""
    token: ""
    timeout: 60s
  agentic:
    backend: remote
    daily_limit: 10000
    price_per_mtok: 0
paper_access:
  enabled: false
```

`search.remote` 接入独立 `qatlas-search`，支持 multi/backend 与 agentic 路径；本地 agentic 后端的 `search.agentic.local` 详见完整 YAML 模板。`paper_access.mineru` 下的 `api_tokens`、`api_base_url`、模型、轮询和并发设置仅供服务端，不是独立客户端的配置文件。启用转换会消耗外部服务额度；默认测试不应使用这些真实资源。PDF 的对外交付仍以当前 API 契约为准，不能因开启此配置就假定 PDF 下载已恢复。

### 下载器与 worker fleet

`downloader.enabled` 与 `paper_access.enabled` 共同控制下载器。可选路径：

- `downloader.browser.cdp_url` / `timeout`：连接已获授权的 Chromium CDP 实例。
- `downloader.proxy.url/token/timeout`：兼容旧的主服务主动调用代理。
- `downloader.remote.enabled`：启用主动出站 worker，需 PostgreSQL；不能同时配置旧 proxy URL。
- `downloader.remote.max_in_flight`、`max_worker_in_flight`、`max_worker_attempts` 及超时/租约字段：约束 fleet 执行容量，不等于本地下载槽位。
- `downloader.remote.spool_dir/spool_max_bytes`：归档暂存及容量，部署时必须持久化相应目录。
- `downloader.agent`：可选 LLM/Claude 链接提取后备路径，凭据与权限属于运行该后端的实例。

不要把独立 `downloaderworker` 的 `DL_WORKER_*` 环境变量当作主服务 YAML；二者配置边界与运行身份不同。完整说明见[下载节点](downloader-workers.md)。

## UI、文档与版本不是业务配置项

- 程序优先使用内嵌 UI；普通 Go 源码安装自动获取自身精确版本的 Release UI、校验并缓存。无需在 YAML 指定资源路径、版本或 URL。见[两种安装方式](install.md)。
- 运行版本来自 GoReleaser 的 `main.version` 或 Go 模块 build info，不从 YAML 或已退役的 Python 包版本推导。
- `~/.qatlas/docs/doc` 与 `devdoc` 是已有运维文档覆盖机制，不是首次安装必须准备的资源目录。每站在启动时选择非空磁盘目录，否则用当前 UI bundle；更新已选目录内部内容不需重启，切换来源需要重启。
- `/devdoc` 有 HTTP 管理员门控，但公开二进制/UI 包包含可直接读取的文档，不能存放机密。

## 环境变量拒绝与旧配置迁移

主进程遇到旧业务环境变量会明确报错，包含变量**名称**而不输出值。包括 `QATLAS_*`（明确的工具/测试白名单除外）、`MINERU_*`、`NEO4J_*`、`POSTGRES_*` 和旧的 `GITHUB_CLIENT_ID` 等。

| 旧配置示意（不能继续 export） | 当前 YAML |
|---|---|
| `QATLAS_POSTGRES_DSN` | `postgres.dsn` |
| `QATLAS_PUBLIC_URL` | `public_url` |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | `auth.github_client_id` / `auth.github_client_secret` |
| `QATLAS_RAW_DIR` / `QATLAS_PB_DATA_DIR` | `paths.raw_dir` / `paths.pb_data_dir` |
| `QATLAS_SYSTEM_PAT` | `system_pat.token` |
| `MINERU_API_TOKENS` | `paper_access.mineru.api_tokens`（列表） |

先备份旧配置并填写新的 YAML，再移除服务环境中对应变量；不要 `source` 旧 `.env` 后启动。Docker 要挂载 YAML，不用 `--env-file` 传业务配置；服务注册使用 `--config`，不是 `--dotenv-path`。历史配置可从旧 tag 查看，本仓当前说明不再将旧命令作为可执行示例。

安装器、Compose 镜像版本、Vite、本地测试和下载 worker 的环境变量仍各有用途，但不是主服务的业务配置接口，见[环境变量边界](../reference/env-vars.md)。
